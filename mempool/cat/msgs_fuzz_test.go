package cat

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	cmttypes "github.com/cometbft/cometbft/types"
)

// FuzzMempoolMessage exercises the allocation precheck before the full CAT
// envelope decoder. CheckTx itself is covered by test/fuzz/tests/FuzzMempool.
func FuzzMempoolMessage(f *testing.F) {
	for _, seed := range []p2p.Wrapper{
		&protomem.Txs{Txs: [][]byte{[]byte("key=value")}},
		&protomem.SeenTx{TxKey: bytes.Repeat([]byte{1}, 32), Signer: []byte("signer"), Sequence: 1},
		&protomem.WantTx{TxKey: bytes.Repeat([]byte{1}, 32)},
	} {
		raw, err := proto.Marshal(seed.Wrap())
		require.NoError(f, err)
		require.NoError(f, validateMempoolBytes(raw))
		var decoded protomem.Message
		require.NoError(f, proto.Unmarshal(raw, &decoded))
		_, err = decoded.Unwrap()
		require.NoError(f, err)
		f.Add(raw)
	}
	// Empty and over-limit lists, including a repeated oneof field, exercise
	// the precheck on inputs that never reach transaction handling.
	for _, raw := range [][]byte{nil, {0xff}, {0x0a, 0}, {0x0a, 2, 0x0a, 0, 0x0a, 2, 0x0a, 0}} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		if err := validateMempoolBytes(raw); err != nil {
			return
		}
		var envelope protomem.Message
		if err := proto.Unmarshal(raw, &envelope); err != nil {
			return
		}
		msg, err := envelope.Unwrap()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *protomem.Txs:
			require.LessOrEqual(t, len(m.Txs), mempool.MaxTxsPerMessage)
			for _, tx := range m.Txs {
				cached := cmttypes.Tx(tx).ToCachedTx()
				_ = cached.Key()
				_ = cached.Hash()
			}
		case *protomem.SeenTx:
			_, _ = cmttypes.TxKeyFromBytes(m.TxKey)
		case *protomem.WantTx:
			_, _ = cmttypes.TxKeyFromBytes(m.TxKey)
		default:
			t.Fatalf("unexpected mempool message %T", msg)
		}
		encoded, err := proto.Marshal(msg.(p2p.Wrapper).Wrap())
		require.NoError(t, err)
		require.NoError(t, validateMempoolBytes(encoded))
		var decoded protomem.Message
		require.NoError(t, proto.Unmarshal(encoded, &decoded))
		back, err := decoded.Unwrap()
		require.NoError(t, err)
		require.True(t, proto.Equal(msg, back), "message changed across proto round trip")
	})
}
