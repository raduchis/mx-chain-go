package rocksdb

import (
	"github.com/multiversx/mx-chain-go/storage"
	"github.com/tecbot/gorocksdb"
)

// DB implements the storage.Persister interface using RocksDB as backend
// It is a thin wrapper over gorocksdb.DB

type DB struct {
	db   *gorocksdb.DB
	opts *gorocksdb.Options
	ro   *gorocksdb.ReadOptions
	wo   *gorocksdb.WriteOptions
	path string
}

// NewDB creates a new RocksDB instance at the given path
// batchDelaySeconds, maxBatchSize and maxOpenFiles must be greater than zero
// These parameters are kept for compatibility with other persisters.
func NewDB(path string, batchDelaySeconds, maxBatchSize, maxOpenFiles int) (*DB, error) {
	if len(path) == 0 || batchDelaySeconds <= 0 || maxBatchSize <= 0 || maxOpenFiles <= 0 {
		return nil, storage.ErrInvalidConfig
	}

	opts := gorocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(true)
	opts.SetMaxOpenFiles(maxOpenFiles)

	db, err := gorocksdb.OpenDb(opts, path)
	if err != nil {
		return nil, err
	}

	return &DB{
		db:   db,
		opts: opts,
		ro:   gorocksdb.NewDefaultReadOptions(),
		wo:   gorocksdb.NewDefaultWriteOptions(),
		path: path,
	}, nil
}

// Put adds the value to the (key, val) storage medium
func (d *DB) Put(key, val []byte) error {
	return d.db.Put(d.wo, key, val)
}

// Get gets the value associated to the key, or reports an error
func (d *DB) Get(key []byte) ([]byte, error) {
	slice, err := d.db.Get(d.ro, key)
	if err != nil {
		return nil, err
	}
	if slice == nil {
		return nil, storage.ErrKeyNotFound
	}
	defer slice.Free()
	if slice.Size() == 0 {
		return nil, storage.ErrKeyNotFound
	}
	data := append([]byte(nil), slice.Data()...)
	return data, nil
}

// Has returns nil if the given key is present in the persistence medium
func (d *DB) Has(key []byte) error {
	slice, err := d.db.Get(d.ro, key)
	if err != nil {
		return err
	}
	if slice == nil {
		return storage.ErrKeyNotFound
	}
	defer slice.Free()
	if slice.Size() == 0 {
		return storage.ErrKeyNotFound
	}
	return nil
}

// Close closes the database
func (d *DB) Close() error {
	if d.db != nil {
		d.db.Close()
	}
	if d.opts != nil {
		d.opts.Destroy()
	}
	if d.ro != nil {
		d.ro.Destroy()
	}
	if d.wo != nil {
		d.wo.Destroy()
	}
	return nil
}

// Remove removes the data associated to the given key
func (d *DB) Remove(key []byte) error {
	return d.db.Delete(d.wo, key)
}

// Destroy removes the storage medium stored data
func (d *DB) Destroy() error {
	if err := d.Close(); err != nil {
		return err
	}
	return gorocksdb.DestroyDb(d.path, d.opts)
}

// DestroyClosed removes the already closed storage medium stored data
func (d *DB) DestroyClosed() error {
	return gorocksdb.DestroyDb(d.path, gorocksdb.NewDefaultOptions())
}

// RangeKeys will iterate over all contained (key, value) pairs calling the handler for each pair
func (d *DB) RangeKeys(handler func(key []byte, value []byte) bool) {
	if handler == nil {
		return
	}

	it := d.db.NewIterator(d.ro)
	defer it.Close()

	for it.SeekToFirst(); it.Valid(); it.Next() {
		k := it.Key()
		v := it.Value()
		cont := handler(append([]byte(nil), k.Data()...), append([]byte(nil), v.Data()...))
		k.Free()
		v.Free()
		if !cont {
			break
		}
	}
}

// IsInterfaceNil returns true if there is no value under the interface
func (d *DB) IsInterfaceNil() bool {
	return d == nil
}

var _ storage.Persister = (*DB)(nil)
