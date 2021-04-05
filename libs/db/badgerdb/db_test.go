package badgerdb

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tmdb "github.com/lazyledger/lazyledger-core/libs/db"
	"github.com/lazyledger/lazyledger-core/libs/db/internal/dbtest"
)

func TestBackendGetSetDelete(t *testing.T) {
	// Default
	dirname, err := ioutil.TempDir("", fmt.Sprintf("test_backend_%s_", "badger"))
	require.Nil(t, err)
	db, err := NewDB("badger", dirname)
	require.NoError(t, err)
	defer dbtest.CleanupDBDir(dirname, "testdb")

	// A nonexistent key should return nil.
	value, err := db.Get([]byte("a"))
	require.NoError(t, err)
	require.Nil(t, value)

	ok, err := db.Has([]byte("a"))
	require.NoError(t, err)
	require.False(t, ok)

	// Set and get a value.
	err = db.Set([]byte("a"), []byte{0x01})
	require.NoError(t, err)

	ok, err = db.Has([]byte("a"))
	require.NoError(t, err)
	require.True(t, ok)

	value, err = db.Get([]byte("a"))
	require.NoError(t, err)
	require.Equal(t, []byte{0x01}, value)

	err = db.SetSync([]byte("b"), []byte{0x02})
	require.NoError(t, err)

	value, err = db.Get([]byte("b"))
	require.NoError(t, err)
	require.Equal(t, []byte{0x02}, value)

	// Deleting a non-existent value is fine.
	err = db.Delete([]byte("x"))
	require.NoError(t, err)

	err = db.DeleteSync([]byte("x"))
	require.NoError(t, err)

	// Delete a value.
	err = db.Delete([]byte("a"))
	require.NoError(t, err)

	value, err = db.Get([]byte("a"))
	require.NoError(t, err)
	require.Nil(t, value)

	err = db.DeleteSync([]byte("b"))
	require.NoError(t, err)

	value, err = db.Get([]byte("b"))
	require.NoError(t, err)
	require.Nil(t, value)

	// Setting, getting, and deleting an empty key should error.
	_, err = db.Get([]byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	_, err = db.Get(nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)

	_, err = db.Has([]byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	_, err = db.Has(nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)

	err = db.Set([]byte{}, []byte{0x01})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = db.Set(nil, []byte{0x01})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = db.SetSync([]byte{}, []byte{0x01})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = db.SetSync(nil, []byte{0x01})
	require.Equal(t, tmdb.ErrKeyEmpty, err)

	err = db.Delete([]byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = db.Delete(nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = db.DeleteSync([]byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = db.DeleteSync(nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)

	// Setting a nil value should error, but an empty value is fine.
	err = db.Set([]byte("x"), nil)
	require.Equal(t, tmdb.ErrValueNil, err)
	err = db.SetSync([]byte("x"), nil)
	require.Equal(t, tmdb.ErrValueNil, err)

	err = db.Set([]byte("x"), []byte{})
	require.NoError(t, err)
	err = db.SetSync([]byte("x"), []byte{})
	require.NoError(t, err)
	value, err = db.Get([]byte("x"))
	require.NoError(t, err)
	require.Equal(t, []byte{}, value)
}

func TestDBIterator(t *testing.T) {
	name := fmt.Sprintf("test_%x", dbtest.RandStr(12))
	dir := os.TempDir()
	db, err := NewDB(name, dir)
	require.NoError(t, err)
	defer dbtest.CleanupDBDir(dir, name)

	for i := 0; i < 10; i++ {
		if i != 6 { // but skip 6.
			err := db.Set(dbtest.Int642Bytes(int64(i)), []byte{})
			require.NoError(t, err)
		}
	}

	// Blank iterator keys should error
	_, err = db.Iterator([]byte{}, nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	_, err = db.Iterator(nil, []byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	_, err = db.ReverseIterator([]byte{}, nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	_, err = db.ReverseIterator(nil, []byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)

	itr, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{0, 1, 2, 3, 4, 5, 7, 8, 9}, "forward iterator")

	ritr, err := db.ReverseIterator(nil, nil)
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{9, 8, 7, 5, 4, 3, 2, 1, 0}, "reverse iterator")

	itr, err = db.Iterator(nil, dbtest.Int642Bytes(0))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64(nil), "forward iterator to 0")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(10), nil)
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64(nil), "reverse iterator from 10 (ex)")

	itr, err = db.Iterator(dbtest.Int642Bytes(0), nil)
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{0, 1, 2, 3, 4, 5, 7, 8, 9}, "forward iterator from 0")

	itr, err = db.Iterator(dbtest.Int642Bytes(1), nil)
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{1, 2, 3, 4, 5, 7, 8, 9}, "forward iterator from 1")

	ritr, err = db.ReverseIterator(nil, dbtest.Int642Bytes(10))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64{9, 8, 7, 5, 4, 3, 2, 1, 0}, "reverse iterator from 10 (ex)")

	ritr, err = db.ReverseIterator(nil, dbtest.Int642Bytes(9))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64{8, 7, 5, 4, 3, 2, 1, 0}, "reverse iterator from 9 (ex)")

	ritr, err = db.ReverseIterator(nil, dbtest.Int642Bytes(8))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64{7, 5, 4, 3, 2, 1, 0}, "reverse iterator from 8 (ex)")

	itr, err = db.Iterator(dbtest.Int642Bytes(5), dbtest.Int642Bytes(6))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{5}, "forward iterator from 5 to 6")

	itr, err = db.Iterator(dbtest.Int642Bytes(5), dbtest.Int642Bytes(7))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{5}, "forward iterator from 5 to 7")

	itr, err = db.Iterator(dbtest.Int642Bytes(5), dbtest.Int642Bytes(8))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{5, 7}, "forward iterator from 5 to 8")

	itr, err = db.Iterator(dbtest.Int642Bytes(6), dbtest.Int642Bytes(7))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64(nil), "forward iterator from 6 to 7")

	itr, err = db.Iterator(dbtest.Int642Bytes(6), dbtest.Int642Bytes(8))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{7}, "forward iterator from 6 to 8")

	itr, err = db.Iterator(dbtest.Int642Bytes(7), dbtest.Int642Bytes(8))
	require.NoError(t, err)
	verifyIterator(t, itr, []int64{7}, "forward iterator from 7 to 8")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(4), dbtest.Int642Bytes(5))
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{4}, "reverse iterator from 5 (ex) to 4")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(4), dbtest.Int642Bytes(6))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64{5, 4}, "reverse iterator from 6 (ex) to 4")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(4), dbtest.Int642Bytes(7))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64{5, 4}, "reverse iterator from 7 (ex) to 4")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(5), dbtest.Int642Bytes(6))
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{5}, "reverse iterator from 6 (ex) to 5")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(5), dbtest.Int642Bytes(7))
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{5}, "reverse iterator from 7 (ex) to 5")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(6), dbtest.Int642Bytes(7))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64(nil), "reverse iterator from 7 (ex) to 6")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(10), nil)
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64(nil), "reverse iterator to 10")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(6), nil)
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{9, 8, 7}, "reverse iterator to 6")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(5), nil)
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{9, 8, 7, 5}, "reverse iterator to 5")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(8), dbtest.Int642Bytes(9))
	require.NoError(t, err)
	verifyIterator(t, ritr, []int64{8}, "reverse iterator from 9 (ex) to 8")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(2), dbtest.Int642Bytes(4))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64{3, 2}, "reverse iterator from 4 (ex) to 2")

	ritr, err = db.ReverseIterator(dbtest.Int642Bytes(4), dbtest.Int642Bytes(2))
	require.NoError(t, err)
	verifyIterator(t, ritr,
		[]int64(nil), "reverse iterator from 2 (ex) to 4")

	// Ensure that the iterators don't panic with an empty database.
	dir2, err := ioutil.TempDir("", "tm-db-test")
	require.NoError(t, err)
	db2, err := NewDB("badger", dir2)
	require.NoError(t, err)
	defer dbtest.CleanupDBDir(dir2, name)

	itr, err = db2.Iterator(nil, nil)
	require.NoError(t, err)
	verifyIterator(t, itr, nil, "forward iterator with empty db")

	ritr, err = db2.ReverseIterator(nil, nil)
	require.NoError(t, err)
	verifyIterator(t, ritr, nil, "reverse iterator with empty db")

}

func verifyIterator(t *testing.T, itr tmdb.Iterator, expected []int64, msg string) {
	var list []int64
	for itr.Valid() {
		key := itr.Key()
		list = append(list, dbtest.Bytes2Int64(key))
		itr.Next()
	}
	assert.Equal(t, expected, list, msg)
}

func TestDBBatch(t *testing.T) {
	name := fmt.Sprintf("test_%x", dbtest.RandStr(12))
	dir := os.TempDir()
	db, err := NewDB(name, dir)
	require.NoError(t, err)
	defer dbtest.CleanupDBDir(dir, name)

	// create a new batch, and some items - they should not be visible until we write
	batch := db.NewBatch()
	require.NoError(t, batch.Set([]byte("a"), []byte{1}))
	require.NoError(t, batch.Set([]byte("b"), []byte{2}))
	require.NoError(t, batch.Set([]byte("c"), []byte{3}))
	assertKeyValues(t, db, map[string][]byte{})

	err = batch.Write()
	require.NoError(t, err)
	assertKeyValues(t, db, map[string][]byte{"a": {1}, "b": {2}, "c": {3}})

	// trying to modify or rewrite a written batch should error, but closing it should work
	require.Error(t, batch.Set([]byte("a"), []byte{9}))
	require.Error(t, batch.Delete([]byte("a")))
	require.Error(t, batch.Write())
	require.Error(t, batch.WriteSync())
	require.NoError(t, batch.Close())

	// batches should write changes in order
	batch = db.NewBatch()
	require.NoError(t, batch.Delete([]byte("a")))
	require.NoError(t, batch.Set([]byte("a"), []byte{1}))
	require.NoError(t, batch.Set([]byte("b"), []byte{1}))
	require.NoError(t, batch.Set([]byte("b"), []byte{2}))
	require.NoError(t, batch.Set([]byte("c"), []byte{3}))
	require.NoError(t, batch.Delete([]byte("c")))
	require.NoError(t, batch.Write())
	require.NoError(t, batch.Close())
	assertKeyValues(t, db, map[string][]byte{"a": {1}, "b": {2}})

	// empty and nil keys, as well as nil values, should be disallowed
	batch = db.NewBatch()
	err = batch.Set([]byte{}, []byte{0x01})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = batch.Set(nil, []byte{0x01})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = batch.Set([]byte("a"), nil)
	require.Equal(t, tmdb.ErrValueNil, err)

	err = batch.Delete([]byte{})
	require.Equal(t, tmdb.ErrKeyEmpty, err)
	err = batch.Delete(nil)
	require.Equal(t, tmdb.ErrKeyEmpty, err)

	err = batch.Close()
	require.NoError(t, err)

	// it should be possible to write an empty batch
	batch = db.NewBatch()
	err = batch.Write()
	require.NoError(t, err)
	assertKeyValues(t, db, map[string][]byte{"a": {1}, "b": {2}})

	// it should be possible to close an empty batch, and to re-close a closed batch
	batch = db.NewBatch()
	batch.Close()
	batch.Close()

	// all other operations on a closed batch should error
	require.Error(t, batch.Set([]byte("a"), []byte{9}))
	require.Error(t, batch.Delete([]byte("a")))
	require.Error(t, batch.Write())
	require.Error(t, batch.WriteSync())
}

func assertKeyValues(t *testing.T, db tmdb.DB, expect map[string][]byte) {
	iter, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	defer iter.Close()

	actual := make(map[string][]byte)
	for ; iter.Valid(); iter.Next() {
		require.NoError(t, iter.Error())
		actual[string(iter.Key())] = iter.Value()
	}

	assert.Equal(t, expect, actual)
}
