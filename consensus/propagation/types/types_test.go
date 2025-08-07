package types

import (
	"errors"
	"testing"

	"github.com/cometbft/cometbft/libs/protoio"

	"github.com/cometbft/cometbft/proto/tendermint/crypto"
	protobits "github.com/cometbft/cometbft/proto/tendermint/libs/bits"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/rand"
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
			errors.New("TxMetaData: start 10 >= end 5"),
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

func TestSignBytes(t *testing.T) {
	block := CompactBlock{
		BpHash: []byte("block_part_hash"),
		Blobs: []TxMetaData{
			{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10},
			{Hash: rand.Bytes(tmhash.Size), Start: 5, End: 10},
		},
		Proposal: types.Proposal{
			Signature: []byte("proposal_signature"),
		},
		LastLen: 42,
		PartsHashes: [][]byte{
			[]byte("part1hash"),
			[]byte("part2hash"),
		},
	}

	// Compute expected sign bytes manually
	txMetaData := make([]*protoprop.TxMetaData, 0)
	for _, md := range block.Blobs {
		txMetaData = append(txMetaData, md.ToProto())
	}
	protoCompactBlock := &protoprop.CompactBlock{
		BpHash:      block.BpHash,
		Blobs:       txMetaData,
		Signature:   block.Signature,
		Proposal:    block.Proposal.ToProto(),
		LastLength:  block.LastLen,
		PartsHashes: block.PartsHashes,
	}
	expectedSignBytes, err := protoio.MarshalDelimited(protoCompactBlock)
	require.NoError(t, err)

	// Generate sign bytes from the function
	actualSignBytes, err := block.SignBytes()
	require.NoError(t, err)

	// Compare results
	require.Equal(t, expectedSignBytes, actualSignBytes)
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
			WantParts{Height: 1, Round: 1, Parts: &bits.BitArray{}, MissingPartsCount: 10},
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

func TestMsgFromProto_NilMessage(t *testing.T) {
	testCases := []struct {
		name        string
		msg         *protoprop.Message
		expectError string
	}{
		{
			name:        "nil message",
			msg:         nil,
			expectError: "propagation: nil message",
		},
		{
			name: "nil message: CompactBlock",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_CompactBlock{},
			},
			expectError: "propagation: nil compact block",
		},
		{
			name: "nil message fields: CompactBlock",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_CompactBlock{
					CompactBlock: &protoprop.CompactBlock{
						BpHash:      nil,
						Blobs:       nil,
						Signature:   nil,
						Proposal:    nil,
						LastLength:  0,
						PartsHashes: nil,
					},
				},
			},
			expectError: "nil proposal",
		},
		{
			name: "nil blob: CompactBlock",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_CompactBlock{
					CompactBlock: &protoprop.CompactBlock{
						Blobs: []*protoprop.TxMetaData{
							nil,
						},
					},
				},
			},
			expectError: "CompactBlock: nil blob",
		},
		{
			name: "nil message: HaveParts",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_HaveParts{},
			},
			expectError: "propagation: nil have parts",
		},
		{
			name: "nil message fields: HaveParts",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_HaveParts{
					HaveParts: &protoprop.HaveParts{
						Height: 0,
						Round:  0,
						Parts:  nil,
					},
				},
			},
			expectError: "HaveParts: Parts cannot be nil or empty",
		},
		{
			name: "nil part: HaveParts",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_HaveParts{
					HaveParts: &protoprop.HaveParts{
						Parts: []*protoprop.PartMetaData{nil},
					},
				},
			},
			expectError: "HaveParts: nil part at index",
		},
		{
			name: "nil message: WantParts",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_WantParts{},
			},
			expectError: "propagation: nil want parts",
		},
		{
			name: "nil message fields: WantParts",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_WantParts{
					WantParts: &protoprop.WantParts{
						Parts:  protobits.BitArray{},
						Height: 0,
						Round:  0,
						Prove:  false,
					},
				},
			},
			expectError: "WantParts: nil parts",
		},
		{
			name: "nil message: RecoveryPart",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_RecoveryPart{},
			},
			expectError: "propagation: nil recovery part",
		},
		{
			name: "nil message fields: RecoveryPart",
			msg: &protoprop.Message{
				Sum: &protoprop.Message_RecoveryPart{
					RecoveryPart: &protoprop.RecoveryPart{
						Height: 0,
						Round:  0,
						Index:  0,
						Data:   nil,
						Proof:  crypto.Proof{},
					},
				},
			},
			expectError: "Data cannot be nil or empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MsgFromProto(tc.msg)
			if tc.expectError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHaveParts_ValidatePartHashes(t *testing.T) {
	tests := []struct {
		name           string
		haveParts      HaveParts
		expectedHashes [][]byte
		wantErr        bool
		errMsg         string
	}{
		{
			name: "valid hashes",
			haveParts: HaveParts{
				Parts: []PartMetaData{
					{Index: 0, Hash: []byte("hash1")},
					{Index: 1, Hash: []byte("hash2")},
					{Index: 2, Hash: []byte("hash3")},
				},
			},
			expectedHashes: [][]byte{
				[]byte("hash1"),
				[]byte("hash2"),
				[]byte("hash3"),
			},
			wantErr: false,
		},
		{
			name: "invalid hash mismatch",
			haveParts: HaveParts{
				Parts: []PartMetaData{
					{Index: 0, Hash: []byte("hash1")},
					{Index: 1, Hash: []byte("invalid")},
					{Index: 2, Hash: []byte("hash3")},
				},
			},
			expectedHashes: [][]byte{
				[]byte("hash1"),
				[]byte("hash2"),
				[]byte("hash3"),
			},
			wantErr: true,
			errMsg:  "invalid part hash at index 1",
		},
		{
			name: "fewer expected hashes",
			haveParts: HaveParts{
				Parts: []PartMetaData{
					{Index: 0, Hash: []byte("hash1")},
					{Index: 1, Hash: []byte("hash2")},
					{Index: 2, Hash: []byte("hash3")},
				},
			},
			expectedHashes: [][]byte{
				[]byte("hash1"),
				[]byte("hash2"),
			},
			wantErr: true,
			errMsg:  "non existing part hash index 2",
		},
		{
			name: "no parts",
			haveParts: HaveParts{
				Parts: nil,
			},
			expectedHashes: [][]byte{},
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.haveParts.ValidatePartHashes(tt.expectedHashes)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAreOverlappingRanges(t *testing.T) {
	tests := []struct {
		name    string
		blobs   []TxMetaData
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no blobs",
			blobs:   []TxMetaData{},
			wantErr: false,
		},
		{
			name: "non-overlapping ranges",
			blobs: []TxMetaData{
				{Start: 0, End: 10},
				{Start: 10, End: 20},
				{Start: 20, End: 30},
			},
			wantErr: false,
		},
		{
			name: "overlapping ranges",
			blobs: []TxMetaData{
				{Start: 0, End: 10},
				{Start: 5, End: 15},
				{Start: 15, End: 25},
			},
			wantErr: true,
			errMsg:  "overlapping tx metadata ranges: 0:[0-10) and 1:[5-15)",
		},
		{
			name: "single blob",
			blobs: []TxMetaData{
				{Start: 0, End: 10},
			},
			wantErr: false,
		},
		{
			name: "unsorted input with overlapping ranges",
			blobs: []TxMetaData{
				{Start: 10, End: 20},
				{Start: 5, End: 15},
			},
			wantErr: true,
			errMsg:  "overlapping tx metadata ranges: 0:[5-15) and 1:[10-20)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := hasOverlappingRanges(tt.blobs)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRecoveryPart_ValidateBasic(t *testing.T) {
	tests := []struct {
		name        string
		recovery    *RecoveryPart
		expectError string
	}{
		{
			name:        "nil recovery part",
			recovery:    nil,
			expectError: "propagation: nil recovery part",
		},
		{
			name: "valid recovery part with proof",
			recovery: &RecoveryPart{
				Height: 10,
				Round:  1,
				Index:  2,
				Data:   []byte("valid-data"),
				Proof: &merkle.Proof{
					LeafHash: merkle.LeafHash([]byte("valid-data")),
				},
			},
			expectError: "",
		},
		{
			name: "negative height",
			recovery: &RecoveryPart{
				Height: -1,
				Round:  1,
				Index:  0,
				Data:   []byte("data"),
			},
			expectError: "RecoveryPart: Height and Round cannot be negative",
		},
		{
			name: "negative round",
			recovery: &RecoveryPart{
				Height: 1,
				Round:  -1,
				Index:  0,
				Data:   []byte("data"),
			},
			expectError: "RecoveryPart: Height and Round cannot be negative",
		},
		{
			name: "empty data",
			recovery: &RecoveryPart{
				Height: 1,
				Round:  1,
				Index:  0,
				Data:   []byte{},
			},
			expectError: "RecoveryPart: Data cannot be nil or empty",
		},
		{
			name: "nil data",
			recovery: &RecoveryPart{
				Height: 1,
				Round:  1,
				Index:  0,
				Data:   nil,
			},
			expectError: "RecoveryPart: Data cannot be nil or empty",
		},
		{
			name: "proof with invalid structure",
			recovery: &RecoveryPart{
				Height: 1,
				Round:  1,
				Index:  0,
				Data:   []byte("data"),
				Proof:  &merkle.Proof{},
			},
			expectError: "RecoveryPart: invalid proof",
		},
		{
			name: "proof with mismatched leaf hash",
			recovery: &RecoveryPart{
				Height: 1,
				Round:  1,
				Index:  0,
				Data:   []byte("data"),
				Proof: &merkle.Proof{
					LeafHash: merkle.LeafHash([]byte("invalid")),
				},
			},
			expectError: "RecoveryPart: invalid proof leaf hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.recovery.ValidateBasic()
			if tt.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
			} else {
				assert.NoError(t, err)
			}
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
