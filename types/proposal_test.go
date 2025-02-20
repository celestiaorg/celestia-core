package types

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/protoio"
	"github.com/tendermint/tendermint/libs/rand"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

var (
	testProposal *Proposal
	pbp          *cmtproto.Proposal
)

func init() {
	var stamp, err = time.Parse(TimeFormat, "2018-02-11T07:09:22.765Z")
	if err != nil {
		panic(err)
	}
	testProposal = &Proposal{
		Height: 12345,
		Round:  23456,
		BlockID: BlockID{Hash: []byte("--June_15_2020_amino_was_removed"),
			PartSetHeader: PartSetHeader{Total: 111, Hash: []byte("--June_15_2020_amino_was_removed")}},
		POLRound:  -1,
		Timestamp: stamp,
	}
	pbp = testProposal.ToProto()
}

func TestProposalSignable(t *testing.T) {
	chainID := "test_chain_id"
	signBytes := ProposalSignBytes(chainID, pbp)
	pb := CanonicalizeProposal(chainID, pbp)

	expected, err := protoio.MarshalDelimited(&pb)
	require.NoError(t, err)
	require.Equal(t, expected, signBytes, "Got unexpected sign bytes for Proposal")
}

func TestProposalString(t *testing.T) {
	str := testProposal.String()
	expected := `Proposal{12345/23456 (2D2D4A756E655F31355F323032305F616D696E6F5F7761735F72656D6F766564:111:2D2D4A756E65, -1) 000000000000 @ 2018-02-11T07:09:22.765Z}` //nolint:lll // ignore line length for tests
	if str != expected {
		t.Errorf("got unexpected string for Proposal. Expected:\n%v\nGot:\n%v", expected, str)
	}
}

func TestProposalVerifySignature(t *testing.T) {
	privVal := NewMockPV()
	pubKey, err := privVal.GetPubKey()
	require.NoError(t, err)

	h, r := int64(4), int32(2)
	compB, err := NewCompactBlock(h, r, &PartSet{hash: rand.Bytes(32)}, []*TxMetaData{})
	assert.NoError(t, err)

	prop := NewProposal(
		4, 2, 2,
		BlockID{cmtrand.Bytes(tmhash.Size), PartSetHeader{777, cmtrand.Bytes(tmhash.Size)}},
		compB,
	)
	p := prop.ToProto()
	signBytes := ProposalSignBytes("test_chain_id", p)

	// sign it
	err = privVal.SignProposal("test_chain_id", p)
	require.NoError(t, err)
	prop.Signature = p.Signature

	// verify the same proposal
	valid := pubKey.VerifySignature(signBytes, prop.Signature)
	require.True(t, valid)

	// serialize, deserialize and verify again....
	newProp := new(cmtproto.Proposal)
	pb := prop.ToProto()

	bs, err := proto.Marshal(pb)
	require.NoError(t, err)

	err = proto.Unmarshal(bs, newProp)
	require.NoError(t, err)

	np, err := ProposalFromProto(newProp)
	require.NoError(t, err)

	// verify the transmitted proposal
	newSignBytes := ProposalSignBytes("test_chain_id", pb)
	require.Equal(t, string(signBytes), string(newSignBytes))
	valid = pubKey.VerifySignature(newSignBytes, np.Signature)
	require.True(t, valid)
}

func BenchmarkProposalWriteSignBytes(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ProposalSignBytes("test_chain_id", pbp)
	}
}

func BenchmarkProposalSign(b *testing.B) {
	privVal := NewMockPV()
	for i := 0; i < b.N; i++ {
		err := privVal.SignProposal("test_chain_id", pbp)
		if err != nil {
			b.Error(err)
		}
	}
}

func BenchmarkProposalVerifySignature(b *testing.B) {
	privVal := NewMockPV()
	err := privVal.SignProposal("test_chain_id", pbp)
	require.NoError(b, err)
	pubKey, err := privVal.GetPubKey()
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		pubKey.VerifySignature(ProposalSignBytes("test_chain_id", pbp), testProposal.Signature)
	}
}

func TestProposalValidateBasic(t *testing.T) {

	privVal := NewMockPV()
	testCases := []struct {
		testName         string
		malleateProposal func(*Proposal)
		expectErr        bool
	}{
		{"Good Proposal", func(p *Proposal) {}, false},
		{"Invalid Type", func(p *Proposal) { p.Type = cmtproto.PrecommitType }, true},
		{"Invalid Height", func(p *Proposal) { p.Height = -1 }, true},
		{"Invalid Round", func(p *Proposal) { p.Round = -1 }, true},
		{"Invalid POLRound", func(p *Proposal) { p.POLRound = -2 }, true},
		{"Invalid BlockId", func(p *Proposal) {
			p.BlockID = BlockID{[]byte{1, 2, 3}, PartSetHeader{111, []byte("blockparts")}}
		}, true},
		{"Invalid Signature", func(p *Proposal) {
			p.Signature = make([]byte, 0)
		}, true},
		{"Too big Signature", func(p *Proposal) {
			p.Signature = make([]byte, MaxSignatureSize+1)
		}, true},
	}
	blockID := makeBlockID(tmhash.Sum([]byte("blockhash")), math.MaxInt32, tmhash.Sum([]byte("partshash")))

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			h, r := int64(4), int32(2)
			compB, err := NewCompactBlock(h, r, &PartSet{hash: rand.Bytes(32)}, []*TxMetaData{})
			require.NoError(t, err)
			prop := NewProposal(
				h, r, r,
				blockID,
				compB,
			)
			p := prop.ToProto()
			err = privVal.SignProposal("test_chain_id", p)
			prop.Signature = p.Signature
			require.NoError(t, err)
			tc.malleateProposal(prop)
			assert.Equal(t, tc.expectErr, prop.ValidateBasic() != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestProposalProtoBuf(t *testing.T) {
	h, r := int64(1), int32(2)
	compB, err := NewCompactBlock(h, r, &PartSet{hash: rand.Bytes(32)}, []*TxMetaData{})
	compB.Signature = rand.Bytes(64)
	assert.NoError(t, err)
	proposal := NewProposal(1, 2, 3, makeBlockID([]byte("hash"), 2, []byte("part_set_hash")), compB)
	proposal.Signature = []byte("sig")
	proposal2 := NewProposal(1, 2, 3, BlockID{}, compB)

	testCases := []struct {
		msg     string
		p1      *Proposal
		expPass bool
	}{
		{"success", proposal, true},
		{"success", proposal2, false}, // blcokID cannot be empty
		{"empty proposal failure validatebasic", &Proposal{}, false},
		{"nil proposal", nil, false},
	}
	for _, tc := range testCases {
		protoProposal := tc.p1.ToProto()

		p, err := ProposalFromProto(protoProposal)
		if tc.expPass {
			require.NoError(t, err)
			require.Equal(t, tc.p1, p, tc.msg)
		} else {
			require.Error(t, err)
		}
	}
}

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
				Signature: rand.Bytes(MaxSignatureSize),
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
				Signature: rand.Bytes(MaxSignatureSize),
			},
			nil,
		},
		{
			"negative height",
			&CompactBlock{
				Height: -1, Round: 1, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(MaxSignatureSize),
			},
			errors.New("CompactBlock: Height cannot be negative"),
		},
		{
			"negative round",
			&CompactBlock{
				Height: 1, Round: -2, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(MaxSignatureSize),
			},
			errors.New("CompactBlock: Round cannot be negative"),
		},
		{
			"invalid bp_hash",
			&CompactBlock{
				Height: 1, Round: 1, BpHash: rand.Bytes(tmhash.Size + 1),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(MaxSignatureSize),
			},
			errors.New("expected size to be 32 bytes, got 33 bytes"),
		},
		{
			"too big of signature",
			&CompactBlock{
				Height: 1, Round: 1, BpHash: rand.Bytes(tmhash.Size),
				Blobs:     []*TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
				Signature: rand.Bytes(MaxSignatureSize + 1),
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
