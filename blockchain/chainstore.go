// Copyright (c) 2017-2020 The Elastos Foundation
// Use of this source code is governed by an MIT
// license that can be found in the LICENSE file.
//

package blockchain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/common/config"
	"github.com/elastos/Elastos.ELA/common/log"
	. "github.com/elastos/Elastos.ELA/core/types"
	"github.com/elastos/Elastos.ELA/core/types/outputpayload"
	"github.com/elastos/Elastos.ELA/core/types/payload"
	_ "github.com/elastos/Elastos.ELA/database/ffldb"
	"github.com/elastos/Elastos.ELA/events"
	"github.com/elastos/Elastos.ELA/utils"

	"github.com/robfig/cron"
)

const (
	//INCOME                      string = "income"
	//SPEND                       string = "spend"
	RECEIVED                    string = "received"
	SENT                        string = "sent"
	MOVED                       string = "moved"
	ELA                         uint64 = 100000000
	DPOS_CHECK_POINT                   = 290000
	CHECK_POINT_ROLLBACK_HEIGHT        = 100
)

type ProducerState byte

type ProducerInfo struct {
	Payload   *payload.ProducerInfo
	RegHeight uint32
	Vote      Fixed64
}

type ChainStore struct {
	levelDB            IStore
	fflDB              IFFLDBChainStore
	currentBlockHeight uint32
	persistMutex       sync.Mutex
}

func NewChainStore(dataDir string, params *config.Params) (IChainStore, error) {
	db, err := NewLevelDB(filepath.Join(dataDir, "chain"))
	if err != nil {
		return nil, err
	}
	fflDB, err := NewChainStoreFFLDB(dataDir, params)
	if err != nil {
		return nil, err
	}
	s := &ChainStore{
		levelDB: db,
		fflDB:   fflDB,
	}

	return s, nil
}

func (c *ChainStore) CloseLeveldb() {
	c.levelDB.Close()
}

func (c *ChainStore) Close() {
	c.persistMutex.Lock()
	defer c.persistMutex.Unlock()
	if err := c.fflDB.Close(); err != nil {
		log.Error("fflDB close failed:", err)
	}
}

func (c *ChainStore) IsTxHashDuplicate(txID Uint256) bool {
	txn, _, err := c.fflDB.GetTransaction(txID)
	if err != nil || txn == nil {
		return false
	}

	return true
}

func (c *ChainStore) IsSidechainTxHashDuplicate(sidechainTxHash Uint256) bool {
	return c.GetFFLDB().IsTx3Exist(&sidechainTxHash)
}

func (c *ChainStore) IsDoubleSpend(txn *Transaction) bool {
	if len(txn.Inputs) == 0 {
		return false
	}
	for i := 0; i < len(txn.Inputs); i++ {
		txID := txn.Inputs[i].Previous.TxID
		unspents, err := c.GetFFLDB().GetUnspent(txID)
		if err != nil {
			return true
		}
		findFlag := false
		for k := 0; k < len(unspents); k++ {
			if unspents[k] == txn.Inputs[i].Previous.Index {
				findFlag = true
				break
			}
		}
		if !findFlag {
			return true
		}
	}

	return false
}

func (c *ChainStore) RollbackBlock(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	now := time.Now()
	err := c.handleRollbackBlockTask(b, node, confirm, medianTimePast)
	tcall := float64(time.Now().Sub(now)) / float64(time.Second)
	log.Debugf("handle block rollback exetime: %g", tcall)
	return err
}

func (c *ChainStore) GetTransaction(txID Uint256) (*Transaction, uint32, error) {
	return c.fflDB.GetTransaction(txID)
}

func (c *ChainStore) GetTxReference(tx *Transaction) (map[*Input]*Output, error) {
	if tx.TxType == RegisterAsset {
		return nil, nil
	}
	txOutputsCache := make(map[Uint256][]*Output)
	//UTXO input /  Outputs
	reference := make(map[*Input]*Output)
	// Key index，v UTXOInput
	for _, input := range tx.Inputs {
		txID := input.Previous.TxID
		index := input.Previous.Index
		if outputs, ok := txOutputsCache[txID]; ok {
			reference[input] = outputs[index]
		} else {
			transaction, _, err := c.GetTransaction(txID)

			if err != nil {
				return nil, errors.New("GetTxReference failed, previous transaction not found")
			}
			if int(index) >= len(transaction.Outputs) {
				return nil, errors.New("GetTxReference failed, refIdx out of range")
			}
			reference[input] = transaction.Outputs[index]
			txOutputsCache[txID] = transaction.Outputs
		}
	}
	return reference, nil
}

func (c *ChainStore) rollback(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	if err := c.fflDB.RollbackBlock(b, node, confirm, medianTimePast); err != nil {
		return err
	}
	atomic.StoreUint32(&c.currentBlockHeight, b.Height-1)

	return nil
}

func (c *ChainStore) persist(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	c.persistMutex.Lock()
	defer c.persistMutex.Unlock()

	if err := c.fflDB.SaveBlock(b, node, confirm, medianTimePast); err != nil {
		return err
	}
	return nil
}

func (c *ChainStore) GetFFLDB() IFFLDBChainStore {
	return c.fflDB
}

func (c *ChainStore) SaveBlock(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	log.Debug("SaveBlock()")

	now := time.Now()
	err := c.handlePersistBlockTask(b, node, confirm, medianTimePast)

	tcall := float64(time.Now().Sub(now)) / float64(time.Second)
	log.Debugf("handle block exetime: %g num transactions:%d",
		tcall, len(b.Transactions))
	return err
}

func (c *ChainStore) handleRollbackBlockTask(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	_, err := c.fflDB.GetBlock(b.Hash())
	if err != nil {
		log.Errorf("block %x can't be found", BytesToHexString(b.Hash().Bytes()))
		return err
	}
	return c.rollback(b, node, confirm, medianTimePast)
}

func (c *ChainStore) handlePersistBlockTask(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	if b.Header.Height <= c.currentBlockHeight {
		return errors.New("block height less than current block height")
	}

	return c.persistBlock(b, node, confirm, medianTimePast)
}

func (c *ChainStore) persistBlock(b *Block, node *BlockNode,
	confirm *payload.Confirm, medianTimePast time.Time) error {
	err := c.persist(b, node, confirm, medianTimePast)
	if err != nil {
		log.Fatal("[persistBlocks]: error to persist block:", err.Error())
		return err
	}

	atomic.StoreUint32(&c.currentBlockHeight, b.Height)
	return nil
}

func (c *ChainStore) GetConfirm(hash Uint256) (*payload.Confirm, error) {
	var confirm = new(payload.Confirm)
	prefix := []byte{byte(DATAConfirm)}
	confirmBytes, err := c.levelDB.Get(append(prefix, hash.Bytes()...))
	if err != nil {
		return nil, err
	}

	if err = confirm.Deserialize(bytes.NewReader(confirmBytes)); err != nil {
		return nil, err
	}

	return confirm, nil
}

func (c *ChainStore) GetHeight() uint32 {
	return atomic.LoadUint32(&c.currentBlockHeight)
}

func (c *ChainStore) SetHeight(height uint32) {
	atomic.StoreUint32(&c.currentBlockHeight, height)
}

var (
	MINING_ADDR  = Uint168{}
	ELA_ASSET, _ = Uint256FromHexString("b037db964a231458d2d6ffd5ea18944c4f90e63d547c5d3b9874df66a4ead0a3")
	DBA          *Dba
)

type ChainStoreExtend struct {
	IChainStore
	IStore
	chain    *BlockChain
	taskChEx chan interface{}
	quitEx   chan chan bool
	mu       sync.RWMutex
	*cron.Cron
	rp         chan bool
	checkPoint bool
}

func (c *ChainStoreExtend) AddTask(task interface{}) {
	c.taskChEx <- task
}

func NewChainStoreEx(chain *BlockChain, chainstore IChainStore, filePath string) (*ChainStoreExtend, error) {
	if !utils.FileExisted(filePath) {
		os.MkdirAll(filePath, 0700)
	}
	st, err := NewLevelDB(filePath)
	if err != nil {
		return nil, err
	}
	DBA, err = NewInstance(filePath)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	err = InitDb(DBA)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	c := &ChainStoreExtend{
		IChainStore: chainstore,
		IStore:      st,
		chain:       chain,
		taskChEx:    make(chan interface{}, 100),
		quitEx:      make(chan chan bool, 1),
		Cron:        cron.New(),
		mu:          sync.RWMutex{},
		rp:          make(chan bool, 1),
		checkPoint:  true,
	}
	StoreEx = c
	MemPoolEx = MemPool{
		c:    StoreEx,
		is_p: make(map[Uint256]bool),
		p:    make(map[string][]byte),
	}
	go c.loop()
	go c.initTask()
	events.Subscribe(func(e *events.Event) {
		switch e.Type {
		case events.ETBlockConnected:
			b, ok := e.Data.(*Block)
			if ok {
				go StoreEx.AddTask(b)
			}
		//	TODO：是否需要维护第二个交易池交易列表？
		case events.ETTransactionAccepted:
			tx, ok := e.Data.(*Transaction)
			if ok {
				go MemPoolEx.AppendToMemPool(tx)
			}
		}
	})
	return c, nil
}

func (c *ChainStoreExtend) Close() {

}
//todo:清理无效代码，处理投票数据，除persistBestHeight外，还需要获得交易的投票类型
func (c *ChainStoreExtend) processVote(block *Block, voteTxHolder *map[string]TxType) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	bestHeight, _ := c.GetBestHeightExt()
	if block.Height >= DPOS_CHECK_POINT {
		//db, err := DBA.Begin()
		//if err != nil {
		//	return err
		//}
		if block.Height > bestHeight {
			//TODO：不需要缓存投票数据，不将投票数据缓存db，但需要获得交易的投票类型
			//err = doProcessVote(block, voteTxHolder, db)
			err := doProcessVote(block, voteTxHolder)
			if err != nil {
				//db.Rollback()
				return err
			}
		//} else {
			//删除db中block.height之后的投票信息
			//err = c.cleanInvalidBlock(block.Height, db)
			//if err != nil {
			//	return err
			//}
			//for z := block.Height; z <= bestHeight; z++ {
			//	blockHash, err := c.chain.GetBlockHash(z)
			//	if err != nil {
			//		db.Rollback()
			//		return err
			//	}
			//	_block, err := c.chain.GetBlockByHash(blockHash)
			//	if err != nil {
			//		db.Rollback()
			//		return err
			//	}
			//	//err = doProcessVote(_block, voteTxHolder, db)
			//	//if err != nil {
			//	//	db.Rollback()
			//	//	return err
			//	//}
			//}
		}
		//err = db.Commit()
		//if err != nil {
		//	return err
		//}
		//if len(c.rp) > 0 {
		//	//c.renewProducer()
		//	//c.renewCrCandidates()
		//	<-c.rp
		//}
	}
	c.persistBestHeight(block.Height)
	return nil
}

//TODO：移除注释代码，处理block内的交易，不需要更新db中的投票信息，但需要获得交易的投票类型
//func doProcessVote(block *Block, voteTxHolder *map[string]TxType, db *sql.Tx) error {
func doProcessVote(block *Block, voteTxHolder *map[string]TxType) error {
	for i := 0; i < len(block.Transactions); i++ {
		tx := block.Transactions[i]
		version := tx.Version
		txid, err := ReverseHexString(tx.Hash().String())
		vt := 0
		if err != nil {
			return err
		}
		if version == 0x09 {
			vout := tx.Outputs
			//stmt 用于dpos投票
			//stmt, err := db.Prepare("insert into chain_vote_info (producer_public_key,vote_type,txid,n,`value`,outputlock,address,block_time,height) values(?,?,?,?,?,?,?,?,?)")
			//if err != nil {
			//	return err
			//}
			//stmt1 用于CR投票
			//stmt1, err1 := db.Prepare("insert into chain_vote_cr_info (did,vote_type,txid,n,`value`,outputlock,address,block_time,height) values(?,?,?,?,?,?,?,?,?)")
			//if err1 != nil {
			//	return err1
			//}
			for _, v := range vout {
				if v.Type == 0x01 && v.AssetID == *ELA_ASSET {
					payload, ok := v.Payload.(*outputpayload.VoteOutput)
					if !ok || payload == nil {
						continue
					}
					contents := payload.Contents
					//voteVersion := payload.Version
					//value := v.Value.String()
					//address, err := v.ProgramHash.ToAddress()
					//if err != nil {
					//	return err
					//}
					//outputlock := v.OutputLock
					for _, cv := range contents {
						votetype := cv.VoteType
						votetypeStr := ""
						if votetype == 0x00 {
							votetypeStr = "Delegate"
						} else if votetype == 0x01 {
							votetypeStr = "CRC"
						} else {
							continue
						}

						for _, _ = range cv.CandidateVotes {
							//if voteVersion == outputpayload.VoteProducerAndCRVersion {
							//	value = candidate.Votes.String()
							//}
							//var err error
							if votetypeStr == "Delegate" {
								if vt != 3 {
									if vt == 2 {
										vt = 3
									} else if vt == 0 {
										vt = 1
									}
								}
								//_, err = stmt.Exec(BytesToHexString(candidate.Candidate), votetypeStr, txid, n, value, outputlock, address, block.Header.Timestamp, block.Header.Height)
							} else {
								if vt != 3 {
									if vt == 1 {
										vt = 3
									} else if vt == 0 {
										vt = 2
									}
								}
								//didbyte, err := Uint168FromBytes(candidate.Candidate)
								//if err != nil {
								//	return err
								//}
								//did, err := didbyte.ToAddress()
								//if err != nil {
								//	return err
								//}
								//_, err = stmt1.Exec(did, votetypeStr, txid, n, value, outputlock, address, block.Header.Timestamp, block.Header.Height)
							}
							//if err != nil {
							//	return err
							//}
						}
					}
				}
			}
			//stmt.Close()
			//stmt1.Close()
		}

		if vt == 1 {
			(*voteTxHolder)[txid] = DPoS
		} else if vt == 2 {
			(*voteTxHolder)[txid] = CRC
		} else if vt == 3 {
			(*voteTxHolder)[txid] = DPoSAndCRC
		}

		// remove canceled vote
		//vin := tx.Inputs
		//prepStat, err := db.Prepare("select * from chain_vote_info where txid = ? and n = ?")
		//if err != nil {
		//	return err
		//}
		//stmt, err := db.Prepare("update chain_vote_info set is_valid = 'NO',cancel_height=? where txid = ? and n = ? ")
		//if err != nil {
		//	return err
		//}
		//prepStat1, err := db.Prepare("select * from chain_vote_cr_info where txid = ? and n = ?")
		//if err != nil {
		//	return err
		//}
		//stmt1, err := db.Prepare("update chain_vote_cr_info set is_valid = 'NO',cancel_height=? where txid = ? and n = ? ")
		//if err != nil {
		//	return err
		//}
		//for _, v := range vin {
		//	txhash, _ := ReverseHexString(v.Previous.TxID.String())
		//	vout := v.Previous.Index
			//r, err := prepStat.Query(txhash, vout)
			//if err != nil {
			//	return err
			//}
			//if r.Next() {
			//	_, err = stmt.Exec(block.Header.Height, txhash, vout)
			//	if err != nil {
			//		return err
			//	}
			//}

			//r1, err := prepStat1.Query(txhash, vout)
			//if err != nil {
			//	return err
			//}
			//if r1.Next() {
			//	_, err = stmt1.Exec(block.Header.Height, txhash, vout)
			//	if err != nil {
			//		return err
			//	}
			//}
		//}
		//stmt.Close()
		//stmt1.Close()
	}
	return nil
}

func (c *ChainStoreExtend) assembleRollbackBlock(rollbackStart uint32, blk *Block, blocks *[]*Block) error {
	for i := rollbackStart; i < blk.Height; i++ {
		blockHash, err := c.chain.GetBlockHash(i)
		if err != nil {
			return err
		}
		b, err := c.chain.GetBlockByHash(blockHash)
		if err != nil {
			return err
		}
		*blocks = append(*blocks, b)
	}
	return nil
}

//TODO-remove：删除db中的投票信息，因不缓存投票数据，故可以删除此函数
//func (c *ChainStoreExtend) cleanInvalidBlock(height uint32, db *sql.Tx) error {
//	stmt, err := db.Prepare("delete from chain_vote_info where height >= ?")
//	if err != nil {
//		return err
//	}
//	stmt1, err := db.Prepare("delete from chain_vote_cr_info where height >= ?")
//	if err != nil {
//		return err
//	}
//
//	_, err = stmt.Exec(height)
//	if err != nil {
//		return err
//	}
//	_, err = stmt1.Exec(height)
//	if err != nil {
//		return err
//	}
//
//	stmt.Close()
//	stmt1.Close()
//	return nil
//}

func (c *ChainStoreExtend) persistTxHistory(blk *Block) error {
	var blocks []*Block
	var rollbackStart uint32 = 0
	if c.checkPoint {
		bestHeight, err := c.GetBestHeightExt()
		if err == nil && bestHeight > CHECK_POINT_ROLLBACK_HEIGHT {
			rollbackStart = bestHeight - CHECK_POINT_ROLLBACK_HEIGHT
		}
		c.assembleRollbackBlock(rollbackStart, blk, &blocks)
		c.checkPoint = false
	} else if blk.Height > DPOS_CHECK_POINT {
		rollbackStart = blk.Height - 5
		c.assembleRollbackBlock(rollbackStart, blk, &blocks)
	}

	blocks = append(blocks, blk)

	for _, block := range blocks {
		//todo-remove 注释: 检查block[height]是否已经在数据库中，如已存储则跳过，继续处理后续区块
		_, err := c.GetStoredHeightExt(block.Height)
		if err == nil {
			continue
		}
		//process vote
		voteTxHolder := make(map[string]TxType)
		//todo: check if need to remove
		err = c.processVote(block, &voteTxHolder)
		if err != nil {
			return err
		}
		//err = c.persistBestHeight(block.Height)
		//if err != nil {
		//	return err
		//}

		txs := block.Transactions
		txhs := make([]TransactionHistory, 0)
		//pubks := make(map[Uint168][]byte)
		//dposReward := make(map[Uint168]Fixed64)
		for i := 0; i < len(txs); i++ {
			tx := txs[i]
			txid, err := ReverseHexString(tx.Hash().String())
			if err != nil {
				return err
			}
			var memo []byte
			//var signedAddress string
			//var node_fee Fixed64
			//var node_output_index uint64 = 999999
			var txType = tx.TxType
			for _, attr := range tx.Attributes {
				if attr.Usage == Memo {
					memo = attr.Data
				}
				//todo-remove 解析attribute.Description 数据好像没有实际意义
				//if attr.Usage == Description {
				//	am := make(map[string]interface{})
				//	err = json.Unmarshal(attr.Data, &am)
				//	if err == nil {
				//		pm, ok := am["Postmark"]
				//		if ok {
				//			dpm, ok := pm.(map[string]interface{})
				//			if ok {
				//				var orgMsg string
				//				for i, input := range tx.Inputs {
				//					hash := input.Previous.TxID
				//					orgMsg += BytesToHexString(BytesReverse(hash[:])) + "-" + strconv.Itoa(int(input.Previous.Index))
				//					if i != len(tx.Inputs)-1 {
				//						orgMsg += ";"
				//					}
				//				}
				//				orgMsg += "&"
				//				for i, output := range tx.Outputs {
				//					address, _ := output.ProgramHash.ToAddress()
				//					orgMsg += address + "-" + fmt.Sprintf("%d", output.Value)
				//					if i != len(tx.Outputs)-1 {
				//						orgMsg += ";"
				//					}
				//				}
				//				orgMsg += "&"
				//				orgMsg += fmt.Sprintf("%d", tx.Fee)
				//				log.Debugf("origin debug %s ", orgMsg)
				//				pub, ok_pub := dpm["pub"].(string)
				//				sig, ok_sig := dpm["signature"].(string)
				//				b_msg := []byte(orgMsg)
				//				b_pub, ok_b_pub := hex.DecodeString(pub)
				//				b_sig, ok_b_sig := hex.DecodeString(sig)
				//				if ok_pub && ok_sig && ok_b_pub == nil && ok_b_sig == nil {
				//					pubKey, err := crypto.DecodePoint(b_pub)
				//					if err != nil {
				//						log.Infof("Error deserialise pubkey from postmark data %s", hex.EncodeToString(attr.Data))
				//						continue
				//					}
				//					err = crypto.Verify(*pubKey, b_msg, b_sig)
				//					if err != nil {
				//						log.Infof("Error verify postmark data %s", hex.EncodeToString(attr.Data))
				//						continue
				//					}
				//					signedAddress, err = GetAddress(b_pub)
				//					if err != nil {
				//						log.Infof("Error Getting signed address from postmark %s", hex.EncodeToString(attr.Data))
				//						continue
				//					}
				//				} else {
				//					log.Infof("Invalid postmark data %s", hex.EncodeToString(attr.Data))
				//					continue
				//				}
				//			} else {
				//				log.Infof("Invalid postmark data %s", hex.EncodeToString(attr.Data))
				//				continue
				//			}
				//		}
				//	}
				//}
			}

			if txType == CoinBase {
				var to []Uint168
				hold := make(map[Uint168]uint64)
				txhsCoinBase := make([]TransactionHistory, 0)
				//for i, vout := range tx.Outputs {
				for _, vout := range tx.Outputs {
					if !ContainsU168(vout.ProgramHash, to) {
						to = append(to, vout.ProgramHash)
						txh := TransactionHistory{}
						txh.Address = vout.ProgramHash
						txh.Txid = tx.Hash()
						txh.Type = []byte(RECEIVED)
						txh.CreateTime = uint64(block.Header.Timestamp)
						txh.Height = uint64(block.Height)
						txh.Fee = 0
						txh.Inputs = []Uint168{MINING_ADDR}
						txh.TxType = txType
						txh.Memo = memo

						//txh.NodeFee = 0
						//txh.NodeOutputIndex = uint64(node_output_index)
						hold[vout.ProgramHash] = uint64(vout.Value)
						txhsCoinBase = append(txhsCoinBase, txh)
					} else {
						hold[vout.ProgramHash] += uint64(vout.Value)
					}
					//todo: remove, dpos reward
					//if i > 1 {
					//	dposReward[vout.ProgramHash] = vout.Value
					//}
				}
				for i := 0; i < len(txhsCoinBase); i++ {
					txhsCoinBase[i].Outputs = []Uint168{txhsCoinBase[i].Address}
					txhsCoinBase[i].Value = hold[txhsCoinBase[i].Address]
				}
				txhs = append(txhs, txhsCoinBase...)
			} else {
				//for _, program := range tx.Programs {
				//	code := program.Code
				//	programHash, err := GetProgramHash(code[1 : len(code)-1])
				//	if err != nil {
				//		continue
				//	}
				//	//pubks[*programHash] = code[1 : len(code)-1]
				//}

				isCrossTx := false
				if txType == TransferCrossChainAsset {
					isCrossTx = true
				}
				if voteTxHolder[txid] == DPoS || voteTxHolder[txid] == CRC || voteTxHolder[txid] == DPoSAndCRC {
					txType = voteTxHolder[txid]
				}
				spend := make(map[Uint168]int64)
				var totalInput int64 = 0
				var fromAddress []Uint168
				var toAddress []Uint168
				for _, input := range tx.Inputs {
					txid := input.Previous.TxID
					index := input.Previous.Index
					referTx, _, err := c.GetTransaction(txid)
					if err != nil {
						return err
					}
					address := referTx.Outputs[index].ProgramHash
					totalInput += int64(referTx.Outputs[index].Value)
					v, ok := spend[address]
					if ok {
						spend[address] = v + int64(referTx.Outputs[index].Value)
					} else {
						spend[address] = int64(referTx.Outputs[index].Value)
					}
					if !ContainsU168(address, fromAddress) {
						fromAddress = append(fromAddress, address)
					}
				}
				receive := make(map[Uint168]int64)
				var totalOutput int64 = 0
				for _, output := range tx.Outputs {
					address, _ := output.ProgramHash.ToAddress()
					var valueCross int64
					if isCrossTx == true && (output.ProgramHash == MINING_ADDR || strings.Index(address, "X") == 0 || address == "4oLvT2") {
						switch pl := tx.Payload.(type) {
						case *payload.TransferCrossChainAsset:
							valueCross = int64(pl.CrossChainAmounts[0])
						}
					}
					if valueCross != 0 {
						totalOutput += valueCross
					} else {
						totalOutput += int64(output.Value)
					}
					v, ok := receive[output.ProgramHash]
					if ok {
						receive[output.ProgramHash] = v + int64(output.Value)
					} else {
						receive[output.ProgramHash] = int64(output.Value)
					}
					if !ContainsU168(output.ProgramHash, toAddress) {
						toAddress = append(toAddress, output.ProgramHash)
					}
					//if signedAddress != "" {
					//	outputAddress, _ := output.ProgramHash.ToAddress()
					//	if signedAddress == outputAddress {
					//		//node_fee = output.Value
					//		node_output_index = uint64(i)
					//	}
					//}
				}
				fee := totalInput - totalOutput
				for addressReceiver, valueReceived := range receive {
					transferType := RECEIVED
					valueSpent, ok := spend[addressReceiver]
					var txValue int64
					if ok {
						if valueSpent > valueReceived {
							txValue = valueSpent - valueReceived
							transferType = SENT
						} else {
							txValue = valueReceived - valueSpent
						}
						delete(spend, addressReceiver)
					} else {
						txValue = valueReceived
					}
					var realFee = uint64(fee)
					var txOutput = toAddress
					if transferType == RECEIVED {
						realFee = 0
						txOutput = []Uint168{addressReceiver}
					}

					if transferType == SENT {
						fromAddress = []Uint168{addressReceiver}
					}

					txh := TransactionHistory{}
					txh.Value = uint64(txValue)
					txh.Address = addressReceiver
					txh.Inputs = fromAddress
					txh.TxType = txType
					txh.Txid = tx.Hash()
					txh.Height = uint64(block.Height)
					txh.CreateTime = uint64(block.Header.Timestamp)
					txh.Type = []byte(transferType)
					txh.Fee = realFee
					//txh.NodeFee = uint64(node_fee)
					//txh.NodeOutputIndex = uint64(node_output_index)
					if len(txOutput) > 10 {
						txh.Outputs = txOutput[0:10]
					} else {
						txh.Outputs = txOutput
					}
					txh.Memo = memo
					txhs = append(txhs, txh)
				}

				for k, r := range spend {
					txh := TransactionHistory{}
					txh.Value = uint64(r)
					txh.Address = k
					txh.Inputs = []Uint168{k}
					txh.TxType = txType
					txh.Txid = tx.Hash()
					txh.Height = uint64(block.Height)
					txh.CreateTime = uint64(block.Header.Timestamp)
					txh.Type = []byte(SENT)
					txh.Fee = uint64(fee)
					//txh.NodeFee = uint64(node_fee)
					//txh.NodeOutputIndex = uint64(node_output_index)
					if len(toAddress) > 10 {
						txh.Outputs = toAddress[0:10]
					} else {
						txh.Outputs = toAddress
					}
					txh.Memo = memo
					txhs = append(txhs, txh)
				}
			}
		}
		c.persistTransactionHistory(txhs)
		//c.persistPbks(pubks)
		//TODO-remove 不需要缓存DPoS收益情况
		//c.persistDposReward(dposReward, block.Height)
		c.persistStoredHeight(block.Height)
	}
	return nil
}

func (c *ChainStoreExtend) CloseEx() {
	closed := make(chan bool)
	c.quitEx <- closed
	<-closed
	c.Stop()
	log.Info("Extend chainStore shutting down")
}

func (c *ChainStoreExtend) loop() {
	for {
		select {
		case t := <-c.taskChEx:
			now := time.Now()
			switch kind := t.(type) {
			case *Block:
				err := c.persistTxHistory(kind)
				if err != nil {
					log.Errorf("Error persist transaction history %s", err.Error())
					os.Exit(-1)
					return
				}
				tcall := float64(time.Now().Sub(now)) / float64(time.Second)
				log.Debugf("handle SaveHistory time cost: %g num transactions:%d", tcall, len(kind.Transactions))
			}
		case closed := <-c.quitEx:
			closed <- true
			return
		}
	}
}

//func (c *ChainStoreExtend) GetTxHistory(addr string, order string, skip uint32, limit uint32) interface{} {
func (c *ChainStoreExtend) GetTxHistory(addr string, order string) interface{} {
	key := new(bytes.Buffer)
	key.WriteByte(byte(DataTxHistoryPrefix))
	//todo:remove this line  txhs用于存储从数据库中检索到的结果，其数据结构为RPC接口返回的内容，目前为[]TransactionHistoryDisplay
	var txhs interface{}
	if order == "desc" {
		txhs = make(TransactionHistorySorterDesc, 0)
	} else {
		txhs = make(TransactionHistorySorter, 0)
	}
	programHash, err := Uint168FromAddress(addr)
	if err != nil {
		return txhs
	}
	WriteVarBytes(key, programHash[:])
	iter := c.NewIterator(key.Bytes())
	defer iter.Release()

	//todo:remove this line  通过key从数据库中检索所有与地址相关的交易记录
	//todo: 单条记录类型为TransactionHistory，因TransactionHistory与TransactionHistoryDisplay数据结构相同，所以按照部分逻辑对数据进行清洗后，可以直接存储
	for iter.Next() {
		val := new(bytes.Buffer)
		val.Write(iter.Value())
		txh := TransactionHistory{}
		txhd, _ := txh.Deserialize(val)
		if txhd.Type == "received" {
			if len(txhd.Inputs) > 10 {
				txhd.Inputs = txhd.Inputs[0:10]
			}
			txhd.Outputs = []string{txhd.Address}
		} else {
			txhd.Inputs = []string{txhd.Address}
			if len(txhd.Outputs) > 10 {
				txhd.Outputs = txhd.Outputs[0:10]
			}
		}

		//TODO: remove the lines below, 不对手续费数值进行更改
		//if txhd.Type == "sent" {
		//	txhd.Fee = txhd.Fee + uint64(*txhd.NodeFee)
		//}
		txhd.TxType = strings.ToLower(txhd.TxType[0:1]) + txhd.TxType[1:]

		if order == "desc" {
			txhs = append(txhs.(TransactionHistorySorterDesc), *txhd)
		} else {
			txhs = append(txhs.(TransactionHistorySorter), *txhd)
		}
	}

	txInMempool := MemPoolEx.GetMemPoolTx(programHash)
	for _, txh := range txInMempool {
		txh.TxType = strings.ToLower(txh.TxType[0:1]) + txh.TxType[1:]
		//txh.Fee = txh.Fee + uint64(*txh.NodeFee)

		if order == "desc" {
			txhs = append(txhs.(TransactionHistorySorterDesc), txh)
		} else {
			txhs = append(txhs.(TransactionHistorySorter), txh)
		}
	}

	if order == "desc" {
		sort.Sort(txhs.(TransactionHistorySorterDesc))
	} else {
		sort.Sort(txhs.(TransactionHistorySorter))
	}
	return txhs
}

//func (c *ChainStoreExtend) GetTxHistoryByPage(addr, order, vers string, pageNum, pageSize uint32) (interface{}, int) {
func (c *ChainStoreExtend) GetTxHistoryByLimit(addr, order string, skip, limit uint32) (interface{}, int) {
	txhs := c.GetTxHistory(addr, order)
	if order == "desc" {
		return txhs.(TransactionHistorySorterDesc).Filter(skip, limit), len(txhs.(TransactionHistorySorterDesc))
	} else {
		return txhs.(TransactionHistorySorter).Filter(skip, limit), len(txhs.(TransactionHistorySorter))
	}
}

//读取ex数据库中的最新高度
func (c *ChainStoreExtend) GetBestHeightExt() (uint32, error) {
	key := new(bytes.Buffer)
	key.WriteByte(byte(DataBestHeightPrefix))
	data, err := c.Get(key.Bytes())
	if err != nil {
		return 0, err
	}
	buf := bytes.NewBuffer(data)
	return binary.LittleEndian.Uint32(buf.Bytes()), nil
}

func (c *ChainStoreExtend) GetStoredHeightExt(height uint32) (bool, error) {
	key := new(bytes.Buffer)
	key.WriteByte(byte(DataStoredHeightPrefix))
	WriteUint32(key, height)
	_, err := c.Get(key.Bytes())
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *ChainStoreExtend) LockDposData() {
	c.mu.RLock()
}

func (c *ChainStoreExtend) UnlockDposData() {
	c.mu.RUnlock()
}
