package consensus

import (
	"encoding/hex"
	"math"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/crypto"
	"github.com/lazyledger/lazyledger-core/libs/bits"
	tmrand "github.com/lazyledger/lazyledger-core/libs/rand"
	"github.com/lazyledger/lazyledger-core/p2p"
	tmcons "github.com/lazyledger/lazyledger-core/proto/tendermint/consensus"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	"github.com/lazyledger/lazyledger-core/types"
)

func TestMsgToProto(t *testing.T) {
	bi := types.BlockID{
		Hash:          tmrand.Bytes(32),
	}
	pbBi := bi.ToProto()
	bits := bits.NewBitArray(1)
	pbBits := bits.ToProto()

	block := types.MakeBlock(
		int64(3),
		[]types.Tx{types.Tx("Hello World")},
		nil,
		nil,
		types.Messages{},
		&types.Commit{Signatures: []types.CommitSig{}},
	)
	block.ProposerAddress = tmrand.Bytes(crypto.AddressSize)
	pbBlock, err := block.ToProto()
	require.NoError(t, err)

	dah := &block.DataAvailabilityHeader
	pbDah := dah.ToProto()

	proposal := types.Proposal{
		Type:      tmproto.ProposalType,
		Height:    1,
		Round:     1,
		POLRound:  1,
		DAHeader:  dah,
		Timestamp: time.Now(),
		Signature: tmrand.Bytes(20),
	}
	pbProposal := proposal.ToProto()

	pv := types.NewMockPV()
	pk, err := pv.GetPubKey()
	require.NoError(t, err)
	val := types.NewValidator(pk, 100)

	vote, err := types.MakeVote(
		1, types.BlockID{}, &types.ValidatorSet{Proposer: val, Validators: []*types.Validator{val}},
		pv, "chainID", time.Now())
	require.NoError(t, err)
	pbVote := vote.ToProto()

	testsCases := []struct {
		testName string
		msg      Message
		want     *tmcons.Message
		wantErr  bool
	}{
		{"successful NewRoundStepMessage", &NewRoundStepMessage{
			Height:                2,
			Round:                 1,
			Step:                  1,
			SecondsSinceStartTime: 1,
			LastCommitRound:       2,
		}, &tmcons.Message{
			Sum: &tmcons.Message_NewRoundStep{
				NewRoundStep: &tmcons.NewRoundStep{
					Height:                2,
					Round:                 1,
					Step:                  1,
					SecondsSinceStartTime: 1,
					LastCommitRound:       2,
				},
			},
		}, false},

		{"successful NewValidBlockMessage", &NewValidBlockMessage{
			Height:             1,
			Round:              1,
			BlockDAHeader: 		dah,
			IsCommit:           false,
		}, &tmcons.Message{
			Sum: &tmcons.Message_NewValidBlock{
				NewValidBlock: &tmcons.NewValidBlock{
					Height:             1,
					Round:              1,
					DAHeader:           pbDah,
					IsCommit:           false,
				},
			},
		}, false},
		{"successful BlockPartMessage", &BlockMessage{
			Height: 100,
			Round:  1,
			Block: 	block,
		}, &tmcons.Message{
			Sum: &tmcons.Message_Block{
				Block: &tmcons.Block{
					Height: 100,
					Round:  1,
					Block:  pbBlock,
				},
			},
		}, false},
		{"successful ProposalPOLMessage", &ProposalPOLMessage{
			Height:           1,
			ProposalPOLRound: 1,
			ProposalPOL:      bits,
		}, &tmcons.Message{
			Sum: &tmcons.Message_ProposalPol{
				ProposalPol: &tmcons.ProposalPOL{
					Height:           1,
					ProposalPolRound: 1,
					ProposalPol:      *pbBits,
				},
			}}, false},
		{"successful ProposalMessage", &ProposalMessage{
			Proposal: &proposal,
		}, &tmcons.Message{
			Sum: &tmcons.Message_Proposal{
				Proposal: &tmcons.Proposal{
					Proposal: *pbProposal,
				},
			},
		}, false},
		{"successful VoteMessage", &VoteMessage{
			Vote: vote,
		}, &tmcons.Message{
			Sum: &tmcons.Message_Vote{
				Vote: &tmcons.Vote{
					Vote: pbVote,
				},
			},
		}, false},
		{"successful VoteSetMaj23", &VoteSetMaj23Message{
			Height:  1,
			Round:   1,
			Type:    1,
			BlockID: bi,
		}, &tmcons.Message{
			Sum: &tmcons.Message_VoteSetMaj23{
				VoteSetMaj23: &tmcons.VoteSetMaj23{
					Height:  1,
					Round:   1,
					Type:    1,
					BlockID: pbBi,
				},
			},
		}, false},
		{"successful VoteSetBits", &VoteSetBitsMessage{
			Height:  1,
			Round:   1,
			Type:    1,
			BlockID: bi,
			Votes:   bits,
		}, &tmcons.Message{
			Sum: &tmcons.Message_VoteSetBits{
				VoteSetBits: &tmcons.VoteSetBits{
					Height:  1,
					Round:   1,
					Type:    1,
					BlockID: pbBi,
					Votes:   *pbBits,
				},
			},
		}, false},
		{"failure", nil, &tmcons.Message{}, true},
	}
	for _, tt := range testsCases {
		tt := tt
		t.Run(tt.testName, func(t *testing.T) {
			pb, err := MsgToProto(tt.msg)
			if tt.wantErr == true {
				assert.Equal(t, err != nil, tt.wantErr)
				return
			}
			assert.EqualValues(t, tt.want, pb, tt.testName)

			msg, err := MsgFromProto(pb)

			if !tt.wantErr {
				require.NoError(t, err)
				bcm := assert.Equal(t, tt.msg, msg, tt.testName)
				assert.True(t, bcm, tt.testName)
			} else {
				require.Error(t, err, tt.testName)
			}
		})
	}
}

func TestWALMsgProto(t *testing.T) {
	block := types.MakeBlock(
		int64(3),
		[]types.Tx{types.Tx("Hello World")},
		nil,
		nil,
		types.Messages{},
		&types.Commit{Signatures: []types.CommitSig{}},
	)
	block.ProposerAddress = tmrand.Bytes(crypto.AddressSize)

	pbBlock, err := block.ToProto()
	require.NoError(t, err)

	testsCases := []struct {
		testName string
		msg      WALMessage
		want     *tmcons.WALMessage
		wantErr  bool
	}{
		{"successful EventDataRoundState", types.EventDataRoundState{
			Height: 2,
			Round:  1,
			Step:   "ronies",
		}, &tmcons.WALMessage{
			Sum: &tmcons.WALMessage_EventDataRoundState{
				EventDataRoundState: &tmproto.EventDataRoundState{
					Height: 2,
					Round:  1,
					Step:   "ronies",
				},
			},
		}, false},
		{"successful msgInfo", msgInfo{
			Msg: &BlockMessage{
				Height: 100,
				Round:  1,
				Block:  block,
			},
			PeerID: p2p.ID("string"),
		}, &tmcons.WALMessage{
			Sum: &tmcons.WALMessage_MsgInfo{
				MsgInfo: &tmcons.MsgInfo{
					Msg: tmcons.Message{
						Sum: &tmcons.Message_Block{
							Block: &tmcons.Block{
								Height: 100,
								Round:  1,
								Block:  pbBlock,
							},
						},
					},
					PeerID: "string",
				},
			},
		}, false},
		{"successful timeoutInfo", timeoutInfo{
			Duration: time.Duration(100),
			Height:   1,
			Round:    1,
			Step:     1,
		}, &tmcons.WALMessage{
			Sum: &tmcons.WALMessage_TimeoutInfo{
				TimeoutInfo: &tmcons.TimeoutInfo{
					Duration: time.Duration(100),
					Height:   1,
					Round:    1,
					Step:     1,
				},
			},
		}, false},
		{"successful EndHeightMessage", EndHeightMessage{
			Height: 1,
		}, &tmcons.WALMessage{
			Sum: &tmcons.WALMessage_EndHeight{
				EndHeight: &tmcons.EndHeight{
					Height: 1,
				},
			},
		}, false},
		{"failure", nil, &tmcons.WALMessage{}, true},
	}
	for _, tt := range testsCases {
		tt := tt
		t.Run(tt.testName, func(t *testing.T) {
			pb, err := WALToProto(tt.msg)
			if tt.wantErr == true {
				assert.Equal(t, err != nil, tt.wantErr)
				return
			}
			assert.EqualValues(t, tt.want, pb, tt.testName)

			msg, err := WALFromProto(pb)

			if !tt.wantErr {
				require.NoError(t, err)
				assert.EqualValues(t, tt.msg, msg, tt.testName) // need the concrete type as WAL Message is a empty interface
			} else {
				require.Error(t, err, tt.testName)
			}
		})
	}
}

// nolint:lll //ignore line length for tests
func TestConsMsgsVectors(t *testing.T) {
	date := time.Date(2018, 8, 30, 12, 0, 0, 0, time.UTC)
	psh := types.PartSetHeader{
		Total: 1,
		Hash:  []byte("add_more_exclamation_marks_code-"),
	}

	bi := types.BlockID{
		Hash:          []byte("add_more_exclamation_marks_code-"),
		PartSetHeader: psh,
	}
	pbBi := bi.ToProto()
	bits := bits.NewBitArray(1)
	pbBits := bits.ToProto()

	block := types.MakeBlock(
		int64(3),
		[]types.Tx{types.Tx("Hello World")},
		nil,
		nil,
		types.Messages{},
		nil,
	)
	pbBlock, err := block.ToProto()
	require.NoError(t, err)

	dah := &block.DataAvailabilityHeader
	pbDah := dah.ToProto()

	proposal := types.Proposal{
		Type:      tmproto.ProposalType,
		Height:    1,
		Round:     1,
		POLRound:  1,
		DAHeader:  dah,
		Timestamp: date,
		Signature: []byte("add_more_exclamation"),
	}
	pbProposal := proposal.ToProto()

	v := &types.Vote{
		ValidatorAddress: []byte("add_more_exclamation"),
		ValidatorIndex:   1,
		Height:           1,
		Round:            0,
		Timestamp:        date,
		Type:             tmproto.PrecommitType,
		BlockID:          bi,
	}
	vpb := v.ToProto()

	testCases := []struct {
		testName string
		cMsg     proto.Message
		expBytes string
	}{
		{"NewRoundStep", &tmcons.Message{Sum: &tmcons.Message_NewRoundStep{NewRoundStep: &tmcons.NewRoundStep{
			Height:                1,
			Round:                 1,
			Step:                  1,
			SecondsSinceStartTime: 1,
			LastCommitRound:       1,
		}}}, "0a0a08011001180120012801"},
		{"NewRoundStep Max", &tmcons.Message{Sum: &tmcons.Message_NewRoundStep{NewRoundStep: &tmcons.NewRoundStep{
			Height:                math.MaxInt64,
			Round:                 math.MaxInt32,
			Step:                  math.MaxUint32,
			SecondsSinceStartTime: math.MaxInt64,
			LastCommitRound:       math.MaxInt32,
		}}}, "0a2608ffffffffffffffff7f10ffffffff0718ffffffff0f20ffffffffffffffff7f28ffffffff07"},
		{"NewValidBlock", &tmcons.Message{Sum: &tmcons.Message_NewValidBlock{
			NewValidBlock: &tmcons.NewValidBlock{
				Height: 1, Round: 1, DAHeader: pbDah, IsCommit: false}}},
			"12cf01080110011ac8010a300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d0a30ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e812300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d1230ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e8"},
		{"Proposal", &tmcons.Message{Sum: &tmcons.Message_Proposal{Proposal: &tmcons.Proposal{Proposal: *pbProposal}}},
			"1af4010af10108201001180120012ac8010a300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d0a30ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e812300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d1230ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e8320608c0b89fdc053a146164645f6d6f72655f6578636c616d6174696f6e"},
		{"ProposalPol", &tmcons.Message{Sum: &tmcons.Message_ProposalPol{
			ProposalPol: &tmcons.ProposalPOL{Height: 1, ProposalPolRound: 1}}},
			"2206080110011a00"},
		{"Blocks", &tmcons.Message{Sum: &tmcons.Message_Block{
			Block: &tmcons.Block{Height: 1, Round: 1, Block: pbBlock}}},
			"52c402080110011abd020a5b0a02080b1803220b088092b8c398feffffff012a0212003a20ba28ef83fed712be8a128587469a4effda9f4f4895bd5638dab1f10775c9bed66a20e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85512130a0b48656c6c6f20576f726c6412001a0022001ac8010a300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d0a30ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e812300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d1230ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e8"},
		{"Vote", &tmcons.Message{Sum: &tmcons.Message_Vote{
			Vote: &tmcons.Vote{Vote: vpb}}},
			"32720a700802100122480a206164645f6d6f72655f6578636c616d6174696f6e5f6d61726b735f636f64652d1224080112206164645f6d6f72655f6578636c616d6174696f6e5f6d61726b735f636f64652d2a00320608c0b89fdc053a146164645f6d6f72655f6578636c616d6174696f6e4001"},
		{"HasVote", &tmcons.Message{Sum: &tmcons.Message_HasVote{
			HasVote: &tmcons.HasVote{Height: 1, Round: 1, Type: tmproto.PrevoteType, Index: 1}}},
			"3a080801100118012001"},
		{"HasVote", &tmcons.Message{Sum: &tmcons.Message_HasVote{
			HasVote: &tmcons.HasVote{Height: math.MaxInt64, Round: math.MaxInt32,
				Type: tmproto.PrevoteType, Index: math.MaxInt32}}},
			"3a1808ffffffffffffffff7f10ffffffff07180120ffffffff07"},
		{"VoteSetMaj23", &tmcons.Message{Sum: &tmcons.Message_VoteSetMaj23{
			VoteSetMaj23: &tmcons.VoteSetMaj23{Height: 1, Round: 1, Type: tmproto.PrevoteType, BlockID: pbBi}}},
			"425008011001180122480a206164645f6d6f72655f6578636c616d6174696f6e5f6d61726b735f636f64652d1224080112206164645f6d6f72655f6578636c616d6174696f6e5f6d61726b735f636f64652d"},
		{"VoteSetBits", &tmcons.Message{Sum: &tmcons.Message_VoteSetBits{
			VoteSetBits: &tmcons.VoteSetBits{Height: 1, Round: 1, Type: tmproto.PrevoteType, BlockID: pbBi, Votes: *pbBits}}},
			"4a5708011001180122480a206164645f6d6f72655f6578636c616d6174696f6e5f6d61726b735f636f64652d1224080112206164645f6d6f72655f6578636c616d6174696f6e5f6d61726b735f636f64652d2a050801120100"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			bz, err := proto.Marshal(tc.cMsg)
			require.NoError(t, err)

			require.Equal(t, tc.expBytes, hex.EncodeToString(bz))
		})
	}
}
