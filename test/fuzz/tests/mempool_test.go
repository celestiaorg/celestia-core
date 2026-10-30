//go:build gofuzz || go1.21

package tests

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	abciclient "github.com/cometbft/cometbft/abci/client"
	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/internal/fuzzinput"
	"github.com/cometbft/cometbft/libs/log"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat"
	cmttypes "github.com/cometbft/cometbft/types"
)

const mempoolMaxTxBytes = 4096

// FuzzMempool sends transactions through cat.TxPool.CheckTx backed by the
// kvstore app. With empty ops it submits raw (twice, to cover resubmission);
// otherwise ops describes a sequence of kvstore-style transactions.
func FuzzMempool(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("key=value"), []byte("key:value"), []byte("val=invalid"), bytes.Repeat([]byte("x"), mempoolMaxTxBytes+1)} {
		f.Add(seed, []byte(nil))
	}
	// Structured seeds: count, then per tx a kind followed by its fields.
	f.Add([]byte(nil), []byte{3, 0, 1, 'k', 1, 'v', 9, 0, 1, 1, 'k', 1, 'v'})
	f.Add([]byte(nil), []byte{4, 8, 2, 'a', 'b', 8, 3, 8, 4, 9, 0})
	f.Add([]byte(nil), []byte{5, 2, 0, 1, 'v', 3, 1, 1, 'k', 4, 1, 'x', 5, 0, 6, 1, 5, 7, 2})
	f.Add([]byte(nil), []byte{2, 6, 4, 1, 2, 3, 4, 7, 7, 10, 3, 'a', '=', 'b'})

	f.Fuzz(func(t *testing.T, raw []byte, ops []byte) {
		if len(raw) > 1<<20 {
			return
		}
		// Start each input with an empty pool and caches. Otherwise accepted
		// inputs eventually fill the pool and stop reaching ABCI CheckTx.
		app := kvstore.NewInMemoryApplication()
		t.Cleanup(func() { require.NoError(t, app.Close()) })
		conn := abciclient.NewLocalClient(new(cmtsync.Mutex), app)
		require.NoError(t, conn.Start())
		t.Cleanup(func() { require.NoError(t, conn.Stop()) })
		cfg := cmtcfg.DefaultMempoolConfig()
		cfg.Broadcast = false
		cfg.MaxTxBytes = mempoolMaxTxBytes
		mp := cat.NewTxPool(log.NewNopLogger(), cfg, conn, 0)

		var txs [][]byte
		if len(ops) == 0 {
			txs = [][]byte{raw, raw}
		} else {
			txs = genMempoolTxs(fuzzinput.New(ops))
		}

		pool := &mempoolModel{accepted: map[cmttypes.TxKey][]byte{}}
		for _, tx := range txs {
			pool.checkTx(t, mp, tx)
		}
		for key, tx := range pool.accepted {
			got, ok := mp.GetTxByKey(key)
			require.True(t, ok)
			require.Equal(t, tx, []byte(got.Tx))
		}
		mp.Flush()
		require.Zero(t, mp.Size())
		require.Zero(t, mp.SizeBytes())
	})
}

// mempoolModel tracks the transactions the pool must hold.
type mempoolModel struct {
	accepted map[cmttypes.TxKey][]byte
	bytes    int64
}

// checkTx submits tx and checks the outcome and pool totals against the model.
func (m *mempoolModel) checkTx(t *testing.T, mp *cat.TxPool, tx []byte) {
	t.Helper()
	key := cmttypes.Tx(tx).Key()
	var response *abci.ResponseCheckTx
	err := mp.CheckTx(tx, func(r *abci.ResponseCheckTx) { response = r }, mempool.TxInfo{})
	_, inPool := m.accepted[key]
	switch {
	case len(tx) > mempoolMaxTxBytes:
		var tooLarge mempool.ErrTxTooLarge
		require.ErrorAs(t, err, &tooLarge)
		require.Nil(t, response)
		require.False(t, mp.Has(key))
	case inPool:
		require.ErrorIs(t, err, cat.ErrTxInMempool)
		require.Nil(t, response)
	default:
		require.NoError(t, err)
		require.NotNil(t, response)
		want := kvstoreAccepts(tx)
		require.Equal(t, want, response.Code == abci.CodeTypeOK, "tx %q: code %d, log %q", tx, response.Code, response.Log)
		if want {
			m.accepted[key] = tx
			m.bytes += int64(len(tx))
			got, ok := mp.GetTxByKey(key)
			require.True(t, ok)
			require.Equal(t, tx, []byte(got.Tx))
		} else {
			require.False(t, mp.Has(key))
		}
	}
	require.Equal(t, len(m.accepted), mp.Size())
	require.Equal(t, m.bytes, mp.SizeBytes())
}

// kvstoreAccepts mirrors kvstore.Application.CheckTx: a validator update
// "val=type!base64(pubkey)!power" must parse with a non-negative power;
// anything else must contain exactly one ':' or '=' (not both), not at
// either end.
func kvstoreAccepts(tx []byte) bool {
	s := string(tx)
	if rest, ok := strings.CutPrefix(s, kvstore.ValidatorPrefix); ok {
		parts := strings.Split(rest, "!")
		if len(parts) != 3 {
			return false
		}
		if _, err := base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return false
		}
		power, err := strconv.ParseInt(parts[2], 10, 64)
		return err == nil && power >= 0
	}
	colons, equals := strings.Count(s, ":"), strings.Count(s, "=")
	var sep string
	switch {
	case colons == 1 && equals == 0:
		sep = ":"
	case equals == 1 && colons == 0:
		sep = "="
	default:
		return false
	}
	return !strings.HasPrefix(s, sep) && !strings.HasSuffix(s, sep)
}

// genMempoolTxs builds up to 16 kvstore-style transactions from r.
func genMempoolTxs(r *fuzzinput.Reader) [][]byte {
	n := 1 + r.Intn(16)
	txs := make([][]byte, 0, n)
	for range n {
		var tx []byte
		switch r.Intn(11) {
		case 0: // key=value
			tx = kvTx(mempoolWord(r), "=", mempoolWord(r))
		case 1: // key:value
			tx = kvTx(mempoolWord(r), ":", mempoolWord(r))
		case 2: // empty key
			tx = kvTx(nil, mempoolSep(r), mempoolWord(r))
		case 3: // empty value
			tx = kvTx(mempoolWord(r), mempoolSep(r), nil)
		case 4: // no separator
			tx = mempoolWord(r)
		case 5: // invalid separators: repeated or mixed
			tx = kvTx(kvTx(mempoolWord(r), mempoolSep(r), mempoolWord(r)), mempoolSep(r), mempoolWord(r))
		case 6: // well-formed validator update
			tx = []byte(kvstore.ValidatorPrefix + "ed25519!" + base64.StdEncoding.EncodeToString(r.Bytes(32)) + "!" + strconv.Itoa(int(r.Uint16())))
		case 7: // malformed validator update
			pk := base64.StdEncoding.EncodeToString(r.Bytes(32))
			power := strconv.Itoa(int(r.Uint16()))
			var body string
			switch r.Intn(5) {
			case 0: // wrong field count
				body = "ed25519!" + pk
			case 1: // invalid base64
				body = "ed25519!" + pk + "*!" + power
			case 2: // power is not an int
				body = "ed25519!" + pk + "!x" + power
			case 3: // negative power
				body = "ed25519!" + pk + "!-1" + power
			case 4: // arbitrary value
				body = string(r.Bytes(64))
			}
			tx = []byte(kvstore.ValidatorPrefix + body)
		case 8: // valid key=value sized around MaxTxBytes
			size := mempoolMaxTxBytes - 2 + r.Intn(5)
			key := mempoolWord(r)
			if len(key) == 0 {
				key = []byte("k")
			}
			tx = kvTx(key, "=", bytes.Repeat([]byte("v"), max(1, size-len(key)-1)))
		case 9: // resubmit an earlier tx
			if len(txs) == 0 {
				tx = []byte("key=value")
			} else {
				tx = txs[r.Intn(len(txs))]
			}
		case 10: // raw bytes
			tx = r.Bytes(64)
		}
		txs = append(txs, tx)
	}
	return txs
}

// mempoolWord reads up to 16 bytes and replaces kvstore separators, so the
// generator controls where separators appear.
func mempoolWord(r *fuzzinput.Reader) []byte {
	w := r.Bytes(16)
	for i, b := range w {
		if b == ':' || b == '=' {
			w[i] = '_'
		}
	}
	return w
}

func mempoolSep(r *fuzzinput.Reader) string {
	if r.Bool() {
		return ":"
	}
	return "="
}

func kvTx(key []byte, sep string, value []byte) []byte {
	tx := append([]byte(nil), key...)
	tx = append(tx, sep...)
	return append(tx, value...)
}
