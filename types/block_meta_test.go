package types

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/crypto/tmhash"
	tmrand "github.com/lazyledger/lazyledger-core/libs/rand"
)

func TestBlockMeta_ToProto(t *testing.T) {
	h := makeRandHeader()
	bi := h.Hash()

	bm := &BlockMeta{
		HeaderHash: bi,
		BlockSize:  200,
		Header:     h,
		NumTxs:     0,
	}

	tests := []struct {
		testName string
		bm       *BlockMeta
		expErr   bool
	}{
		{"success", bm, false},
		{"failure nil", nil, true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.testName, func(t *testing.T) {
			pb := tt.bm.ToProto()

			bm, err := BlockMetaFromProto(pb)

			if !tt.expErr {
				require.NoError(t, err, tt.testName)
				require.Equal(t, tt.bm, bm, tt.testName)
			} else {
				require.Error(t, err, tt.testName)
			}
		})
	}
}

func TestBlockMeta_ValidateBasic(t *testing.T) {
	h := makeRandHeader()
	bi := h.Hash()
	bi2 := tmrand.Bytes(tmhash.Size)

	bi3 := []byte("incorrect hash")

	bm := &BlockMeta{
		HeaderHash: bi,
		BlockSize:  200,
		Header:     h,
		NumTxs:     0,
	}

	bm2 := &BlockMeta{
		HeaderHash: bi2,
		BlockSize:  200,
		Header:     h,
		NumTxs:     0,
	}

	bm3 := &BlockMeta{
		HeaderHash: bi3,
		BlockSize:  200,
		Header:     h,
		NumTxs:     0,
	}

	tests := []struct {
		name    string
		bm      *BlockMeta
		wantErr bool
	}{
		{"success", bm, false},
		{"failure wrong HeaderHash hash", bm2, true},
		{"failure wrong length HeaderHash hash", bm3, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.bm.ValidateBasic(); (err != nil) != tt.wantErr {
				t.Errorf("BlockMeta.ValidateBasic() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
