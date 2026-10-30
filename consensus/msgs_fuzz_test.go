package consensus

import (
	"bytes"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	cmtbits "github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// FuzzConsensusMessage follows the reactor's wire decode, unwrap and
// MsgFromProto path, including the block-part envelope absent from type fuzzing.
func FuzzConsensusMessage(f *testing.F) {
	parts, err := cmttypes.NewPartSetFromData([]byte("block part"), cmttypes.BlockPartSizeBytes)
	require.NoError(f, err)
	header := parts.Header()
	blockID := cmttypes.BlockID{Hash: bytes.Repeat([]byte{1}, 32), PartSetHeader: header}
	proposal := cmttypes.NewProposal(1, 0, -1, blockID)
	proposal.Timestamp = time.Unix(1704067200, 0).UTC()
	proposal.Signature = bytes.Repeat([]byte{2}, 64)
	vote := &cmttypes.Vote{
		Type: cmtproto.PrecommitType, Height: 1, Round: 0, BlockID: blockID,
		Timestamp: proposal.Timestamp, ValidatorAddress: bytes.Repeat([]byte{3}, 20),
		ValidatorIndex: 0, Signature: bytes.Repeat([]byte{4}, 64),
	}
	for _, seed := range []Message{
		&NewRoundStepMessage{Height: 1, Round: 0, Step: 1, LastCommitRound: -1},
		&NewValidBlockMessage{Height: 1, Round: 0, BlockPartSetHeader: header, BlockParts: parts.BitArray()},
		&ProposalMessage{Proposal: proposal},
		&ProposalPOLMessage{Height: 1, ProposalPOLRound: 0, ProposalPOL: cmtbits.NewBitArray(1)},
		&BlockPartMessage{Height: 1, Round: 0, Part: parts.GetPart(0)},
		&VoteMessage{Vote: vote},
		&HasVoteMessage{Height: 1, Round: 0, Type: cmtproto.PrecommitType, Index: 0},
		&VoteSetMaj23Message{Height: 1, Round: 0, Type: cmtproto.PrecommitType, BlockID: blockID},
		&VoteSetBitsMessage{Height: 1, Round: 0, Type: cmtproto.PrecommitType, BlockID: blockID, Votes: cmtbits.NewBitArray(1)},
	} {
		require.NoError(f, seed.ValidateBasic())
		pb, err := MsgToProto(seed)
		require.NoError(f, err)
		raw, err := proto.Marshal(pb.(p2p.Wrapper).Wrap())
		require.NoError(f, err)
		// Verify seeds reach conversion after the same envelope decode as peers.
		var envelope cmtcons.Message
		require.NoError(f, proto.Unmarshal(raw, &envelope))
		inner, err := envelope.Unwrap()
		require.NoError(f, err)
		_, err = MsgFromProto(inner)
		require.NoError(f, err)
		f.Add(raw)
	}
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxMsgSize {
			return
		}
		var envelope cmtcons.Message
		if err := proto.Unmarshal(raw, &envelope); err != nil {
			return
		}
		pb, err := envelope.Unwrap()
		if err != nil {
			return
		}
		msg, err := MsgFromProto(pb)
		if err != nil {
			return
		}
		require.NoError(t, msg.ValidateBasic())
		canonical, err := MsgToProto(msg)
		require.NoError(t, err)
		encoded, err := proto.Marshal(canonical.(p2p.Wrapper).Wrap())
		require.NoError(t, err)
		var decoded cmtcons.Message
		require.NoError(t, proto.Unmarshal(encoded, &decoded))
		inner, err := decoded.Unwrap()
		require.NoError(t, err)
		back, err := MsgFromProto(inner)
		require.NoError(t, err)
		again, err := MsgToProto(back)
		require.NoError(t, err)
		require.True(t, proto.Equal(canonical, again), "message changed across proto round trip")
	})
}
