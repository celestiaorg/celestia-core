package consensus

import (
	"context"
	"testing"

	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

// aliasBlock returns a copy of block whose Data.SquareSize differs but whose
// header, and therefore block hash, is identical. Data.Hash() does not cover
// SquareSize while Data.ToProto() serializes it, so the copy has the same block
// hash and a different PartSetHeader.
func aliasBlock(t *testing.T, block *types.Block) *types.Block {
	t.Helper()

	data := types.NewData(block.Txs, block.SquareSize+(uint64(1)<<63), block.DataHash)
	alias := types.MakeBlock(block.Height, data, block.LastCommit, block.Evidence.Evidence)
	alias.Header = block.Header

	require.Equal(t, block.Hash(), alias.Hash(), "alias must share the block hash")
	return alias
}

// TestBlockHashDoesNotDeterminePartSetHeader documents the property the checks
// below defend against: a block hash does not uniquely determine the serialized
// body, so BlockID.Hash matching is not proof that the part set matches too.
func TestBlockHashDoesNotDeterminePartSetHeader(t *testing.T) {
	cs, _ := randState(1)

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	require.NoError(t, alias.ValidateBasic())
	require.NotEqual(t, parts.Header(), aliasParts.Header(),
		"the two bodies must have distinct part set headers")
}

// TestEnterCommitDropsBlockFromDifferentPartSet checks that a node holding a
// block that hashes to the committed BlockID, but which was built from a
// different part set, drops that block and waits for the committed parts
// instead of finalizing. Before this was handled, enterCommit left
// ProposalBlock paired with a part set the commit did not refer to and
// finalizeCommit panicked.
func TestEnterCommitDropsBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	// We hold the block built from our own part set.
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts

	// The rest of the network commits the aliased body: same hash, different
	// part set header.
	committed := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrecommitType, committed.Hash, committed.PartSetHeader, true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	require.NotPanics(t, func() { cs.enterCommit(height, round) })

	require.Nil(t, cs.rs.ProposalBlock,
		"the block we held was not built from the committed part set, so it must be dropped")
	require.Equal(t, committed.PartSetHeader, cs.rs.ProposalBlockParts.Header(),
		"we must be collecting the committed part set")
	require.False(t, cs.rs.ProposalBlockParts.IsComplete())
}

// TestTryFinalizeCommitWaitsForCompletePartSet checks that tryFinalizeCommit
// declines to finalize when the part set matches the commit but is not yet
// complete. finalizeCommit would otherwise hand an incomplete part set to the
// block store, which panics.
func TestTryFinalizeCommitWaitsForCompletePartSet(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	// We hold the committed block, but only an empty part set for it.
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(parts.Header(), types.BlockPartSizeBytes)
	cs.rs.Step = cstypes.RoundStepCommit
	cs.rs.CommitRound = round

	committed := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	for _, vote := range signVotes(cmtproto.PrecommitType, committed.Hash, committed.PartSetHeader, true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	require.NotPanics(t, func() { cs.tryFinalizeCommit(height) })

	require.Equal(t, height, cs.rs.Height, "must not have advanced past the height")
}
