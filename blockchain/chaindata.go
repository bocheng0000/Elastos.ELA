package blockchain

import (
	"bytes"
	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/common/log"
	"github.com/elastos/Elastos.ELA/core/types"
	//"github.com/elastos/Elastos.ELA.Elephant.Node/core/types"
)

func (c ChainStoreExtend) begin() {
	c.NewBatch()
}

func (c ChainStoreExtend) commit() {
	c.BatchCommit()
}

func (c ChainStoreExtend) rollback() {

}

// key: DataEntryPrefix + height + address
// value: serialized history
func (c ChainStoreExtend) persistTransactionHistory(txhs []types.TransactionHistory) error {
	c.begin()
	for i, txh := range txhs {
		err := c.doPersistTransactionHistory(uint64(i), txh)
		if err != nil {
			c.rollback()
			log.Fatal("Error persist transaction history")
			return err
		}
	}
	for _, txh := range txhs {
		c.deleteMemPoolTx(txh.Txid)
	}
	c.commit()
	return nil
}

func (c ChainStoreExtend) deleteMemPoolTx(txid common.Uint256) {
	MemPoolEx.DeleteMemPoolTx(txid)
}

//todo-remove 不需要对地址的公钥进行缓存
// key: DataEntryPrefix + height + address
// value: serialized history
//func (c ChainStoreExtend) persistPbks(pbks map[common.Uint168][]byte) error {
//	c.begin()
//	for k, v := range pbks {
//		err := c.doPersistPbks(k, v)
//		if err != nil {
//			c.rollback()
//			log.Fatal("Error persist public keys")
//			return err
//		}
//	}
//	c.commit()
//	return nil
//}

//TODO-remove: 将DPoS收益情况写入数据库；如不需要可以删除
//func (c ChainStoreExtend) persistDposReward(rewardDpos map[common.Uint168]common.Fixed64, height uint32) error {
//	c.begin()
//	for programHash, value := range rewardDpos {
//		err := c.doPersistDposReward(programHash, value, height)
//		if err != nil {
//			c.rollback()
//			log.Fatal("Error persist dpos reward")
//			return err
//		}
//	}
//	c.commit()
//	return nil
//}

//在ex数据库中存储最新的高度
func (c ChainStoreExtend) persistBestHeight(height uint32) error {
	bestHeight, err := c.GetBestHeightExt()
	if (err == nil && bestHeight < height) || err != nil {
		c.begin()
		err = c.doPersistBestHeight(height)
		if err != nil {
			c.rollback()
			log.Fatal("Error persist best height")
			return err
		}
		c.commit()
	}
	return nil
}

//在ex数据库中存储最新的高度
func (c ChainStoreExtend) doPersistBestHeight(height uint32) error {
	key := new(bytes.Buffer)
	key.WriteByte(byte(DataBestHeightPrefix))
	value := new(bytes.Buffer)
	common.WriteUint32(value, height)
	c.BatchPut(key.Bytes(), value.Bytes())
	return nil
}

func (c ChainStoreExtend) persistStoredHeight(height uint32) error {
	c.begin()
	err := c.doPersistStoredHeight(height)
	if err != nil {
		c.rollback()
		log.Fatal("Error persist best height")
		return err
	}
	c.commit()
	return nil
}

func (c ChainStoreExtend) doPersistStoredHeight(height uint32) error {
	key := new(bytes.Buffer)
	key.WriteByte(byte(DataStoredHeightPrefix))
	common.WriteUint32(key, height)
	value := new(bytes.Buffer)
	common.WriteVarBytes(value, []byte{1})
	c.BatchPut(key.Bytes(), value.Bytes())
	return nil
}

//todo-remove, 不需要缓存dpos收益
// key: DataDposRewardPrefix + programHash + height
// value: rewardValue
//func (c ChainStoreExtend) doPersistDposReward(programHash common.Uint168, rewardValue common.Fixed64, height uint32) error {
//	key := new(bytes.Buffer)
//	key.WriteByte(byte(DataDposRewardPrefix))
//	err := programHash.Serialize(key)
//	if err != nil {
//		return err
//	}
//	common.WriteUint32(key, height)
//
//	value := new(bytes.Buffer)
//	rewardValue.Serialize(value)
//	c.BatchPut(key.Bytes(), value.Bytes())
//	return nil
//}

//todo-remove 不需要对地址的公钥进行缓存
//func (c ChainStoreExtend) doPersistPbks(k common.Uint168, pub []byte) error {
//	key := new(bytes.Buffer)
//	key.WriteByte(byte(DataPkPrefix))
//	err := k.Serialize(key)
//	if err != nil {
//		return err
//	}
//	value := new(bytes.Buffer)
//	common.WriteVarBytes(value, pub)
//	c.BatchPut(key.Bytes(), value.Bytes())
//	return nil
//}

func (c ChainStoreExtend) doPersistTransactionHistory(i uint64, history types.TransactionHistory) error {
	key := new(bytes.Buffer)
	key.WriteByte(byte(DataTxHistoryPrefix))
	err := common.WriteVarBytes(key, history.Address[:])
	if err != nil {
		return err
	}
	err = common.WriteUint64(key, history.Height)
	if err != nil {
		return err
	}
	err = common.WriteUint64(key, i)
	if err != nil {
		return err
	}

	value := new(bytes.Buffer)
	history.Serialize(value)
	c.BatchPut(key.Bytes(), value.Bytes())
	return nil
}

func (c ChainStoreExtend) initTask() {
	c.AddFunc("@every 2m", func() {
		if len(c.rp) == 0 {
			c.rp <- true
		}
	})
	c.Start()
}

//todo-remove, 更新dpos节点在数据库中的信息
//func (c *ChainStoreExtend) renewProducer() {
//	//if DefaultChainStoreEx.GetHeight() >= 290000 {
//	if StoreEx.GetHeight() >= 290000 {
//		var err error
//		db, err := DBA.Begin()
//		defer func() {
//			if err != nil {
//				log.Errorf("Error renew producer %s", err.Error())
//				db.Rollback()
//			} else {
//				db.Commit()
//			}
//		}()
//		if err != nil {
//			return
//		}
//		stmt1, err := db.Prepare("delete from chain_producer_info")
//		if err != nil {
//			return
//		}
//		_, err = stmt1.Exec()
//		if err != nil {
//			return
//		}
//		stmt1.Close()
//
//		stmt, err := db.Prepare("insert into chain_producer_info (Ownerpublickey,Nodepublickey,Nickname,Url,Location,Active,Votes,Netaddress,State,Registerheight,Cancelheight,Inactiveheight,Illegalheight,`Index`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
//		if err != nil {
//			return
//		}
//		producers := c.chain.GetState().GetAllProducers()
//		for i, producer := range producers {
//			var active int
//			if producer.State() == state.Active {
//				active = 1
//			} else {
//				active = 0
//			}
//			_, err = stmt.Exec(common.BytesToHexString(producer.OwnerPublicKey()), common.BytesToHexString(producer.NodePublicKey()),
//				producer.Info().NickName, producer.Info().Url, producer.Info().Location, active, producer.Votes().String(),
//				producer.Info().NetAddress, producer.State().String(), producer.RegisterHeight(), producer.CancelHeight(),
//				producer.InactiveSince(), producer.IllegalHeight(), i)
//			if err != nil {
//				return
//			}
//		}
//		stmt.Close()
//	}
//}

//todo-remove 更新CR候选人在db中的信息
//func (c *ChainStoreExtend) renewCrCandidates() {
//	var cRVotingStartHeight = 537670
//	if "testnet" == strings.ToLower(config.Parameters.ActiveNet) {
//		cRVotingStartHeight = 436900
//	} else if "regnet" == strings.ToLower(config.Parameters.ActiveNet) {
//		cRVotingStartHeight = 292000
//	}
//	if StoreEx.GetHeight() >= uint32(cRVotingStartHeight) {
//		var err error
//		db, err := DBA.Begin()
//		defer func() {
//			if err != nil {
//				log.Errorf("Error renew renewCrCandidates %s", err.Error())
//				db.Rollback()
//			} else {
//				db.Commit()
//			}
//		}()
//		if err != nil {
//			return
//		}
//		cands := c.chain.GetCRCommittee().GetAllCandidates()
//		if len(cands) == 0 {
//			return
//		}
//		stmt1, err := db.Prepare("delete from chain_cr_candidate_info")
//		if err != nil {
//			return
//		}
//		_, err = stmt1.Exec()
//		if err != nil {
//			return
//		}
//		stmt1.Close()
//
//		stmt, err := db.Prepare("insert into chain_cr_candidate_info (code, did ,nickname ,url ,location ,state ,votes ,`index` ) values(?,?,?,?,?,?,?,?)")
//		if err != nil {
//			return
//		}
//		for i, can := range cands {
//			did, _ := can.Info().CID.ToAddress()
//			_, err = stmt.Exec(hex.EncodeToString(can.Info().Code), did, can.Info().NickName, can.Info().Url, can.Info().Location, can.State().String(), can.Votes().String(), i)
//			if err != nil {
//				log.Errorf("%s \n", err.Error())
//				return
//			}
//		}
//		stmt.Close()
//	}
//}
