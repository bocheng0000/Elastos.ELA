package wallet

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"sync"

	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/common/log"
	"github.com/elastos/Elastos.ELA/utils"
)

type AddressData struct {
	Address string
	Code    string
}

type FileData struct {
	Version   string
	Height    int32
	Addresses []AddressData
}

type FileStore struct {
	// this lock could be hold by readDB, writeDB and interrupt signals.
	sync.Mutex

	data FileData
	file *os.File
	path string
}

// Caller holds the lock and reads bytes from DB, then close the DB and release the lock
func (cs *FileStore) readDB() ([]byte, error) {
	cs.Lock()
	defer cs.Unlock()
	defer cs.closeDB()

	var err error
	cs.file, err = os.OpenFile(cs.path, os.O_RDONLY, 0400)
	if err != nil {
		return nil, err
	}

	if cs.file != nil {
		data, err := ioutil.ReadAll(cs.file)
		if err != nil {
			return nil, err
		}
		return data, nil
	} else {
		return nil, errors.New("[readDB] file handle is nil")
	}
}

// Caller holds the lock and writes bytes to DB, then close the DB and release the lock
func (cs *FileStore) writeDB(data []byte) error {
	cs.Lock()
	defer cs.Unlock()
	defer cs.closeDB()

	var err error
	cs.file, err = os.OpenFile(cs.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	if cs.file != nil {
		cs.file.Write(data)
	}

	return nil
}

func (cs *FileStore) closeDB() {
	if cs.file != nil {
		cs.file.Close()
		cs.file = nil
	}
}

func (cs *FileStore) BuildDatabase(path string) error {
	if utils.FileExisted(path) {
		log.Warn(path + " file already exist")
	}
	jsonBlob, err := json.Marshal(cs.data)
	if err != nil {
		log.Warn("Build DataBase Error")
	}
	return cs.writeDB(jsonBlob)
}

func (cs *FileStore) SaveAddressData(address string, code []byte) error {
	JSONData, err := cs.readDB()
	if err != nil {
		return errors.New("error: reading db")
	}
	if err := json.Unmarshal(JSONData, &cs.data); err != nil {
		return errors.New("error: unmarshal db")
	}

	a := AddressData{
		Address: address,
		Code:    common.BytesToHexString(code),
	}

	for _, v := range cs.data.Addresses {
		if a.Address == v.Address {
			return errors.New("address already exists")
		}
	}
	cs.data.Addresses = append(cs.data.Addresses, a)

	JSONBlob, err := json.Marshal(cs.data)
	if err != nil {
		return errors.New("error: marshal db")
	}
	return cs.writeDB(JSONBlob)
}

func (cs *FileStore) DeleteAddressData(address string) error {
	JSONData, err := cs.readDB()
	if err != nil {
		return errors.New("error: reading db")
	}
	if err := json.Unmarshal(JSONData, &cs.data); err != nil {
		return errors.New("error: unmarshal db")
	}

	for i, v := range cs.data.Addresses {
		if address == v.Address {
			cs.data.Addresses = append(cs.data.Addresses[:i], cs.data.Addresses[i+1:]...)
		}
	}

	JSONBlob, err := json.Marshal(cs.data)
	if err != nil {
		return errors.New("error: marshal db")
	}
	return cs.writeDB(JSONBlob)
}

func (cs *FileStore) LoadAddressData() ([]AddressData, error) {
	JSONData, err := cs.readDB()
	if err != nil {
		return nil, errors.New("error: reading db")
	}
	if err := json.Unmarshal(JSONData, &cs.data); err != nil {
		return nil, errors.New("error: unmarshal db")
	}
	return cs.data.Addresses, nil
}

func (cs *FileStore) SaveStoredData(name string, value []byte) error {
	JSONData, err := cs.readDB()
	if err != nil {
		return errors.New("error: reading db")
	}
	if err := json.Unmarshal(JSONData, &cs.data); err != nil {
		return errors.New("error: unmarshal db")
	}

	switch name {
	case "Version":
		cs.data.Version = string(value)
	case "Height":
		var height int32
		bytesBuffer := bytes.NewBuffer(value)
		binary.Read(bytesBuffer, binary.LittleEndian, &height)
		cs.data.Height = height

	}
	JSONBlob, err := json.Marshal(cs.data)
	if err != nil {
		return errors.New("error: marshal db")
	}
	return cs.writeDB(JSONBlob)
}

func (cs *FileStore) LoadStoredData(name string) ([]byte, error) {
	JSONData, err := cs.readDB()
	if err != nil {
		return nil, errors.New("error: reading db")
	}
	if err := json.Unmarshal(JSONData, &cs.data); err != nil {
		return nil, errors.New("error: unmarshal db")
	}
	switch name {
	case "Version":
		return []byte(cs.data.Version), nil
	case "Height":
		bytesBuffer := new(bytes.Buffer)
		binary.Write(bytesBuffer, binary.LittleEndian, cs.data.Height)
		return bytesBuffer.Bytes(), nil
	}

	return nil, errors.New("can't find the key: " + name)
}