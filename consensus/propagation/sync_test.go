package propagation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/headersync"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

func TestSwitchToCatchup_WaitForHeadersync(t *testing.T) {
	// 1. Create reactors and switches (connected).
	// We use 2 nodes. r0 will be the one state-syncing. r1 is the peer.
	reactors, _ := createTestReactors(2, cfg.DefaultP2PConfig(), false, "")
	r0 := reactors[0]
	// r1 := reactors[1]

	// 2. Set up headersync reactor for r0.
	// We need a blockstore and statestore.
	blockStoreDB := dbm.NewMemDB()
	blockStore := store.NewBlockStore(blockStoreDB)
	stateStore := state.NewStore(dbm.NewMemDB(), state.StoreOptions{})

	hsR := headersync.NewReactor(
		stateStore,
		blockStore,
		TestChainID,
		10,  // batch size
		nil, // metrics
	)
	hsR.SetLogger(log.TestingLogger())

	// Inject headersync into propagation reactor.
	r0.headerSyncReactor = hsR

	// 3. Set r0's store height to simulate state sync completion.

	// Create a dummy state
	state, err := stateStore.LoadFromDBOrGenesisDoc(&types.GenesisDoc{ChainID: TestChainID})
	require.NoError(t, err)
	state.LastBlockHeight = 1000

	// We need to put something in the blockstore of r0 to make Store().Height() return 1000.
	// r0.pendingBlocks.store is accessible.
	r0Store := r0.pendingBlocks.Store()
	require.NotNil(t, r0Store)

	// Save a dummy block at 1000.
	// We create a block at height 1000 and save it.
	lastCommit := &types.Commit{Height: 999}
	block := types.MakeBlock(1000, types.Data{Txs: []types.Tx{}}, lastCommit, []types.Evidence{})
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := &types.Commit{
		Height: 1000,
		BlockID: types.BlockID{
			Hash:          block.Hash(),
			PartSetHeader: partSet.Header(),
		},
	}
	r0.pendingBlocks.Store().SaveBlock(block, partSet, seenCommit)

	// Now `store.Height()` should be 1000.
	require.Equal(t, int64(1000), r0.pendingBlocks.Store().Height())

	// Now call IsCaughtUp.
	// Peers: 1 (r1).
	// MaxPeerHeight: 0 (headersync has no peers because it is not connected to the switch in this test,
	// or hasn't received status).

	// Expectation: IsCaughtUp should be FALSE (because we don't trust maxPeerHeight=0 yet).
	// Current behavior (bug): IsCaughtUp returns TRUE because storeHeight(1000) > 0 and maxPeerHeight(0) == 0.
	//
	//  1. State Sync brings your node to height 1000.
	//  2. Headersync is reset but needs a split second to exchange Status messages with peers to learn their heights.
	//  3. During this brief window:
	//      * Your Height: 1000
	//      * Max Peer Height: 0 (Default value because Headersync hasn't processed peer statuses yet).

	// The Bug:
	// The catchup logic says: "I am caught up if my height is greater than or equal to the max peer height."
	// 1000 >= 0 is True.

	assert.False(t, r0.IsCaughtUp(), "Should not be caught up if headersync has no peers yet")
}
