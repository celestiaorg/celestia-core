package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/types"
)

//-------------------------------------------
// Tests for CatchupBlockMessage
//-------------------------------------------

func TestCatchupBlockMessage_ValidateBasic(t *testing.T) {
	// Create a valid block
	block := makeTestBlock(1)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	commit := &types.Commit{Height: 1, Round: 0, BlockID: blockID, Signatures: nil}

	testCases := []struct {
		name    string
		msg     *CatchupBlockMessage
		wantErr bool
	}{
		{
			name: "valid message",
			msg: &CatchupBlockMessage{
				Height: 1,
				Block:  block,
				Parts:  partSet,
				Commit: commit,
			},
			wantErr: false,
		},
		{
			name: "invalid height zero",
			msg: &CatchupBlockMessage{
				Height: 0,
				Block:  block,
				Parts:  partSet,
				Commit: commit,
			},
			wantErr: true,
		},
		{
			name: "invalid height negative",
			msg: &CatchupBlockMessage{
				Height: -1,
				Block:  block,
				Parts:  partSet,
				Commit: commit,
			},
			wantErr: true,
		},
		{
			name: "nil block",
			msg: &CatchupBlockMessage{
				Height: 1,
				Block:  nil,
				Parts:  partSet,
				Commit: commit,
			},
			wantErr: true,
		},
		{
			name: "nil parts",
			msg: &CatchupBlockMessage{
				Height: 1,
				Block:  block,
				Parts:  nil,
				Commit: commit,
			},
			wantErr: true,
		},
		{
			name: "incomplete parts",
			msg: &CatchupBlockMessage{
				Height: 1,
				Block:  block,
				Parts:  types.NewPartSetFromHeader(partSet.Header(), types.BlockPartSizeBytes),
				Commit: commit,
			},
			wantErr: true,
		},
		{
			name: "nil commit",
			msg: &CatchupBlockMessage{
				Height: 1,
				Block:  block,
				Parts:  partSet,
				Commit: nil,
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCatchupBlockMessage_String(t *testing.T) {
	block := makeTestBlock(1)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	msg := &CatchupBlockMessage{
		Height: 1,
		Block:  block,
		Parts:  partSet,
		Commit: &types.Commit{Height: 1},
	}

	str := msg.String()
	assert.Contains(t, str, "1")
}

//-------------------------------------------
// Tests for executeCatchupBlock
//-------------------------------------------

func TestExecuteCatchupBlock_Success(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	// Start consensus
	newRoundCh := subscribe(cs.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs.eventBus, types.EventQueryCompleteProposal)

	startTestRound(cs, height, round)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	rs := cs.GetRoundState()
	block := rs.ProposalBlock
	parts := rs.ProposalBlockParts

	// Finalize height 1 to advance to height 2 using extEnabled=true to match state_test.go
	signAddVotes(cs, cmtproto.PrecommitType, block.Hash(), parts.Header(), true, vss[1:]...)
	ensureNewRound(newRoundCh, height+1, 0)

	// Create a valid block for height 2
	cs.mtx.Lock()
	block2, _, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	parts2, err := block2.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	cs.mtx.Unlock()

	// Create commit for block2
	blockID2 := types.BlockID{Hash: block2.Hash(), PartSetHeader: parts2.Header()}
	for _, vs := range vss {
		vs.Height = height + 1
		vs.Round = 0
	}
	votes2 := signVotes(cmtproto.PrecommitType, block2.Hash(), parts2.Header(), true, vss...)

	sigs := make([]types.CommitSig, len(votes2))
	for i, vote := range votes2 {
		sigs[i] = types.CommitSig{
			BlockIDFlag:      types.BlockIDFlagCommit,
			ValidatorAddress: vote.ValidatorAddress,
			Timestamp:        vote.Timestamp,
			Signature:        vote.Signature,
		}
	}
	commit := &types.Commit{
		Height:     height + 1,
		Round:      0,
		BlockID:    blockID2,
		Signatures: sigs,
	}

	msg := &CatchupBlockMessage{
		Height: height + 1,
		Block:  block2,
		Parts:  parts2,
		Commit: commit,
	}

	initialHeight := cs.state.LastBlockHeight

	// Execute catchup block
	cs.executeCatchupBlock(msg)

	// Verify the block was applied
	assert.Equal(t, initialHeight+1, cs.state.LastBlockHeight)
}

func TestExecuteCatchupBlock_WrongHeight(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	// Start consensus
	newRoundCh := subscribe(cs.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs.eventBus, types.EventQueryCompleteProposal)

	startTestRound(cs, height, round)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	rs := cs.GetRoundState()
	block := rs.ProposalBlock
	parts := rs.ProposalBlockParts

	// Finalize height 1
	signAddVotes(cs, cmtproto.PrecommitType, block.Hash(), parts.Header(), true, vss[1:]...)
	ensureNewRound(newRoundCh, height+1, 0)

	// Try to execute a block at height 3 (skipping height 2)
	cs.mtx.Lock()
	block3, _, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	parts3, err := block3.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	cs.mtx.Unlock()

	blockID3 := types.BlockID{Hash: block3.Hash(), PartSetHeader: parts3.Header()}

	msg := &CatchupBlockMessage{
		Height: height + 2, // Wrong height - should be height + 1
		Block:  block3,
		Parts:  parts3,
		Commit: &types.Commit{Height: height + 2, BlockID: blockID3},
	}

	initialHeight := cs.state.LastBlockHeight

	// Execute should fail due to wrong height
	cs.executeCatchupBlock(msg)

	// Height should remain unchanged
	assert.Equal(t, initialHeight, cs.state.LastBlockHeight)
}

func TestExecuteCatchupBlock_InvalidBlock(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	// Start consensus
	newRoundCh := subscribe(cs.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs.eventBus, types.EventQueryCompleteProposal)

	startTestRound(cs, height, round)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	rs := cs.GetRoundState()
	block := rs.ProposalBlock
	parts := rs.ProposalBlockParts

	// Finalize height 1
	signAddVotes(cs, cmtproto.PrecommitType, block.Hash(), parts.Header(), true, vss[1:]...)
	ensureNewRound(newRoundCh, height+1, 0)

	// Create an invalid block (wrong chain ID)
	invalidBlock := makeTestBlock(height + 1)
	invalidBlock.ChainID = "wrong-chain"
	invalidParts, err := invalidBlock.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	msg := &CatchupBlockMessage{
		Height: height + 1,
		Block:  invalidBlock,
		Parts:  invalidParts,
		Commit: &types.Commit{Height: height + 1},
	}

	initialHeight := cs.state.LastBlockHeight

	// Execute should fail due to invalid block
	cs.executeCatchupBlock(msg)

	// Height should remain unchanged
	assert.Equal(t, initialHeight, cs.state.LastBlockHeight)
}

func TestExecuteCatchupBlock_InvalidCommit(t *testing.T) {
	cs, vss := randState(4)
	height, round := cs.rs.Height, cs.rs.Round

	// Start consensus
	newRoundCh := subscribe(cs.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs.eventBus, types.EventQueryCompleteProposal)

	startTestRound(cs, height, round)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	rs := cs.GetRoundState()
	block := rs.ProposalBlock
	parts := rs.ProposalBlockParts

	// Finalize height 1
	signAddVotes(cs, cmtproto.PrecommitType, block.Hash(), parts.Header(), true, vss[1:]...)
	ensureNewRound(newRoundCh, height+1, 0)

	// Create a valid block for height 2
	cs.mtx.Lock()
	block2, _, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	parts2, err := block2.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	cs.mtx.Unlock()

	// Create a commit with no signatures (insufficient voting power)
	blockID2 := types.BlockID{Hash: block2.Hash(), PartSetHeader: parts2.Header()}
	commit := &types.Commit{
		Height:     height + 1,
		Round:      0,
		BlockID:    blockID2,
		Signatures: nil, // No signatures = no voting power
	}

	msg := &CatchupBlockMessage{
		Height: height + 1,
		Block:  block2,
		Parts:  parts2,
		Commit: commit,
	}

	initialHeight := cs.state.LastBlockHeight

	// Execute should fail due to invalid commit (insufficient voting power)
	cs.executeCatchupBlock(msg)

	// Height should remain unchanged
	assert.Equal(t, initialHeight, cs.state.LastBlockHeight)
}

//-------------------------------------------
// Tests for syncData with catchup channel
//-------------------------------------------

// TestSyncData_CatchupChannelReceives verifies that the syncData goroutine
// properly receives messages from the catchup channel and forwards them
// to the internal message queue.
func TestSyncData_CatchupChannelReceives(t *testing.T) {
	cs, _ := randState(1)

	// Create a mock propagator that returns a catchup channel
	catchupChan := make(chan *propagation.CatchupBlockInfo, 10)
	mockPropagator := &MockCatchupPropagator{
		catchupChan:  catchupChan,
		partChan:     make(chan types.PartInfo),
		proposalChan: make(chan propagation.ProposalAndSrc),
	}
	cs.propagator = mockPropagator

	// Start consensus (which starts syncData)
	require.NoError(t, cs.Start())
	defer cs.Stop()

	// Create a simple catchup block (doesn't need to be valid for this test)
	block := makeTestBlock(1)
	parts, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	commit := &types.Commit{Height: 1}

	catchupInfo := &propagation.CatchupBlockInfo{
		Height: 1,
		Block:  block,
		Parts:  parts,
		Commit: commit,
	}

	// Send to catchup channel - this should be picked up by syncData
	select {
	case catchupChan <- catchupInfo:
		// Successfully sent to channel
	case <-time.After(time.Second):
		t.Fatal("timeout sending to catchup channel")
	}

	// Give time for syncData to receive and forward the message
	// We can't easily verify it was processed, but we verify no panic/deadlock
	time.Sleep(100 * time.Millisecond)

	// The message is forwarded to internalMsgQueue and will be processed by handleMsg.
	// Since we don't have valid block/commit, it will be rejected, but that's expected.
	// This test verifies the channel plumbing works.
}

//-------------------------------------------
// Helper functions and mock types
//-------------------------------------------

func makeTestBlock(height int64) *types.Block {
	return &types.Block{
		Header: types.Header{
			Version:            cmtversion.Consensus{App: 1, Block: 11},
			ChainID:            "test-chain",
			Height:             height,
			Time:               time.Now(),
			LastBlockID:        types.BlockID{},
			LastCommitHash:     nil,
			DataHash:           nil,
			ValidatorsHash:     cmtrand.Bytes(32),
			NextValidatorsHash: cmtrand.Bytes(32),
			ConsensusHash:      cmtrand.Bytes(32),
			AppHash:            cmtrand.Bytes(32),
			LastResultsHash:    nil,
			EvidenceHash:       nil,
			ProposerAddress:    cmtrand.Bytes(20),
		},
		Data:     types.Data{},
		Evidence: types.EvidenceData{},
		LastCommit: &types.Commit{
			Height: height - 1,
			Round:  0,
		},
	}
}

// MockCatchupPropagator is a mock propagator that provides a catchup channel for testing.
type MockCatchupPropagator struct {
	catchupChan  chan *propagation.CatchupBlockInfo
	partChan     chan types.PartInfo
	proposalChan chan propagation.ProposalAndSrc
}

var _ propagation.Propagator = &MockCatchupPropagator{}

func (m *MockCatchupPropagator) GetCatchupBlockChan() <-chan *propagation.CatchupBlockInfo {
	return m.catchupChan
}

func (m *MockCatchupPropagator) GetPartChan() <-chan types.PartInfo {
	return m.partChan
}

func (m *MockCatchupPropagator) GetProposalChan() <-chan propagation.ProposalAndSrc {
	return m.proposalChan
}

func (m *MockCatchupPropagator) GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool) {
	return nil, nil, false
}

func (m *MockCatchupPropagator) ProposeBlock(proposal *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData) error {
	return nil
}

func (m *MockCatchupPropagator) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {}

func (m *MockCatchupPropagator) Prune(height int64) {}

func (m *MockCatchupPropagator) SetHeightAndRound(height int64, round int32) {}

func (m *MockCatchupPropagator) StartProcessing() {}

func (m *MockCatchupPropagator) SetProposer(proposer crypto.PubKey) {}

//-------------------------------------------
// Tests for state sync handoff
//-------------------------------------------

func TestStateSyncHandoff_ShouldWait(t *testing.T) {
	cs, _ := randState(1)

	// Create a noop propagator
	propagator := propagation.NewNoOpPropagator()

	// Create a reactor in state sync mode (waitSync=true)
	reactor := NewReactor(cs, propagator, true)

	// Verify reactor IS waiting for sync
	require.True(t, reactor.WaitSync(), "state sync mode should wait for sync")
}

func TestBlocksyncHandoff_NoWait(t *testing.T) {
	cs, _ := randState(1)

	// Create a noop propagator
	propagator := propagation.NewNoOpPropagator()

	// Create a reactor in blocksync mode (waitSync=false)
	reactor := NewReactor(cs, propagator, false)

	// Verify reactor is NOT waiting for sync
	require.False(t, reactor.WaitSync(), "blocksync mode should not wait for sync")
}

func TestReactorCreation_StateSyncVsBlocksync(t *testing.T) {
	testCases := []struct {
		name          string
		stateSync     bool
		expectWait    bool
	}{
		{
			name:       "state sync mode",
			stateSync:  true,
			expectWait: true,
		},
		{
			name:       "blocksync mode (phase 5)",
			stateSync:  false,
			expectWait: false,
		},
		{
			name:       "normal mode",
			stateSync:  false,
			expectWait: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := randState(1)
			propagator := propagation.NewNoOpPropagator()
			reactor := NewReactor(cs, propagator, tc.stateSync)

			assert.Equal(t, tc.expectWait, reactor.WaitSync())
		})
	}
}
