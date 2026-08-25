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

// TestPOLDoesNotPromoteBlockFromDifferentPartSet checks that a proposal is not
// recorded as the valid block unless both halves of its BlockID match the POL.
func TestPOLDoesNotPromoteBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts

	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss[1:]...) {
		added, err := cs.addVote(vote, "peer")
		require.NoError(t, err)
		require.True(t, added)
	}

	require.EqualValues(t, -1, cs.rs.ValidRound)
	require.Nil(t, cs.rs.ValidBlock)
	require.Nil(t, cs.rs.ValidBlockParts)
	require.Nil(t, cs.rs.ProposalBlock)
	require.Equal(t, pol.PartSetHeader, cs.rs.ProposalBlockParts.Header())
}

// TestHandleCompleteProposalDoesNotPromoteBlockFromDifferentPartSet checks the
// parts-arrive-after-polka path: when a completed proposal hashes to a
// pre-existing POL but was built from a different part set, it must not be
// recorded as the valid block.
func TestHandleCompleteProposalDoesNotPromoteBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)
	height := cs.rs.Height

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	// We hold a complete proposal built from our own part set, past the
	// propose step so handleCompleteProposal only runs the Valid* update.
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	cs.rs.Step = cstypes.RoundStepPrevote

	// A polka for the aliased body already exists at this round; add the votes
	// to the vote set directly so addVote's own defenses are not in play.
	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	cs.handleCompleteProposal(height)

	require.EqualValues(t, -1, cs.rs.ValidRound)
	require.Nil(t, cs.rs.ValidBlock)
	require.Nil(t, cs.rs.ValidBlockParts)
}

// TestEnterPrecommitDoesNotLockBlockFromDifferentPartSet checks that a polka
// for a block that hashes to our proposal but was built from a different part
// set does not lock the proposal: the node must precommit nil, drop the
// mismatched body, and start collecting the polka's part set.
func TestEnterPrecommitDoesNotLockBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	cs.rs.Step = cstypes.RoundStepPrevote

	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	cs.lockAll()
	cs.enterPrecommit(height, round)
	cs.unlockAll()

	require.Nil(t, cs.rs.LockedBlock,
		"must not lock a block built from a different part set than the polka")
	require.EqualValues(t, -1, cs.rs.LockedRound)
	require.Nil(t, cs.rs.ProposalBlock)
	require.Equal(t, pol.PartSetHeader, cs.rs.ProposalBlockParts.Header(),
		"we must be collecting the polka's part set")
}

// TestEnterPrecommitDoesNotRelockBlockFromDifferentPartSet checks that a polka
// for a block that hashes to our locked block but was built from a different
// part set does not relock: relocking would keep a part set the precommit's
// BlockID does not refer to.
func TestEnterPrecommitDoesNotRelockBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	// We are locked on the body built from our own part set.
	cs.rs.LockedRound = round
	cs.rs.LockedBlock = block
	cs.rs.LockedBlockParts = parts
	cs.rs.Step = cstypes.RoundStepPrevote

	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	cs.lockAll()
	cs.enterPrecommit(height, round)
	cs.unlockAll()

	require.Nil(t, cs.rs.LockedBlock,
		"must not stay locked on a block built from a different part set than the polka")
	require.EqualValues(t, -1, cs.rs.LockedRound)
	require.Equal(t, pol.PartSetHeader, cs.rs.ProposalBlockParts.Header(),
		"we must be collecting the polka's part set")
}

// TestPOLUnlocksBlockFromDifferentPartSet checks that a later-round polka for
// the same block hash but a different part set unlocks: the locked identity is
// not the one the polka endorses, so staying locked would keep the node
// prevoting a BlockID no polka backs.
func TestPOLUnlocksBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	// Locked in round 0 on the body built from our own part set; the node has
	// since moved to round 1.
	cs.rs.LockedRound = 0
	cs.rs.LockedBlock = block
	cs.rs.LockedBlockParts = parts
	cs.rs.Round = 1

	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vs := range vss[1:] {
		vs.Round = 1
	}
	for _, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss[1:]...) {
		added, err := cs.addVote(vote, "peer")
		require.NoError(t, err)
		require.True(t, added)
	}

	require.Nil(t, cs.rs.LockedBlock,
		"a POL for the same hash but a different part set must unlock")
	require.EqualValues(t, -1, cs.rs.LockedRound)
	require.Nil(t, cs.rs.LockedBlockParts)
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
