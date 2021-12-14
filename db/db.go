package db

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"

	"github.com/lbryio/hub/db/prefixes"
	"github.com/lbryio/hub/db/rocksdbwrap"
	"github.com/linxGnu/grocksdb"
)

type IterOptions struct {
	FillCache    bool
	Start        []byte //interface{}
	Stop         []byte //interface{}
	IncludeStart bool
	IncludeStop  bool
	IncludeKey   bool
	IncludeValue bool
}

type PrefixRow struct {
	//KeyStruct     interface{}
	//ValueStruct   interface{}
	Prefix          []byte
	KeyPackFunc     interface{}
	ValuePackFunc   interface{}
	KeyUnpackFunc   interface{}
	ValueUnpackFunc interface{}
	DB              *rocksdbwrap.RocksDB
	// DB              *grocksdb.DB
}

type PrefixRowKV struct {
	Key   []byte
	Value []byte
}

type PrefixRowKV2 struct {
	Key   interface{}
	Value interface{}
}

type UTXOKey struct {
	Prefix []byte
	HashX  []byte
	TxNum  uint32
	Nout   uint16
}

type UTXOValue struct {
	Amount uint64
}

// NewIterateOptions creates a defualt options structure for a db iterator.
// Default values:
// FillCache:    false,
// Start:        nil,
// Stop:         nil,
// IncludeStart: true,
// IncludeStop:  false,
// IncludeKey:   true,
// IncludeValue: false,
func NewIterateOptions() *IterOptions {
	return &IterOptions{
		FillCache:    false,
		Start:        nil,
		Stop:         nil,
		IncludeStart: true,
		IncludeStop:  false,
		IncludeKey:   true,
		IncludeValue: false,
	}
}

func (o *IterOptions) WithFillCache(fillCache bool) *IterOptions {
	o.FillCache = fillCache
	return o
}

func (o *IterOptions) WithStart(start []byte) *IterOptions {
	o.Start = start
	return o
}

func (o *IterOptions) WithStop(stop []byte) *IterOptions {
	o.Stop = stop
	return o
}

func (o *IterOptions) WithIncludeStart(includeStart bool) *IterOptions {
	o.IncludeStart = includeStart
	return o
}

func (o *IterOptions) WithIncludeStop(includeStop bool) *IterOptions {
	o.IncludeStop = includeStop
	return o
}

func (o *IterOptions) WithIncludeKey(includeKey bool) *IterOptions {
	o.IncludeKey = includeKey
	return o
}

func (o *IterOptions) WithIncludeValue(includeValue bool) *IterOptions {
	o.IncludeValue = includeValue
	return o
}

func (k *UTXOKey) String() string {
	return fmt.Sprintf(
		"%s(hashX=%s, tx_num=%d, nout=%d)",
		reflect.TypeOf(k),
		hex.EncodeToString(k.HashX),
		k.TxNum,
		k.Nout,
	)
}

func (pr *PrefixRow) Iter2(options *IterOptions) <-chan *PrefixRowKV2 {
	ch := make(chan *PrefixRowKV2)

	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(options.FillCache)
	it := pr.DB.NewIterator(ro)

	it.Seek(pr.Prefix)
	if options.Start != nil {
		log.Println("Seeking to start")
		it.Seek(options.Start)
	} else {
		log.Println("Not seeking to start")
	}

	stopIteration := func(key []byte) bool {
		if key == nil {
			return false
		}

		if options.Stop != nil &&
			(bytes.HasPrefix(key, options.Stop) || bytes.Compare(options.Stop, key[:len(options.Stop)]) < 0) {
			return true
		} else if options.Start != nil &&
			bytes.Compare(options.Start, key[:len(options.Start)]) > 0 {
			return true
		} else if pr.Prefix != nil && !bytes.HasPrefix(key, pr.Prefix) {
			return true
		}

		return false
	}

	go func() {
		defer it.Close()
		defer close(ch)

		if !options.IncludeStart {
			it.Next()
		}
		var prevKey []byte = nil
		for ; !stopIteration(prevKey); it.Next() {
			key := it.Key()
			keyData := key.Data()
			keyLen := len(keyData)
			value := it.Value()
			valueData := value.Data()
			valueLen := len(valueData)

			var unpackedKey interface{} = nil
			var unpackedValue interface{} = nil

			// We need to check the current key is we're not including the stop
			// key.
			if !options.IncludeStop && stopIteration(keyData) {
				return
			}

			// We have to copy the key no matter what because we need to check
			// it on the next iterations to see if we're going to stop.
			newKeyData := make([]byte, keyLen)
			copy(newKeyData, keyData)
			if options.IncludeKey {
				unpackKeyFnValue := reflect.ValueOf(pr.KeyUnpackFunc)
				keyArgs := []reflect.Value{reflect.ValueOf(newKeyData)}
				unpackKeyFnResult := unpackKeyFnValue.Call(keyArgs)
				unpackedKey = unpackKeyFnResult[0].Interface() //.(*UTXOKey)
			}

			// Value could be quite large, so this setting could be important
			// for performance in some cases.
			if options.IncludeValue {
				newValueData := make([]byte, valueLen)
				copy(newValueData, valueData)
				unpackValueFnValue := reflect.ValueOf(pr.ValueUnpackFunc)
				valueArgs := []reflect.Value{reflect.ValueOf(newValueData)}
				unpackValueFnResult := unpackValueFnValue.Call(valueArgs)
				unpackedValue = unpackValueFnResult[0].Interface() //.(*UTXOValue)
			}

			key.Free()
			value.Free()

			ch <- &PrefixRowKV2{
				Key:   unpackedKey,
				Value: unpackedValue,
			}
			prevKey = newKeyData

		}
	}()

	return ch
}

func (pr *PrefixRow) Iter(options *IterOptions) <-chan *PrefixRowKV {
	ch := make(chan *PrefixRowKV)

	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(options.FillCache)
	it := pr.DB.NewIterator(ro)

	it.Seek(pr.Prefix)
	if options.Start != nil {
		log.Println("Seeking to start")
		it.Seek(options.Start)
	} else {
		log.Println("Not seeking to start")
	}

	stopIteration := func(key []byte) bool {
		if key == nil {
			return false
		}

		if options.Stop != nil &&
			(bytes.HasPrefix(key, options.Stop) || bytes.Compare(options.Stop, key[:len(options.Stop)]) < 0) {
			return true
		} else if options.Start != nil &&
			bytes.Compare(options.Start, key[:len(options.Start)]) > 0 {
			return true
		} else if pr.Prefix != nil && !bytes.HasPrefix(key, pr.Prefix) {
			return true
		}

		return false
	}

	go func() {
		defer it.Close()
		defer close(ch)

		if !options.IncludeStart {
			it.Next()
		}
		var prevKey []byte = nil
		for ; !stopIteration(prevKey); it.Next() {
			key := it.Key()
			keyData := key.Data()
			keyLen := len(keyData)
			value := it.Value()
			valueData := value.Data()
			valueLen := len(valueData)

			// We need to check the current key is we're not including the stop
			// key.
			if !options.IncludeStop && stopIteration(keyData) {
				return
			}

			var outputKeyData []byte = nil
			// We have to copy the key no matter what because we need to check
			// it on the next iterations to see if we're going to stop.
			newKeyData := make([]byte, keyLen)
			copy(newKeyData, keyData)
			if options.IncludeKey {
				outputKeyData = newKeyData
			}

			var newValueData []byte = nil
			// Value could be quite large, so this setting could be important
			// for performance in some cases.
			if options.IncludeValue {
				newValueData = make([]byte, valueLen)
				copy(newValueData, valueData)
			}

			key.Free()
			value.Free()

			ch <- &PrefixRowKV{
				Key:   outputKeyData,
				Value: newValueData,
			}
			prevKey = newKeyData

		}
	}()

	return ch
}

func (k *UTXOKey) PackKey() []byte {
	prefixLen := len(prefixes.UTXO)
	// b'>11sLH'
	n := prefixLen + 11 + 4 + 2
	key := make([]byte, n)
	copy(key, k.Prefix)
	copy(key[prefixLen:], k.HashX)
	binary.BigEndian.PutUint32(key[prefixLen+11:], k.TxNum)
	binary.BigEndian.PutUint16(key[prefixLen+15:], k.Nout)

	return key
}

// UTXOKeyPackPartialNFields creates a pack partial key function for n fields.
func UTXOKeyPackPartialNFields(nFields int) func(*UTXOKey) []byte {
	return func(u *UTXOKey) []byte {
		return UTXOKeyPackPartial(u, nFields)
	}
}

// UTXOKeyPackPartial packs a variable number of fields for a UTXOKey into
// a byte array.
func UTXOKeyPackPartial(k *UTXOKey, nFields int) []byte {
	// Limit nFields between 0 and number of fields, we always at least need
	// the prefix and we never need to iterate past the number of fields.
	if nFields > 3 {
		nFields = 3
	}
	if nFields < 0 {
		nFields = 0
	}

	// b'>11sLH'
	prefixLen := len(prefixes.UTXO)
	var n = prefixLen
	for i := 0; i <= nFields; i++ {
		switch i {
		case 1:
			n += 11
		case 2:
			n += 4
		case 3:
			n += 2
		}
	}

	key := make([]byte, n)

	for i := 0; i <= nFields; i++ {
		switch i {
		case 0:
			copy(key, k.Prefix)
		case 1:
			copy(key[prefixLen:], k.HashX)
		case 2:
			binary.BigEndian.PutUint32(key[prefixLen+11:], k.TxNum)
		case 3:
			binary.BigEndian.PutUint16(key[prefixLen+15:], k.Nout)
		}
	}

	return key
}

func UTXOKeyUnpack(key []byte) *UTXOKey {
	return &UTXOKey{
		Prefix: key[:1],
		HashX:  key[1:12],
		TxNum:  binary.BigEndian.Uint32(key[12:]),
		Nout:   binary.BigEndian.Uint16(key[16:]),
	}
}

func (k *UTXOValue) PackValue() []byte {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, k.Amount)

	return value
}

func UTXOValueUnpack(value []byte) *UTXOValue {
	return &UTXOValue{
		Amount: binary.BigEndian.Uint64(value),
	}
}

func GetDB(name string) (*grocksdb.DB, error) {
	opts := grocksdb.NewDefaultOptions()
	// db, err := grocksdb.OpenDb(opts, name)
	db, err := grocksdb.OpenDbAsSecondary(opts, name, "asdf")
	if err != nil {
		return nil, err
	}

	return db, nil
}

func ReadPrefixN(db *grocksdb.DB, prefix []byte, n int) []*PrefixRowKV {
	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(false)

	it := db.NewIterator(ro)
	defer it.Close()

	res := make([]*PrefixRowKV, n)

	var i = 0
	it.Seek(prefix)
	for ; it.Valid(); it.Next() {
		key := it.Key()
		value := it.Value()

		res[i] = &PrefixRowKV{
			Key:   key.Data(),
			Value: value.Data(),
		}

		key.Free()
		value.Free()
		i++
		if i >= n {
			break
		}
	}

	return res
}

func OpenDB(name string, start string) int {
	// Read db
	opts := grocksdb.NewDefaultOptions()
	db, err := grocksdb.OpenDb(opts, name)
	if err != nil {
		log.Println(err)
	}
	defer db.Close()
	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(false)

	log.Println(db.Name())

	it := db.NewIterator(ro)
	defer it.Close()

	var i = 0
	it.Seek([]byte(start))
	for ; it.Valid(); it.Next() {
		key := it.Key()
		value := it.Value()

		fmt.Printf("Key: %v Value: %v\n", hex.EncodeToString(key.Data()), hex.EncodeToString(value.Data()))

		key.Free()
		value.Free()
		i++
	}
	if err := it.Err(); err != nil {
		log.Println(err)
	}

	return i
}

func OpenAndWriteDB(prIn *PrefixRow, options *IterOptions, out string) {
	// Write db
	opts := grocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(true)
	db, err := grocksdb.OpenDb(opts, out)
	if err != nil {
		log.Println(err)
	}
	wo := grocksdb.NewDefaultWriteOptions()
	defer db.Close()

	ch := prIn.Iter(options)

	var i = 0
	for kv := range ch {
		key := kv.Key
		value := kv.Value
		var unpackedKey *UTXOKey = nil
		var unpackedValue *UTXOValue = nil

		if key != nil {
			unpackKeyFnValue := reflect.ValueOf(prIn.KeyUnpackFunc)
			keyArgs := []reflect.Value{reflect.ValueOf(key)}
			unpackKeyFnResult := unpackKeyFnValue.Call(keyArgs)
			unpackedKey = unpackKeyFnResult[0].Interface().(*UTXOKey)
		}

		if value != nil {
			unpackValueFnValue := reflect.ValueOf(prIn.ValueUnpackFunc)
			valueArgs := []reflect.Value{reflect.ValueOf(value)}
			unpackValueFnResult := unpackValueFnValue.Call(valueArgs)
			unpackedValue = unpackValueFnResult[0].Interface().(*UTXOValue)
		}

		log.Println(unpackedKey, unpackedValue)

		if err := db.Put(wo, key, value); err != nil {
			log.Println(err)
		}
		i++
	}
}
