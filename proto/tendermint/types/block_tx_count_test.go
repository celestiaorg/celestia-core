package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestCountBlockTxs(t *testing.T) {
	data, err := (&Data{Txs: make([][]byte, 3)}).Marshal()
	require.NoError(t, err)
	dataField := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), data)
	block, err := (&Block{LastCommit: &Commit{Height: 1}}).Marshal()
	require.NoError(t, err)
	// Unmarshal merges repeated Block.data fields, so their txs add up.
	block = append(append(block, dataField...), dataField...)

	n, err := CountBlockTxs(block, 10)
	require.NoError(t, err)
	require.Equal(t, 6, n)
	n, err = CountBlockTxs(block, 4)
	require.NoError(t, err)
	require.Equal(t, 5, n, "counting stops at max+1")

	_, err = CountBlockTxs(append(block, 0x12), 10)
	require.Error(t, err, "a truncated field is malformed")
}
