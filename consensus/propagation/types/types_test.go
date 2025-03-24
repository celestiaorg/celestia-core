package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	mockProposal := types.NewProposal(
		4, 2, 2,
		makeBlockID(rand.Bytes(tmhash.Size), 777, rand.Bytes(tmhash.Size)),
	)
	mockProposal.Signature = rand.Bytes(types.MaxSignatureSize)
	tests := []struct {
		name string
		data *CompactBlock
	}{
		{
			"valid block",
			&CompactBlock{
				BpHash:      rand.Bytes(tmhash.Size),
				Blobs:       []TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature:   rand.Bytes(types.MaxSignatureSize),
				Proposal:    *mockProposal,
				PartsHashes: [][]byte{rand.Bytes(tmhash.Size), rand.Bytes(tmhash.Size)},
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
	id := types.BlockID{Hash: rand.Bytes(32), PartSetHeader: types.PartSetHeader{Total: 1, Hash: rand.Bytes(32)}}
	prop := types.NewProposal(1, 0, 0, id)
	prop.Signature = rand.Bytes(64)

	tests := []struct {
		name    string
		data    *CompactBlock
		expects error
	}{
		{
			"valid block",
			&CompactBlock{
				BpHash:    rand.Bytes(tmhash.Size),
				Blobs:     []TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
				Proposal:  *prop,
			},
			nil,
		},
		{
			"invalid bp_hash",
			&CompactBlock{
				BpHash:    rand.Bytes(tmhash.Size + 1),
				Blobs:     []TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize),
				Proposal:  *prop,
			},
			errors.New("expected size to be 32 bytes, got 33 bytes"),
		},
		{
			"invalid part set hashes length",
			&CompactBlock{
				BpHash:      rand.Bytes(tmhash.Size),
				Blobs:       []TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature:   rand.Bytes(types.MaxSignatureSize),
				PartsHashes: [][]byte{{0x1, 0x2}},
				Proposal:    *prop,
			},
			errors.New("invalid part hash height 1 round 0 index 0: expected size to be 32 bytes, got 2 bytes"),
		},
		{
			"too big of signature",
			&CompactBlock{
				BpHash:    rand.Bytes(tmhash.Size),
				Blobs:     []TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(types.MaxSignatureSize + 1),
				Proposal:  *prop,
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
			},
			wantErr: false,
		},
		{
			name: "invalid hash",
			part: PartMetaData{
				Index: 1,
				Hash:  []byte{0, 1, 2, 3},
			},
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
				{Index: 10, Hash: []byte("h100")},
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

func makeBlockID(hash []byte, partSetSize uint32, partSetHash []byte) types.BlockID {
	var (
		h   = make([]byte, tmhash.Size)
		psH = make([]byte, tmhash.Size)
	)
	copy(h, hash)
	copy(psH, partSetHash)
	return types.BlockID{
		Hash: h,
		PartSetHeader: types.PartSetHeader{
			Total: partSetSize,
			Hash:  psH,
		},
	}
}
