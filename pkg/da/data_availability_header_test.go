package da

import (
	"bytes"
	"strings"
	"testing"

	"github.com/celestiaorg/celestia-core/pkg/consts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNilDataAvailabilityHeaderHashDoesntCrash(t *testing.T) {
	// This follows RFC-6962, i.e. `echo -n '' | sha256sum`
	var emptyBytes = []byte{0xe3, 0xb0, 0xc4, 0x42, 0x98, 0xfc, 0x1c, 0x14, 0x9a, 0xfb, 0xf4, 0xc8,
		0x99, 0x6f, 0xb9, 0x24, 0x27, 0xae, 0x41, 0xe4, 0x64, 0x9b, 0x93, 0x4c, 0xa4, 0x95, 0x99, 0x1b,
		0x78, 0x52, 0xb8, 0x55}

	assert.Equal(t, emptyBytes, (*DataAvailabilityHeader)(nil).Hash())
	assert.Equal(t, emptyBytes, new(DataAvailabilityHeader).Hash())
}

func TestMinDataAvailabilityHeader(t *testing.T) {
	dah := MinDataAvailabilityHeader()
	expectedHash := []byte{
		0x7b, 0x57, 0x8b, 0x35, 0x1b, 0x1b, 0xb, 0xbd, 0x70, 0xbb, 0x35, 0x0, 0x19, 0xeb, 0xc9, 0x64,
		0xc4, 0x4a, 0x14, 0xa, 0x37, 0xef, 0x71, 0x5b, 0x55, 0x2a, 0x7f, 0x8f, 0x31, 0x5a, 0xcd, 0x19,
	}
	require.Equal(t, expectedHash, dah.hash)
	require.NoError(t, dah.ValidateBasic())
	// important note: also see the types.TestEmptyBlockDataAvailabilityHeader test
	// which ensures that empty block data results in the minimum data availability
	// header
}

func TestNewDataAvailabilityHeader(t *testing.T) {
	type test struct {
		name         string
		expectedHash []byte
		expectedErr  bool
		squareSize   uint64
		shares       [][]byte
	}

	tests := []test{
		{
			name:        "typical",
			expectedErr: false,
			expectedHash: []byte{
				0x6e, 0xf4, 0x94, 0x23, 0x0, 0x15, 0x8c, 0x12, 0x54, 0xca, 0xb, 0xcd, 0xe8, 0x5, 0x8c, 0xc6,
				0xc8, 0x13, 0xf2, 0xea, 0xaa, 0x48, 0x24, 0x1b, 0x27, 0x34, 0xf5, 0x39, 0x66, 0x8, 0xdb, 0xa6,
			},
			squareSize: 2,
			shares:     generateShares(4, 1),
		},
		{
			name:        "max square size",
			expectedErr: false,
			expectedHash: []byte{
				0xa1, 0xda, 0x0, 0x73, 0x2b, 0x55, 0x93, 0x9d, 0xf6, 0x91, 0xd0, 0xa6, 0x23, 0x7d, 0xf9, 0xd7,
				0x52, 0x60, 0xce, 0x48, 0x37, 0xcc, 0x1, 0xd4, 0x25, 0x65, 0xe5, 0xa1, 0xcd, 0xd1, 0x1d, 0x89,
			},
			squareSize: consts.MaxSquareSize,
			shares:     generateShares(consts.MaxSquareSize*consts.MaxSquareSize, 99),
		},
		{
			name:        "too large square size",
			expectedErr: true,
			squareSize:  consts.MaxSquareSize + 1,
			shares:      generateShares((consts.MaxSquareSize+1)*(consts.MaxSquareSize+1), 1),
		},
		{
			name:        "invalid number of shares",
			expectedErr: true,
			squareSize:  2,
			shares:      generateShares(5, 1),
		},
	}

	for _, tt := range tests {
		tt := tt
		resdah, err := NewDataAvailabilityHeader(tt.squareSize, tt.shares)
		if tt.expectedErr {
			require.NotNil(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tt.squareSize*2, uint64(len(resdah.ColumnRoots)), tt.name)
		require.Equal(t, tt.squareSize*2, uint64(len(resdah.RowsRoots)), tt.name)
		require.Equal(t, tt.expectedHash, resdah.hash, tt.name)
	}
}

func TestDataAvailabilityHeaderProtoConversion(t *testing.T) {
	type test struct {
		name string
		dah  DataAvailabilityHeader
	}

	shares := generateShares(consts.MaxSquareSize*consts.MaxSquareSize, 1)
	bigdah, err := NewDataAvailabilityHeader(consts.MaxSquareSize, shares)
	require.NoError(t, err)

	tests := []test{
		{
			name: "min",
			dah:  MinDataAvailabilityHeader(),
		},
		{
			name: "max",
			dah:  bigdah,
		},
	}

	for _, tt := range tests {
		tt := tt
		pdah, err := tt.dah.ToProto()
		require.NoError(t, err)
		resDah, err := DataAvailabilityHeaderFromProto(pdah)
		require.NoError(t, err)
		resDah.Hash() // calc the hash to make the comparisons fair
		require.Equal(t, tt.dah, *resDah, tt.name)
	}

}

func Test_DAHValidateBasic(t *testing.T) {
	type test struct {
		name      string
		dah       DataAvailabilityHeader
		expectErr bool
		errStr    string
	}

	shares := generateShares(consts.MaxSquareSize*consts.MaxSquareSize, 1)
	bigdah, err := NewDataAvailabilityHeader(consts.MaxSquareSize, shares)
	require.NoError(t, err)
	// make a mutant dah that has too many roots
	var tooBigDah DataAvailabilityHeader
	tooBigDah.ColumnRoots = make([][]byte, consts.MaxSquareSize*consts.MaxSquareSize)
	tooBigDah.RowsRoots = make([][]byte, consts.MaxSquareSize*consts.MaxSquareSize)
	copy(tooBigDah.ColumnRoots, bigdah.ColumnRoots)
	copy(tooBigDah.RowsRoots, bigdah.RowsRoots)
	tooBigDah.ColumnRoots = append(tooBigDah.ColumnRoots, bytes.Repeat([]byte{1}, 32))
	tooBigDah.RowsRoots = append(tooBigDah.RowsRoots, bytes.Repeat([]byte{1}, 32))
	// make a mutant dah that has too few roots
	var tooSmallDah DataAvailabilityHeader
	tooSmallDah.ColumnRoots = [][]byte{bytes.Repeat([]byte{2}, 32)}
	tooSmallDah.RowsRoots = [][]byte{bytes.Repeat([]byte{2}, 32)}
	// use a bad hash
	badHashDah := MinDataAvailabilityHeader()
	badHashDah.hash = []byte{1, 2, 3, 4}
	// dah with not equal number of roots
	mismatchDah := MinDataAvailabilityHeader()
	mismatchDah.ColumnRoots = append(mismatchDah.ColumnRoots, bytes.Repeat([]byte{2}, 32))

	tests := []test{
		{
			name: "min",
			dah:  MinDataAvailabilityHeader(),
		},
		{
			name: "max",
			dah:  bigdah,
		},
		{
			name:      "too big dah",
			dah:       tooBigDah,
			expectErr: true,
			errStr:    "maximum valid DataAvailabilityHeader has at most",
		},
		{
			name:      "too small dah",
			dah:       tooSmallDah,
			expectErr: true,
			errStr:    "minimum valid DataAvailabilityHeader has at least",
		},
		{
			name:      "bash hash",
			dah:       badHashDah,
			expectErr: true,
			errStr:    "wrong hash",
		},
		{
			name:      "mismatched roots",
			dah:       mismatchDah,
			expectErr: true,
			errStr:    "unequal number of row and column roots",
		},
	}

	for _, tt := range tests {
		tt := tt
		err := tt.dah.ValidateBasic()
		if tt.expectErr {
			require.True(t, strings.Contains(err.Error(), tt.errStr), tt.name)
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
	}
}

func generateShares(count int, repeatByte byte) [][]byte {
	shares := make([][]byte, count)
	for i := 0; i < count; i++ {
		shares[i] = bytes.Repeat([]byte{repeatByte}, consts.ShareSize)
	}
	return shares
}
