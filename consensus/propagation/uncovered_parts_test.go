package propagation

import (
	"testing"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/stretchr/testify/require"
)

func TestUncoveredParts(t *testing.T) {
	const partSize = 100

	blob := func(start, end uint32) proptypes.TxMetaData {
		return proptypes.TxMetaData{Start: start, End: end}
	}

	tests := map[string]struct {
		blobs   []proptypes.TxMetaData
		total   uint32
		lastLen uint32
		want    []uint32
	}{
		"transactions tile every part": {
			blobs:   []proptypes.TxMetaData{blob(0, 150), blob(150, 300)},
			total:   3,
			lastLen: partSize,
			want:    []uint32{},
		},
		"no transactions leaves every part uncovered": {
			blobs:   nil,
			total:   3,
			lastLen: partSize,
			want:    []uint32{0, 1, 2},
		},
		"a gap in the middle": {
			blobs:   []proptypes.TxMetaData{blob(0, 100), blob(250, 400)},
			total:   4,
			lastLen: partSize,
			want:    []uint32{1, 2},
		},
		"a short final part is covered by a short range": {
			blobs:   []proptypes.TxMetaData{blob(0, 100), blob(100, 140)},
			total:   2,
			lastLen: 40,
			want:    []uint32{},
		},
		"overlapping ranges merge": {
			blobs:   []proptypes.TxMetaData{blob(0, 120), blob(60, 200)},
			total:   2,
			lastLen: partSize,
			want:    []uint32{},
		},
		"unsorted ranges are handled": {
			blobs:   []proptypes.TxMetaData{blob(200, 300), blob(0, 100), blob(100, 200)},
			total:   3,
			lastLen: partSize,
			want:    []uint32{},
		},
		"more uncovered parts than the cap are truncated": {
			blobs:   nil,
			total:   maxPushedParts + 5,
			lastLen: partSize,
			want:    []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := uncoveredParts(tc.blobs, tc.total, partSize, tc.lastLen)

			require.Equal(t, tc.want, got)
			require.LessOrEqual(t, len(got), maxPushedParts)
		})
	}
}
