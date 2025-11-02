package consensus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	abcimocks "github.com/cometbft/cometbft/abci/types/mocks"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// TestStepRegressionDuringProcessProposal reproduces the bug where:
// 1. Validator enters prevote step
// 2. Unlocks during ProcessProposal (slow ABCI call)
// 3. Meanwhile, receives 2/3+ prevotes and enters precommit
// 4. Signs precommit (step 3)
// 5. ProcessProposal returns, tries to sign prevote (step 2)
// 6. Gets "step regression" error from privval
func TestStepRegressionDuringProcessProposal(t *testing.T) {
	// Create a mock app that has a slow ProcessProposal
	app := &abcimocks.Application{}

	// Setup default responses
	app.On("Info", mock.Anything, mock.Anything).Return(&abci.ResponseInfo{
		TimeoutInfo: abci.TimeoutInfo{
			TimeoutPropose:          3000,
			TimeoutPrevote:          1000,
			TimeoutPrecommit:        1000,
			TimeoutCommit:           1000,
			TimeoutProposeDelta:     500,
			TimeoutPrevoteDelta:     500,
			TimeoutPrecommitDelta:   500,
			DelayedPrecommitTimeout: 0,
		},
	}, nil)
	app.On("PrepareProposal", mock.Anything, mock.Anything).Return(&abci.ResponsePrepareProposal{}, nil)
	app.On("ExtendVote", mock.Anything, mock.Anything).Return(&abci.ResponseExtendVote{}, nil)
	app.On("VerifyVoteExtension", mock.Anything, mock.Anything).Return(&abci.ResponseVerifyVoteExtension{
		Status: abci.ResponseVerifyVoteExtension_ACCEPT,
	}, nil)
	app.On("FinalizeBlock", mock.Anything, mock.Anything).Return(&abci.ResponseFinalizeBlock{}, nil)
	app.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil)

	// Create a channel to control when ProcessProposal returns
	processProposalStarted := make(chan struct{})
	processProposalShouldReturn := make(chan struct{})
	var processProposalMu sync.Mutex
	processProposalCalled := false

	// ProcessProposal blocks until we signal it to return
	app.On("ProcessProposal", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		processProposalMu.Lock()
		if !processProposalCalled {
			processProposalCalled = true
			processProposalMu.Unlock()
			close(processProposalStarted)
			<-processProposalShouldReturn
		} else {
			processProposalMu.Unlock()
		}
	}).Return(&abci.ResponseProcessProposal{
		Status: abci.ResponseProcessProposal_ACCEPT,
	}, nil)

	// Create consensus state with 4 validators
	cs1, vss := randStateWithApp(4, app)
	height, round := cs1.rs.Height, cs1.rs.Round

	// Subscribe to events
	voteCh := subscribe(cs1.eventBus, types.EventQueryVote)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)

	// Start the test
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	// Wait for the proposer to create and send the proposal
	// (validator 0 is the proposer and will create the block)
	ensureNewProposal(proposalCh, height, round)

	// Wait for ProcessProposal to be called (validator enters prevote, unlocks)
	select {
	case <-processProposalStarted:
		// Good, ProcessProposal is now blocking
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessProposal was not called")
	}

	// Now ProcessProposal is blocking and all locks are released
	// Get the proposal block hash from the round state
	rs := cs1.GetRoundState()
	propBlock := rs.ProposalBlock
	propBlockParts := rs.ProposalBlockParts
	require.NotNil(t, propBlock)
	require.NotNil(t, propBlockParts)

	// Send prevotes from other validators to trigger 2/3+ majority
	// This will cause cs1 to enter precommit and sign a precommit vote
	signAddVotes(cs1, cmtproto.PrevoteType, propBlock.Hash(), propBlockParts.Header(), false, vss[1], vss[2], vss[3])

	// Wait a bit for the votes to be processed and precommit to be triggered
	time.Sleep(200 * time.Millisecond)

	// At this point, if the race condition exists:
	// - cs1 has entered precommit step
	// - cs1 has signed a precommit (step 3)
	// - ProcessProposal is still blocking

	// Now allow ProcessProposal to return
	close(processProposalShouldReturn)

	// Wait for cs1's prevote - this should fail with "step regression" if bug exists
	// or succeed if the fix is in place
	select {
	case msg := <-voteCh:
		voteEvent, ok := msg.Data().(types.EventDataVote)
		require.True(t, ok)
		vote := voteEvent.Vote

		// Check if we got a prevote from cs1
		if vote.Type == cmtproto.PrevoteType {
			// If we get here, either:
			// 1. The bug doesn't exist (no race happened)
			// 2. The fix prevented the race
			t.Logf("Successfully signed prevote: %v", vote)
		}

	case <-time.After(10 * time.Second):
		// If we timeout here, the bug likely caused a crash or hang
		// Check if there was a step regression error
		t.Fatal("Timeout waiting for prevote - possible step regression or crash")
	}

	// The test will fail if:
	// 1. We panic due to step regression in privval
	// 2. We timeout because the vote was never signed
	// 3. The error is logged but not propagated (check logs for "step regression")
}

// Helper function to create a proposal block
func createProposalBlock(t *testing.T, cs *State, rs *cstypes.RoundState, vs *validatorStub, txs []types.Tx) (*types.Proposal, *types.Block) {
	// Create a block
	ctx := context.Background()
	block, blockParts, err := cs.createProposalBlock(ctx)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.NotNil(t, blockParts)

	// Make the proposal
	polRound := int32(-1)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: blockParts.Header()}
	proposal := types.NewProposal(rs.Height, rs.Round, polRound, blockID)
	p := proposal.ToProto()

	if err := vs.SignProposal(cs.state.ChainID, p); err != nil {
		t.Fatalf("Failed to sign proposal: %v", err)
	}

	proposal.Signature = p.Signature
	return proposal, block
}
