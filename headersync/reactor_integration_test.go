package headersync

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	cfg "github.com/cometbft/cometbft/config"
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

// nodeConfig configures a test node.
type nodeConfig struct {
	// genDoc and privVals are required.
	genDoc   *types.GenesisDoc
	privVals []types.PrivValidator

	// headersUpTo specifies the height to which headers should be populated.
	headersUpTo int64

	// stateUpTo specifies the height to which state (validators) should be populated.
	// If 0, it defaults to headersUpTo.
	stateUpTo int64

	// sourceStore, if provided, is used to copy headers from instead of generating them.
	// This ensures block hashes match between nodes.
	sourceStore *store.BlockStore
}

// createNode creates a test node based on the configuration.
func createNode(t *testing.T, logger log.Logger, cfg nodeConfig) *testNode {
	if len(cfg.privVals) < 1 {
		panic("need at least one validator")
	}

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(cfg.genDoc)
	require.NoError(t, err)

	// Save initial state for validator lookups
	err = stateStore.Save(state)
	require.NoError(t, err)

	stateHeight := cfg.stateUpTo
	if stateHeight == 0 {
		stateHeight = cfg.headersUpTo
	}

	// Populate validator state.
	if stateHeight > 0 {
		populateValidatorState(t, state, stateStore, cfg.privVals, stateHeight)
	}

	// Populate headers/blocks.
	if cfg.headersUpTo > 0 {
		if cfg.sourceStore != nil {
			copyHeaders(t, blockStore, cfg.sourceStore, 1, cfg.headersUpTo)
		} else {
			generateBlocks(t, state, stateStore, blockStore, cfg.privVals, cfg.headersUpTo)
		}
	}

	// Create reactor
	reactor := NewReactor(
		stateStore,
		blockStore,
		cfg.genDoc.ChainID,
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

// makeNetwork creates connected switches for the given nodes.
func makeNetwork(t *testing.T, config *cfg.Config, nodes ...*testNode) []*p2p.Switch {
	return p2p.MakeConnectedSwitches(config.P2P, len(nodes), func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("HEADERSYNC", nodes[i].reactor)
		return s
	}, p2p.Connect2Switches)
}

// populateValidatorState populates the state store with validator sets up to maxHeight.
func populateValidatorState(
	t *testing.T,
	state sm.State,
	stateStore sm.Store,
	privVals []types.PrivValidator,
	maxHeight int64,
) {
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
	pubKey, err := privVals[0].GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	idx, _ := state.Validators.GetByAddress(addr)

	seenExtCommit := &types.ExtendedCommit{}

	for blockHeight := int64(1); blockHeight <= maxHeight; blockHeight++ {
		lastExtCommit := seenExtCommit.Clone()

		block, parts, err := state.MakeBlock(
			blockHeight,
			types.MakeData([]types.Tx{}),
			lastExtCommit.ToCommit(),
			nil,
			state.Validators.Proposer.Address,
		)
		require.NoError(t, err)

		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

		vote, err := types.MakeVote(
			privVals[0],
			block.ChainID,
			idx,
			blockHeight,
			0,
			cmtproto.PrecommitType,
			blockID,
			time.Now(),
		)
		require.NoError(t, err)

		seenExtCommit = &types.ExtendedCommit{
			Height:             vote.Height,
			Round:              vote.Round,
			BlockID:            blockID,
			ExtendedSignatures: []types.ExtendedCommitSig{vote.ExtendedCommitSig()},
		}

		blockStore.SaveBlock(block, parts, seenExtCommit.ToCommit())

		state.LastBlockHeight = blockHeight
		state.LastBlockID = blockID
		state.LastBlockTime = block.Time
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

// TestHeaderSync_Sync tests basic syncing for different chain lengths.
func TestHeaderSync_Sync(t *testing.T) {
	tests := []struct {
		name      string
		maxHeight int64
	}{
		{"ShortChain", 50},
		{"LongChain", 200},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := test.ResetTestRoot("headersync_sync_" + tc.name)
			defer os.RemoveAll(config.RootDir)

			genDoc, privVals := randGenesisDoc(1, false, 30)

			node0 := createNode(t, log.TestingLogger(), nodeConfig{
				genDoc:      genDoc,
				privVals:    privVals,
				headersUpTo: tc.maxHeight,
			})

			node1 := createNode(t, log.TestingLogger(), nodeConfig{
				genDoc:      genDoc,
				privVals:    privVals,
				headersUpTo: 0,
				stateUpTo:   tc.maxHeight,
			})

			defer stopTestNodes(t, node0, node1)

			makeNetwork(t, config, node0, node1)

			waitForCaughtUp(t, node1, 60*time.Second)

			assert.Equal(t, tc.maxHeight, node0.blockStore.HeaderHeight())
			assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), tc.maxHeight-5)
		})
	}
}

// TestHeaderSync_MultiPeerSync tests syncing from multiple peers.
func TestHeaderSync_MultiPeerSync(t *testing.T) {
	config := test.ResetTestRoot("headersync_multipeer_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(100)

	// Create source node with all headers
	sourceNode := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	nodes := make([]*testNode, 4)
	nodes[0] = sourceNode

	// Copy headers to nodes 1 and 2
	for i := 1; i <= 2; i++ {
		nodes[i] = createNode(t, log.TestingLogger(), nodeConfig{
			genDoc:      genDoc,
			privVals:    privVals,
			headersUpTo: maxHeight,
			sourceStore: sourceNode.blockStore,
		})
	}

	// Node 3: syncing node
	nodes[3] = createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 0,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, nodes...)

	makeNetwork(t, config, nodes...)

	waitForCaughtUp(t, nodes[3], 30*time.Second)

	assert.GreaterOrEqual(t, nodes[3].blockStore.HeaderHeight(), maxHeight-5)
}

// TestHeaderSync_StatusBroadcast tests that status is broadcast when height advances.
func TestHeaderSync_StatusBroadcast(t *testing.T) {
	config := test.ResetTestRoot("headersync_status_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(20)

	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	// Node 1 starts with 10 headers
	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 10,
		sourceStore: node0.blockStore,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, node0, node1)

	makeNetwork(t, config, node0, node1)

	waitForCaughtUp(t, node1, 30*time.Second)

	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), int64(15))
}

// TestHeaderSync_SubscriberNotification tests that subscribers receive verified headers.
func TestHeaderSync_SubscriberNotification(t *testing.T) {
	config := test.ResetTestRoot("headersync_subscriber_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(20)

	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 0,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, node0, node1)

	// Subscribe to verified headers on node1
	headerCh := make(chan *VerifiedHeader, 100)
	node1.reactor.Subscribe(headerCh)

	makeNetwork(t, config, node0, node1)

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

	assert.GreaterOrEqual(t, len(receivedHeaders), int(maxHeight)-5)
}

// TestHeaderSync_DoSProtection tests that peers sending duplicate status updates are disconnected.
func TestHeaderSync_DoSProtection(t *testing.T) {
	config := test.ResetTestRoot("headersync_dos_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)

	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 50,
	})
	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 50,
	})

	defer stopTestNodes(t, node0, node1)

	switches := makeNetwork(t, config, node0, node1)

	// Wait for initial status exchange
	time.Sleep(500 * time.Millisecond)
	require.Equal(t, 1, switches[0].Peers().Size())

	// Send DoS attempt
	peers := switches[0].Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	peer.Send(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.StatusResponse{
			Base:   node1.blockStore.Base(),
			Height: node1.blockStore.HeaderHeight() - 1,
		},
	})

	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, 0, switches[0].Peers().Size())
}

// TestHeaderSync_InvalidHeaderDisconnectsPeer tests that peers sending invalid headers are disconnected.
func TestHeaderSync_InvalidHeaderDisconnectsPeer(t *testing.T) {
	config := test.ResetTestRoot("headersync_invalid_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(50)

	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	// Different chain ID/validators
	otherGenDoc, _ := randGenesisDoc(1, false, 30)
	otherGenDoc.ChainID = genDoc.ChainID

	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      otherGenDoc,
		privVals:    privVals,
		headersUpTo: 0,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, node0, node1)

	switches := makeNetwork(t, config, node0, node1)

	time.Sleep(10 * time.Second)

	totalPeers := switches[0].Peers().Size() + switches[1].Peers().Size()
	assert.Less(t, totalPeers, 2)
}

// TestHeaderSync_PartialSync tests starting from a partial sync.
func TestHeaderSync_PartialSync(t *testing.T) {
	config := test.ResetTestRoot("headersync_partial_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(100)

	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 50,
		sourceStore: node0.blockStore,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, node0, node1)

	makeNetwork(t, config, node0, node1)

	waitForCaughtUp(t, node1, 30*time.Second)

	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), int64(95))
}

// TestHeaderSync_ReconnectAfterDisconnect tests that sync resumes after reconnection.
func TestHeaderSync_ReconnectAfterDisconnect(t *testing.T) {
	config := test.ResetTestRoot("headersync_reconnect_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	maxHeight := int64(100)

	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 0,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, node0, node1)

	switches := makeNetwork(t, config, node0, node1)

	time.Sleep(2 * time.Second)
	heightAfterFirstSync := node1.blockStore.HeaderHeight()

	for _, peer := range switches[0].Peers().List() {
		switches[0].StopPeerForError(peer, "test disconnect", "test")
	}

	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, 0, switches[0].Peers().Size())

	p2p.Connect2Switches(switches, 0, 1)

	waitForCaughtUp(t, node1, 30*time.Second)

	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), heightAfterFirstSync)
	assert.GreaterOrEqual(t, node1.blockStore.HeaderHeight(), int64(90))
}

// TestHeaderSync_NoPeers tests behavior when no peers are available.
func TestHeaderSync_NoPeers(t *testing.T) {
	config := test.ResetTestRoot("headersync_nopeers_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)

	node := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 0,
	})
	defer stopTestNode(t, node)

	p2p.MakeConnectedSwitches(config.P2P, 1, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("HEADERSYNC", node.reactor)
		return s
	}, func([]*p2p.Switch, int, int) {})

	time.Sleep(5 * time.Second)
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

	node1 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: maxHeight,
	})

	// Node 0: copy blocks from node1 but only up to height 30.
	// This simulates a peer that's still catching up.
	node0 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 30,
		sourceStore: node1.blockStore,
		stateUpTo:   maxHeight,
	})

	node2 := createNode(t, log.TestingLogger(), nodeConfig{
		genDoc:      genDoc,
		privVals:    privVals,
		headersUpTo: 0,
		stateUpTo:   maxHeight,
	})

	defer stopTestNodes(t, node0, node1, node2)

	makeNetwork(t, config, node0, node1, node2)

	waitForCaughtUp(t, node2, 30*time.Second)

	assert.GreaterOrEqual(t, node2.blockStore.HeaderHeight(), int64(55))
}
