package types

import (
	"errors"
	"testing"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxMetaData_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data *TxMetaData
	}{
		{
			"valid metadata",
			&TxMetaData{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10},
		},
		{
			"empty hash",
			&TxMetaData{Hash: []byte{}, Start: 5, End: 10},
		},
		{
			"same start and end",
			&TxMetaData{Hash: rand.Bytes(tmhash.Size), Start: 7, End: 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto := tt.data.ToProto()
			roundTripped := TxMetaDataFromProto(proto)
			require.Equal(t, tt.data, roundTripped)
		})
	}
}

func TestTxMetaData_ValidateBasic(t *testing.T) {
	tests := []struct {
		name    string
		data    *TxMetaData
		expects error
	}{
		{
			"valid metadata",
			&TxMetaData{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10},
			nil,
		},
		{
			"invalid start > end",
			&TxMetaData{Hash: rand.Bytes(tmhash.Size), Start: 10, End: 5},
			errors.New("TxMetaData: Start > End"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.data.ValidateBasic()
			if tt.expects != nil {
				assert.EqualError(t, err, tt.expects.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCompactBlock_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data *CompactBlock
	}{
		{
			"valid block",
			&CompactBlock{
				Height: 1, Round: 1, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto := tt.data.ToProto()
			// Assuming CompactBlockFromProto exists
			roundTripped, err := CompactBlockFromProto(proto)
			require.NoError(t, err)
			require.Equal(t, tt.data, roundTripped)
		})
	}
}

func TestCompactBlock_ValidateBasic(t *testing.T) {
	tests := []struct {
		name    string
		data    *CompactBlock
		expects error
	}{
		{
			"valid block",
			&CompactBlock{
				Height: 1, Round: 1, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
			},
			nil,
		},
		{
			"negative height",
			&CompactBlock{
				Height: -1, Round: 1, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
			},
			errors.New("CompactBlock: Height cannot be negative"),
		},
		{
			"negative round",
			&CompactBlock{
				Height: 1, Round: -2, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
			},
			errors.New("CompactBlock: Round cannot be negative"),
		},
		{
			"invalid bp_hash",
			&CompactBlock{
				Height: 1, Round: 1, BpHash: rand.Bytes(tmhash.Size + 1),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
			},
			errors.New("expected size to be 32 bytes, got 33 bytes"),
		},
		{
			"too big of signature",
			&CompactBlock{
				Height: 1, Round: 1, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize + 1),
			},
			errors.New("CompactBlock: Signature is too big"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.data.ValidateBasic()
			if tt.expects != nil {
				assert.EqualError(t, err, tt.expects.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

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
