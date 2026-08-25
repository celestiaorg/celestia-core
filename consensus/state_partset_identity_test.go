package consensus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtevents "github.com/cometbft/cometbft/libs/events"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
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

// TestHandleCompleteProposalDoesNotPromoteBlockFromDifferentPartSet checks
// that a completed proposal is not recorded as the valid block when the polka
// names the same hash but a different part set. This covers the case where the
// polka accumulated before our parts completed, so the addVote path never saw
// the two-thirds threshold cross.
func TestHandleCompleteProposalDoesNotPromoteBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)
	height := cs.rs.Height

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	// Keep handleCompleteProposal from stepping into prevote or commit; only
	// the Valid* update is under test.
	cs.rs.Step = cstypes.RoundStepPrevote

	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	cs.handleCompleteProposal(height)

	require.EqualValues(t, -1, cs.rs.ValidRound,
		"the POL names a different part set, so our block must not become the valid block")
	require.Nil(t, cs.rs.ValidBlock)
	require.Nil(t, cs.rs.ValidBlockParts)
}

// TestEnterPrecommitDoesNotLockBlockFromDifferentPartSet checks that a polka
// for the same hash but a different part set does not lock the proposal block:
// precommitting the polka's BlockID while holding a different part set splits
// later prevotes between two BlockIDs for one hash.
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
		"the polka names a different part set, so our block must not be locked")
	require.EqualValues(t, -1, cs.rs.LockedRound)
	require.Equal(t, pol.PartSetHeader, cs.rs.ProposalBlockParts.Header(),
		"we must be collecting the polka'd part set")
}

// TestEnterPrecommitDoesNotRelockBlockFromDifferentPartSet checks the relock
// path the same way: a lock whose parts differ from the polka's part set must
// be released, not renewed.
func TestEnterPrecommitDoesNotRelockBlockFromDifferentPartSet(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	cs.rs.LockedRound = round
	cs.rs.LockedBlock = block
	cs.rs.LockedBlockParts = parts

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
		"the polka names a different part set, so the lock must be released")
	require.EqualValues(t, -1, cs.rs.LockedRound)
}

// TestEnterCommitKeepsCommittedProposalOverAliasedLock checks that a lock on an
// aliased body does not clobber a proposal pair that matches the commit. Before
// this was handled, the locked pair was promoted on the hash alone and the
// guard below then discarded the committed parts we were collecting.
func TestEnterCommitKeepsCommittedProposalOverAliasedLock(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	// We locked on the aliased body, but hold the committed block and are
	// collecting the committed part set.
	cs.rs.LockedRound = round
	cs.rs.LockedBlock = alias
	cs.rs.LockedBlockParts = aliasParts
	cs.rs.ProposalBlock = block
	proposalParts := types.NewPartSetFromHeader(parts.Header(), types.BlockPartSizeBytes)
	cs.rs.ProposalBlockParts = proposalParts

	committed := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	for _, vote := range signVotes(cmtproto.PrecommitType, committed.Hash, committed.PartSetHeader, true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	require.NotPanics(t, func() { cs.enterCommit(height, round) })

	require.Same(t, block, cs.rs.ProposalBlock,
		"the proposal matches the commit, so the aliased lock must not replace it")
	require.Same(t, proposalParts, cs.rs.ProposalBlockParts,
		"the committed parts we were collecting must be kept")
}

// TestAliasedPolkaAnnouncesValidBlockOnce checks that a polka for a different
// part set fires EventValidBlock only for the vote that changed our state.
// Because the aliased case never advances ValidRound, every later prevote of
// the round re-enters the update and would otherwise re-broadcast
// NewValidBlock to every peer.
func TestAliasedPolkaAnnouncesValidBlockOnce(t *testing.T) {
	cs, vss := randState(4)
	// randState leaves the stub for our own validator at height 0; sign with
	// it too so a fourth prevote can arrive after the polka.
	vss[0].Height = cs.rs.Height

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts

	fired := 0
	require.NoError(t, cs.evsw.AddListenerForEvent("test", types.EventValidBlock,
		func(cmtevents.EventData) { fired++ }))

	pol := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for i, vote := range signVotes(cmtproto.PrevoteType, pol.Hash, pol.PartSetHeader, false, vss...) {
		added, err := cs.addVote(vote, "peer")
		require.NoError(t, err)
		require.True(t, added, "vote %d from validator %X", i, vote.ValidatorAddress)
	}

	require.Equal(t, 1, fired,
		"only the prevote that crossed two thirds changed our state, so only it may announce")
}

// TestEnterCommitClearsStaleBlockBeforeAnnouncing checks that when enterCommit
// drops a block that was not built from the committed part set, listeners of
// EventValidBlock never observe the stale block paired with the freshly reset
// part set.
func TestEnterCommitClearsStaleBlockBeforeAnnouncing(t *testing.T) {
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

	var observed *types.Block
	fired := 0
	require.NoError(t, cs.evsw.AddListenerForEvent("test", types.EventValidBlock,
		func(data cmtevents.EventData) {
			fired++
			observed = data.(*cstypes.RoundState).ProposalBlock
		}))

	committed := types.BlockID{Hash: alias.Hash(), PartSetHeader: aliasParts.Header()}
	for _, vote := range signVotes(cmtproto.PrecommitType, committed.Hash, committed.PartSetHeader, true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}

	require.NotPanics(t, func() { cs.enterCommit(height, round) })

	require.Equal(t, 1, fired)
	require.Nil(t, observed,
		"the block we held was not built from the committed part set, so listeners must not see it")
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
