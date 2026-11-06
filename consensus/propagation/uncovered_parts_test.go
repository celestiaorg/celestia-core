package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

func TestUncoveredParts(t *testing.T) {
	const partSize = 100

	tx := func(start, end uint32) proptypes.TxMetaData {
		return proptypes.TxMetaData{Start: start, End: end}
	}

	tests := map[string]struct {
		txs     []proptypes.TxMetaData
		total   uint32
		lastLen uint32
		want    []uint32
	}{
		"header only": {
			total:   1,
			lastLen: 40,
			want:    []uint32{0},
		},
		"empty block": {
			total:   2,
			lastLen: partSize,
			want:    []uint32{0, 1},
		},
		"header and last commit straddling a boundary": {
			txs:     []proptypes.TxMetaData{tx(10, 190)},
			total:   3,
			lastLen: 50,
			want:    []uint32{0, 1, 2},
		},
		"gap between transactions": {
			txs:     []proptypes.TxMetaData{tx(0, 100), tx(250, 400)},
			total:   4,
			lastLen: partSize,
			want:    []uint32{1, 2},
		},
		"unsorted and overlapping ranges": {
			txs:     []proptypes.TxMetaData{tx(150, 300), tx(0, 100), tx(60, 160)},
			total:   3,
			lastLen: partSize,
			want:    []uint32{},
		},
		"short last part covered": {
			txs:     []proptypes.TxMetaData{tx(0, 140)},
			total:   2,
			lastLen: 40,
			want:    []uint32{},
		},
		"exactly the cap": {
			total:   maxPushedParts,
			lastLen: partSize,
			want:    []uint32{0, 1, 2, 3, 4, 5},
		},
		"above the cap pushes nothing": {
			total:   maxPushedParts + 1,
			lastLen: partSize,
			want:    nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, uncoveredParts(tc.txs, tc.total, partSize, tc.lastLen))
		})
	}
}
