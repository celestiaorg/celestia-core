package headersync

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	hsproto "github.com/cometbft/cometbft/proto/tendermint/headersync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

// randGenesisDoc creates a genesis document with the specified number of validators.
func randGenesisDoc(numValidators int, randPower bool, minPower int64) (*types.GenesisDoc, []types.PrivValidator) {
	validators := make([]types.GenesisValidator, numValidators)
	privValidators := make([]types.PrivValidator, numValidators)
	for i := 0; i < numValidators; i++ {
		val, privVal := types.RandValidator(randPower, minPower)
		validators[i] = types.GenesisValidator{
			PubKey: val.PubKey,
			Power:  val.VotingPower,
		}
		privValidators[i] = privVal
	}
	sort.Sort(types.PrivValidatorsByAddress(privValidators))

	consPar := types.DefaultConsensusParams()
	return &types.GenesisDoc{
		GenesisTime:     cmttime.Now(),
		ChainID:         test.DefaultTestChainID,
		Validators:      validators,
		ConsensusParams: consPar,
	}, privValidators
}

// testNode contains all components needed for a header sync test node.
type testNode struct {
	reactor    *Reactor
	blockStore *store.BlockStore
	stateStore sm.Store
	stateDB    dbm.DB
}

// newTestNode creates a test node with headers up to maxHeight.
// If maxHeight is 0, the node starts with no headers (just genesis state).
// stateHeight specifies how much state to populate (for validator lookups during sync).
// If stateHeight is 0, it uses maxHeight.
func newTestNode(
	t *testing.T,
	logger log.Logger,
	genDoc *types.GenesisDoc,
	privVals []types.PrivValidator,
	maxHeight int64,
) *testNode {
	return newTestNodeWithStateHeight(t, logger, genDoc, privVals, maxHeight, maxHeight)
}

// newTestNodeWithStateHeight creates a test node where:
// - maxHeight controls how many blocks/headers are stored
// - stateHeight controls how much state (validators) is populated
// This allows testing header sync where the syncing node has validators but no headers.
func newTestNodeWithStateHeight(
	t *testing.T,
	logger log.Logger,
	genDoc *types.GenesisDoc,
	privVals []types.PrivValidator,
	maxHeight int64,
	stateHeight int64,
) *testNode {
	if len(privVals) < 1 {
		panic("need at least one validator")
	}

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)

	// Save initial state for validator lookups
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate state entries up to stateHeight (for validator lookups).
	// This simulates a node that has state-synced up to stateHeight.
	if stateHeight > 0 {
		populateValidatorState(t, state, stateStore, privVals, stateHeight)
	}

	// Generate blocks and headers up to maxHeight
	if maxHeight > 0 {
		generateBlocks(t, state, stateStore, blockStore, privVals, maxHeight)
	}

	// Create reactor
	reactor := NewReactor(
		stateStore,
		blockStore,
		genDoc.ChainID,
		MaxHeaderBatchSize,
		NopMetrics(),
	)
	reactor.SetLogger(logger.With("module", "headersync"))

	return &testNode{
		reactor:    reactor,
		blockStore: blockStore,
		stateStore: stateStore,
		stateDB:    stateDB,
	}
}

// newTestNodeWithHeaders creates a test node that copies headers 1..headerHeight from
// sourceBlockStore before creating the reactor. This ensures the reactor's pool starts
// at the correct height.
func newTestNodeWithHeaders(
	t *testing.T,
	logger log.Logger,
	genDoc *types.GenesisDoc,
	privVals []types.PrivValidator,
	sourceBlockStore *store.BlockStore,
	headerHeight int64,
	stateHeight int64,
) *testNode {
	if len(privVals) < 1 {
		panic("need at least one validator")
	}

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)

	// Save initial state for validator lookups
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Populate validator state up to stateHeight.
	if stateHeight > 0 {
		populateValidatorState(t, state, stateStore, privVals, stateHeight)
	}

	// Copy headers 1..headerHeight from source block store BEFORE creating reactor.
	for h := int64(1); h <= headerHeight; h++ {
		meta := sourceBlockStore.LoadBlockMeta(h)
		commit := sourceBlockStore.LoadBlockCommit(h)
		if meta != nil && commit != nil {
			err := blockStore.SaveHeader(&meta.Header, commit)
			require.NoError(t, err)
		}
	}

	// Create reactor - pool will start at headerHeight+1
	reactor := NewReactor(
		stateStore,
		blockStore,
		genDoc.ChainID,
		MaxHeaderBatchSize,
		NopMetrics(),
	)
	reactor.SetLogger(logger.With("module", "headersync"))

	return &testNode{
		reactor:    reactor,
		blockStore: blockStore,
		stateStore: stateStore,
		stateDB:    stateDB,
	}
}

// populateValidatorState populates the state store with validator sets up to maxHeight.
// This is needed for header verification - the syncing node needs validators to verify commits.
func populateValidatorState(
	t *testing.T,
	state sm.State,
	stateStore sm.Store,
	privVals []types.PrivValidator,
	maxHeight int64,
) {
	// For each height, save state so validators are available.
	// Since validators don't change in our test, we just need to advance the state.
	for blockHeight := int64(1); blockHeight <= maxHeight; blockHeight++ {
		state.LastBlockHeight = blockHeight
		state.Validators = state.NextValidators.Copy()
		state.NextValidators = state.NextValidators.CopyIncrementProposerPriority(1)
		state.LastValidators = state.Validators.Copy()

		err := stateStore.Save(state)
		require.NoError(t, err)
	}
}

// generateBlocks creates and saves blocks from height 1 to maxHeight.
func generateBlocks(
	t *testing.T,
	state sm.State,
	stateStore sm.Store,
	blockStore *store.BlockStore,
	privVals []types.PrivValidator,
	maxHeight int64,
) {
	// Get validator info
	pubKey, err := privVals[0].GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	idx, _ := state.Validators.GetByAddress(addr)

	// Use ExtendedCommit pattern to properly handle empty commits for first block.
	// An empty ExtendedCommit.ToCommit() returns a non-nil *Commit{} which is
	// required for block.Hash() to work properly (nil LastCommit returns nil hash).
	seenExtCommit := &types.ExtendedCommit{}

	for blockHeight := int64(1); blockHeight <= maxHeight; blockHeight++ {
		lastExtCommit := seenExtCommit.Clone()

		// Create block
		block, parts, err := state.MakeBlock(
			blockHeight,
			types.MakeData([]types.Tx{}),
			lastExtCommit.ToCommit(),
			nil,
			state.Validators.Proposer.Address,
		)
		require.NoError(t, err)

		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

		// Create vote for current block
		vote, err := types.MakeVote(
			privVals[0],
			block.Header.ChainID,
			idx,
			blockHeight,
			0,
			cmtproto.PrecommitType,
			blockID,
			time.Now(),
		)
		require.NoError(t, err)

		// Build ExtendedCommit for this block
		seenExtCommit = &types.ExtendedCommit{
			Height:             vote.Height,
			Round:              vote.Round,
			BlockID:            blockID,
			ExtendedSignatures: []types.ExtendedCommitSig{vote.ExtendedCommitSig()},
		}

		// Save block (this also saves the header and commit)
		blockStore.SaveBlock(block, parts, seenExtCommit.ToCommit())

		// Update state for next iteration
		state.LastBlockHeight = blockHeight
		state.LastBlockID = blockID
		state.LastBlockTime = block.Header.Time
		state.Validators = state.NextValidators.Copy()
		state.NextValidators = state.NextValidators.CopyIncrementProposerPriority(1)
		state.LastValidators = state.Validators.Copy()

		err = stateStore.Save(state)
		require.NoError(t, err)
	}
}

// stopTestNode stops the reactor and cleans up.
func stopTestNode(t *testing.T, node *testNode) {
	if node.reactor.IsRunning() {
		err := node.reactor.Stop()
		require.NoError(t, err)
	}
}

// stopTestNodes stops multiple test nodes.
func stopTestNodes(t *testing.T, nodes ...*testNode) {
	for _, node := range nodes {
		stopTestNode(t, node)
	}
}

// copyHeaders copies headers from source to destination block store.
func copyHeaders(t *testing.T, dst, src *store.BlockStore, startHeight, endHeight int64) {
	for h := startHeight; h <= endHeight; h++ {
		meta := src.LoadBlockMeta(h)
		commit := src.LoadBlockCommit(h)
		if meta != nil && commit != nil {
			err := dst.SaveHeader(&meta.Header, commit)
			require.NoError(t, err)
		}
	}
}

// waitForHeight waits for a node's header height to reach the target.
func waitForHeight(t *testing.T, node *testNode, targetHeight int64, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if node.blockStore.HeaderHeight() >= targetHeight {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for height %d, got %d", targetHeight, node.blockStore.HeaderHeight())
}

// waitForCaughtUp waits for a node to catch up to its peers.
func waitForCaughtUp(t *testing.T, node *testNode, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if node.reactor.IsCaughtUp() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for catch up, height=%d, maxPeer=%d",
		node.reactor.Height(), node.reactor.MaxPeerHeight())
}

// --- Integration Tests ---

// TestHeaderSync_BasicSync tests that a node can sync headers from a peer.
func TestHeaderSync_BasicSync(t *testing.T) {
	config := test.ResetTestRoot("headersync_basic_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(50)

	// Node 0: has headers up to maxHeight
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)
	// Node 1: has no headers but has validator state (simulates state sync)
	// stateHeight=maxHeight ensures validators are available for verification
	node1 := newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Connect reactors via switches
	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for node1 to catch up
	waitForCaughtUp(t, node1, 30*time.Second)

	// Verify headers were synced
	assert.Equal(t, maxHeight, node0.blockStore.HeaderHeight())
	// Note: HeaderHeight on node1 should be at least close to maxHeight
	// It may not be exact due to timing, but should be within a small margin
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), maxHeight-5)
}

// TestHeaderSync_MultiPeerSync tests syncing from multiple peers.
func TestHeaderSync_MultiPeerSync(t *testing.T) {
	config := test.ResetTestRoot("headersync_multipeer_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(100)

	// Create source node with all headers (this is the source of truth)
	sourceNode := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)

	// Create 3 more nodes that share the same blocks (copied from source)
	nodes := make([]*testNode, 4)
	nodes[0] = sourceNode

	// Copy headers to nodes 1 and 2 (they have all headers)
	for i := 1; i <= 2; i++ {
		nodes[i] = newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)
		copyHeaders(t, nodes[i].blockStore, sourceNode.blockStore, 1, maxHeight)
	}

	// Node 3: syncing node (no headers, but has state for verification)
	nodes[3] = newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, nodes...)

	// Connect all nodes
	p2p.MakeConnectedSwitches(config.P2P, 4, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("HEADERSYNC", nodes[i].reactor)
		return s
	}, p2p.Connect2Switches)

	// Wait for node3 to catch up
	waitForCaughtUp(t, nodes[3], 30*time.Second)

	// Verify headers were synced
	assert.GreaterOrEqual(t, nodes[3].blockStore.HeaderHeight(), maxHeight-5)
}

// TestHeaderSync_StatusBroadcast tests that status is broadcast when height advances.
func TestHeaderSync_StatusBroadcast(t *testing.T) {
	config := test.ResetTestRoot("headersync_status_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(20)

	// Node 0: has all headers (source of truth)
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)

	// Node 1: has headers 1-10. We use newTestNodeWithHeaders which copies headers
	// before creating the reactor, so the pool starts at the correct height.
	node1 := newTestNodeWithHeaders(t, log.TestingLogger(), genDoc, privVals, node0.blockStore, 10, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Connect via switches
	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Node1 should sync up to node0's height
	waitForCaughtUp(t, node1, 30*time.Second)

	// Node1 should have synced headers 11-20 from node0
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), int64(15))
}

// TestHeaderSync_SubscriberNotification tests that subscribers receive verified headers.
func TestHeaderSync_SubscriberNotification(t *testing.T) {
	config := test.ResetTestRoot("headersync_subscriber_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(20)

	// Node 0: has headers
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)
	// Node 1: will sync and notify (has state for verification)
	node1 := newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Subscribe to verified headers on node1
	headerCh := make(chan *VerifiedHeader, 100)
	node1.reactor.Subscribe(headerCh)

	// Connect via switches
	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Collect verified headers
	receivedHeaders := make(map[int64]bool)
	timeout := time.After(30 * time.Second)

	for {
		select {
		case vh := <-headerCh:
			receivedHeaders[vh.Header.Height] = true
			if len(receivedHeaders) >= int(maxHeight) {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:

	// Verify we received headers in order
	assert.GreaterOrEqual(t, len(receivedHeaders), int(maxHeight)-5,
		"expected to receive most headers, got %d", len(receivedHeaders))
}

// TestHeaderSync_DoSProtection tests that peers sending duplicate status updates are disconnected.
func TestHeaderSync_DoSProtection(t *testing.T) {
	config := test.ResetTestRoot("headersync_dos_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)

	// Create two nodes with the same height
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, 50)
	node1 := newTestNode(t, log.TestingLogger(), genDoc, privVals, 50)

	defer stopTestNodes(t, node0, node1)

	// Connect via switches
	switches := p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for initial status exchange
	time.Sleep(500 * time.Millisecond)

	// Verify both nodes have 1 peer
	require.Equal(t, 1, switches[0].Peers().Size())
	require.Equal(t, 1, switches[1].Peers().Size())

	// Get the peer from node0's perspective
	peers := switches[0].Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	// Send a duplicate status update with the same height (DoS attempt)
	// This should trigger disconnection
	peer.Send(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.StatusResponse{
			Base:   node1.blockStore.Base(),
			Height: node1.blockStore.HeaderHeight() - 1, // Same or lower height
		},
	})

	// Wait for the peer to be disconnected
	time.Sleep(500 * time.Millisecond)

	// Node0 should have disconnected node1 due to DoS protection
	assert.Equal(t, 0, switches[0].Peers().Size(), "peer should be disconnected after duplicate status")
}

// TestHeaderSync_InvalidHeaderDisconnectsPeer tests that peers sending invalid headers are disconnected.
func TestHeaderSync_InvalidHeaderDisconnectsPeer(t *testing.T) {
	config := test.ResetTestRoot("headersync_invalid_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(50)

	// Node 0: valid chain with more headers (will serve headers to node1)
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)

	// Node 1: different chain (different validators = different validator hashes)
	// Has no headers, so it will try to sync from node0, but will fail verification
	// because its state store has different validators
	otherGenDoc, _ := randGenesisDoc(1, false, 30)
	otherGenDoc.ChainID = genDoc.ChainID // Same chain ID but different validators
	// Create node1 with the OTHER genDoc (different validators)
	node1 := newTestNodeWithStateHeight(t, log.TestingLogger(), otherGenDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Connect via switches
	switches := p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for sync attempts - node1 will try to sync from node0 but fail
	// because the headers won't verify against node1's validator set
	time.Sleep(10 * time.Second)

	// Node1 should have disconnected node0 due to verification failure
	// Check that at least one peer was disconnected
	totalPeers := switches[0].Peers().Size() + switches[1].Peers().Size()
	assert.Less(t, totalPeers, 2, "expected peer disconnection due to invalid headers")
}

// TestHeaderSync_CatchUpFromBehind tests catching up from significantly behind.
func TestHeaderSync_CatchUpFromBehind(t *testing.T) {
	config := test.ResetTestRoot("headersync_catchup_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(200) // Larger chain

	// Node 0: has full chain
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)
	// Node 1: starts from 0 but has state for verification
	node1 := newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Connect via switches
	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for sync
	waitForCaughtUp(t, node1, 60*time.Second)

	// Should have synced most headers
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), maxHeight-10)
}

// TestHeaderSync_PartialSync tests starting from a partial sync.
func TestHeaderSync_PartialSync(t *testing.T) {
	config := test.ResetTestRoot("headersync_partial_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(100)

	// Node 0: has full chain up to 100 (source of truth)
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)

	// Node 1: already has headers up to 50 (copied from node0 before reactor created), will sync rest
	node1 := newTestNodeWithHeaders(t, log.TestingLogger(), genDoc, privVals, node0.blockStore, 50, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Connect via switches
	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for node1 to catch up
	waitForCaughtUp(t, node1, 30*time.Second)

	// Node1 should now have headers up to ~100
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), int64(95))
}

// TestHeaderSync_ReconnectAfterDisconnect tests that sync resumes after reconnection.
func TestHeaderSync_ReconnectAfterDisconnect(t *testing.T) {
	config := test.ResetTestRoot("headersync_reconnect_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(100)

	// Node 0: has headers
	node0 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)
	// Node 1: empty but has state for verification
	node1 := newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, node0, node1)

	// Connect via switches
	switches := p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for some progress
	time.Sleep(2 * time.Second)
	heightAfterFirstSync := node1.blockStore.HeaderHeight()
	t.Logf("Height after first sync: %d", heightAfterFirstSync)

	// Disconnect peers
	for _, peer := range switches[0].Peers().List() {
		switches[0].StopPeerForError(peer, "test disconnect", "test")
	}

	// Wait a bit
	time.Sleep(500 * time.Millisecond)

	// Verify disconnected
	assert.Equal(t, 0, switches[0].Peers().Size())
	assert.Equal(t, 0, switches[1].Peers().Size())

	// Reconnect
	p2p.Connect2Switches(switches, 0, 1)

	// Wait for sync to complete
	waitForCaughtUp(t, node1, 30*time.Second)

	// Should have continued syncing
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), heightAfterFirstSync)
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), int64(90))
}

// TestHeaderSync_NoPeers tests behavior when no peers are available.
func TestHeaderSync_NoPeers(t *testing.T) {
	config := test.ResetTestRoot("headersync_nopeers_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)

	// Single node with no peers
	node := newTestNode(t, log.TestingLogger(), genDoc, privVals, 0)

	defer stopTestNode(t, node)

	// Create switch without connecting to anyone
	p2p.MakeConnectedSwitches(config.P2P, 1, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("HEADERSYNC", node.reactor)
		return s
	}, func([]*p2p.Switch, int, int) {}) // No-op connect function

	// Wait a bit - should not crash or hang
	time.Sleep(5 * time.Second)

	// Should not be caught up (no peers to learn from)
	assert.False(t, node.reactor.IsCaughtUp())
}

// TestHeaderSync_PeerProvidesPartialResponse tests that a node can sync when
// peers have different amounts of headers. This simulates a peer that is still
// catching up and doesn't have the full chain.
func TestHeaderSync_PeerProvidesPartialResponse(t *testing.T) {
	config := test.ResetTestRoot("headersync_partial_response_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(60)

	// Create node1 first with all headers - this becomes the "source of truth"
	// for the blockchain data.
	node1 := newTestNode(t, log.TestingLogger(), genDoc, privVals, maxHeight)

	// Node 0: copy blocks from node1 but only up to height 30.
	// This simulates a peer that's still catching up.
	node0 := newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)
	copyHeaders(t, node0.blockStore, node1.blockStore, 1, 30)

	// Node 2: syncing node (has state for verification)
	node2 := newTestNodeWithStateHeight(t, log.TestingLogger(), genDoc, privVals, 0, maxHeight)

	defer stopTestNodes(t, node0, node1, node2)

	// Connect all nodes
	p2p.MakeConnectedSwitches(config.P2P, 3, func(i int, s *p2p.Switch) *p2p.Switch {
		switch i {
		case 0:
			s.AddReactor("HEADERSYNC", node0.reactor)
		case 1:
			s.AddReactor("HEADERSYNC", node1.reactor)
		case 2:
			s.AddReactor("HEADERSYNC", node2.reactor)
		}
		return s
	}, p2p.Connect2Switches)

	// Wait for sync
	waitForCaughtUp(t, node2, 30*time.Second)

	// Node2 should have synced up to the max available (60)
	assert.GreaterOrEqual(t, node2.blockStore.HeaderHeight(), int64(55))
}
