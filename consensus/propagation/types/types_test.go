package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/rand"
)

func TestWantParts_ValidateBasic(t *testing.T) {
	tests := []struct {
		name    string
		want    WantParts
		wantErr bool
	}{
		{
			"valid want parts",
			WantParts{Height: 1, Round: 1, Parts: &bits.BitArray{}},
			false,
		},
		{
			"nil bit array",
			WantParts{Height: 1, Round: 1, Parts: nil},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.want.ValidateBasic()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHavePartsProtoRoundtrip(t *testing.T) {
	_, proofs := merkle.ProofsFromByteSlices([][]byte{
		[]byte("apple"),
		[]byte("watermelon"),
		[]byte("kiwi"),
	})
	tests := []struct {
		name    string
		have    *HaveParts
		wantErr bool
	}{
		{
			name: "Valid encoding and decoding",
			have: &HaveParts{
				Height: 10,
				Round:  1,
				Parts: []PartMetaData{
					{
						Index: 0,
						Hash:  rand.Bytes(tmhash.Size),
						Proof: *proofs[0],
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Empty parts should fail",
			have: &HaveParts{
				Height: 10,
				Round:  1,
				Parts:  []PartMetaData{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protoMsg := tt.have.ToProto()
			got, err := HavePartFromProto(protoMsg)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.have, got)
			}
		})
	}
}

func TestPartMetaDataValidateBasic(t *testing.T) {
	_, proofs := merkle.ProofsFromByteSlices([][]byte{
		[]byte("apple"),
		[]byte("watermelon"),
		[]byte("kiwi"),
	})
	tests := []struct {
		name    string
		part    PartMetaData
		wantErr bool
	}{
		{
			name: "Valid part",
			part: PartMetaData{
				Index: 1,
				Hash:  rand.Bytes(tmhash.Size),
				Proof: *proofs[0],
			},
			wantErr: false,
		},
		{
			name: "invalid hash",
			part: PartMetaData{
				Index: 1,
				Hash:  []byte{0, 1, 2, 3},
				Proof: *proofs[0],
			},
			wantErr: true,
		},
		{
			name:    "no parts",
			part:    PartMetaData{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.part.ValidateBasic()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHavePartsValidateBasic(t *testing.T) {
	_, proofs := merkle.ProofsFromByteSlices([][]byte{
		[]byte("apple"),
		[]byte("watermelon"),
		[]byte("kiwi"),
	})
	tests := []struct {
		name    string
		have    HaveParts
		wantErr bool
	}{
		{
			name: "Valid HaveParts",
			have: HaveParts{
				Height: 10,
				Round:  1,
				Parts: []PartMetaData{
					{
						Index: 0,
						Hash:  rand.Bytes(tmhash.Size),
						Proof: *proofs[0],
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Empty parts should fail",
			have: HaveParts{
				Height: 10,
				Round:  1,
				Parts:  []PartMetaData{},
			},
			wantErr: true,
		},
		{
			name: "Negative height",
			have: HaveParts{
				Height: -1,
				Round:  1,
				Parts: []PartMetaData{
					{
						Index: 0,
						Hash:  rand.Bytes(tmhash.Size),
						Proof: *proofs[0],
					},
				},
			},
			wantErr: true,
		},
		{
			name: "Negative round",
			have: HaveParts{
				Height: 1,
				Round:  -2,
				Parts: []PartMetaData{
					{
						Index: 0,
						Hash:  rand.Bytes(tmhash.Size),
						Proof: *proofs[0],
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.have.ValidateBasic()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
