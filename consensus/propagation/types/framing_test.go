package types

import (
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// TestUnmarshalledTxFramingMatchesProto pins the hand written framing to the
// protobuf encoder. A transaction sits inside the block as the key of Data.txs,
// its length, then its bytes, which is byte for byte what marshaling a
// one-element mempool.Txs produces. The lengths chosen straddle every varint
// width boundary the block can reach.
func TestUnmarshalledTxFramingMatchesProto(t *testing.T) {
	sizes := []int{0, 1, 2, 126, 127, 128, 129, 16383, 16384, 16385, 2097151, 2097152}

	for _, size := range sizes {
		tx := make([]byte, size)
		for i := range tx {
			tx[i] = byte(i)
		}

		expected, err := proto.Marshal(&mempool.Txs{Txs: [][]byte{tx}})
		require.NoError(t, err)

		framed := NewUnmarshalledTx(TxMetaData{}, types.TxKey{}, tx)
		require.Equal(t, uint32(len(expected)), framed.Len(), "size %d", size)
		require.Equal(t, expected, framed.Bytes(), "size %d", size)
	}
}

// TestUnmarshalledTxCopyInto walks every start offset and length of a framed
// transaction, which is what the part builder does at each part boundary.
func TestUnmarshalledTxCopyInto(t *testing.T) {
	tx := make([]byte, 300)
	for i := range tx {
		tx[i] = byte(i)
	}
	framed := NewUnmarshalledTx(TxMetaData{}, types.TxKey{}, tx)
	whole := framed.Bytes()

	total := framed.Len()
	for offset := uint32(0); offset <= total; offset++ {
		for length := uint32(0); offset+length <= total; length++ {
			dst := make([]byte, length)
			require.True(t, framed.copyInto(dst, offset, length))
			require.Equal(t, whole[offset:offset+length], dst, "offset %d length %d", offset, length)
		}
	}

	require.False(t, framed.copyInto(make([]byte, 1), total, 1))
	require.False(t, framed.copyInto(make([]byte, 0), 0, 1))
}
