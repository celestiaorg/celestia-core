package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/types"
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

func TestHaveParts_SetIndex(t *testing.T) {
	testCases := []struct {
		name      string
		existing  []PartMetaData
		newIndex  uint32
		newHash   []byte
		newProof  *merkle.Proof
		expLength int
	}{
		{
			name:      "append single part",
			existing:  nil,
			newIndex:  0,
			newHash:   []byte("hash0"),
			newProof:  &merkle.Proof{}, // empty proof
			expLength: 1,
		},
		{
			name: "append second part",
			existing: []PartMetaData{
				{Index: 1, Hash: []byte("hash1")},
			},
			newIndex:  2,
			newHash:   []byte("hash2"),
			newProof:  &merkle.Proof{},
			expLength: 2,
		},
		{
			name: "append with non-empty proof",
			existing: []PartMetaData{
				{Index: 10, Hash: []byte("hash10")},
			},
			newIndex:  11,
			newHash:   []byte("hash11"),
			newProof:  &merkle.Proof{Total: 2, Index: 1},
			expLength: 2,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			hp := &HaveParts{
				Parts: tc.existing,
			}

			hp.SetIndex(tc.newIndex, tc.newHash, tc.newProof)
			require.Equal(t, tc.expLength, len(hp.Parts), "length of Parts after SetIndex")

			// Check last appended part
			last := hp.Parts[len(hp.Parts)-1]
			require.Equal(t, tc.newIndex, last.Index)
			require.Equal(t, tc.newHash, last.Hash)
			require.Equal(t, tc.newProof.String(), last.Proof.String())
		})
	}
}

func TestHaveParts_RemoveIndex(t *testing.T) {
	testCases := []struct {
		name     string
		existing []PartMetaData
		remove   uint32
		expLeft  []uint32 // expected remaining indexes
	}{
		{
			name: "remove existing part",
			existing: []PartMetaData{
				{Index: 0, Hash: []byte("hash0")},
				{Index: 1, Hash: []byte("hash1")},
				{Index: 2, Hash: []byte("hash2")},
			},
			remove:  1,
			expLeft: []uint32{0, 2},
		},
		{
			name: "remove non-existing part",
			existing: []PartMetaData{
				{Index: 5, Hash: []byte("hash5")},
				{Index: 6, Hash: []byte("hash6")},
			},
			remove:  10,
			expLeft: []uint32{5, 6},
		},
		{
			name:     "remove from empty slice",
			existing: nil,
			remove:   0,
			expLeft:  []uint32{},
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			hp := &HaveParts{
				Parts: tc.existing,
			}

			hp.RemoveIndex(tc.remove)
			require.Equal(t, len(tc.expLeft), len(hp.Parts), "length after removal")

			// Check the leftover indexes
			for i, part := range hp.Parts {
				require.Equal(t, tc.expLeft[i], part.Index)
			}
		})
	}
}

func TestHaveParts_IsEmpty(t *testing.T) {
	testCases := []struct {
		name      string
		haveParts *HaveParts
		expEmpty  bool
	}{
		{
			name: "no parts -> IsEmpty=true",
			haveParts: &HaveParts{
				Parts: nil,
			},
			expEmpty: true,
		},
		{
			name: "non-empty parts -> IsEmpty=false",
			haveParts: &HaveParts{
				Parts: []PartMetaData{
					{Index: 0, Hash: []byte("hash0")},
				},
			},
			expEmpty: false,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expEmpty, tc.haveParts.IsEmpty())
		})
	}
}

func TestHaveParts_GetIndex(t *testing.T) {
	testCases := []struct {
		name     string
		existing []PartMetaData
		checkIdx uint32
		expFound bool
	}{
		{
			name: "part found at index",
			existing: []PartMetaData{
				{Index: 0, Hash: []byte("h0")},
				{Index: 1, Hash: []byte("h1")},
				{Index: 2, Hash: []byte("h2")},
			},
			checkIdx: 1,
			expFound: true,
		},
		{
			name: "part not found",
			existing: []PartMetaData{
				{Index: 10, Hash: []byte("h10")},
			},
			checkIdx: 5,
			expFound: false,
		},
		{
			name:     "no parts at all",
			existing: nil,
			checkIdx: 2,
			expFound: false,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			hp := &HaveParts{
				Parts: tc.existing,
			}
			got := hp.GetIndex(tc.checkIdx)
			require.Equal(t, tc.expFound, got, "GetIndex result mismatch")
		})
	}
}
