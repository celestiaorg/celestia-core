package blocksync

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	mpmocks "github.com/cometbft/cometbft/mempool/mocks"
	"github.com/cometbft/cometbft/p2p"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	smmocks "github.com/cometbft/cometbft/state/mocks"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

// upgradeApp raises the app version to BlockTxLimitAppVersion after
// activationHeight, so later heights are validated under the v11 limit.
type upgradeApp struct {
	abci.BaseApplication
	activationHeight int64
}

func (app *upgradeApp) FinalizeBlock(ctx context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	res, err := app.BaseApplication.FinalizeBlock(ctx, req)
	if err == nil && req.Height == app.activationHeight {
		res.ConsensusParamUpdates = &cmtproto.ConsensusParams{
			Version: &cmtproto.VersionParams{App: types.BlockTxLimitAppVersion},
		}
	}
	return res, err
}

type activationNode struct {
	reactor *Reactor
	app     proxy.AppConns
	store   *store.BlockStore
}

// newActivationNode commits maxHeight blocks, where txCounts sets the number of
// transactions per height. Core does not bound the count when applying blocks,
// so the source can serve blocks that a syncing node must reject.
func newActivationNode(
	t *testing.T,
	genDoc *types.GenesisDoc,
	privVal types.PrivValidator,
	activationHeight int64,
	maxHeight int64,
	txCounts map[int64]int,
) activationNode {
	t.Helper()
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(&upgradeApp{activationHeight: activationHeight}), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())

	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{})
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	require.NoError(t, stateStore.Save(state))

	mp := &mpmocks.Mempool{}
	mp.On("Lock").Return()
	mp.On("Unlock").Return()
	mp.On("FlushAppConn", mock.Anything).Return(nil)
	mp.On("Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(), mp, sm.EmptyEvidencePool{}, blockStore)

	pubKey, err := privVal.GetPubKey()
	require.NoError(t, err)
	idx, _ := state.Validators.GetByAddress(pubKey.Address())

	lastCommit := &types.Commit{}
	for height := int64(1); height <= maxHeight; height++ {
		txs := make(types.Txs, txCounts[height])
		for i := range txs {
			txs[i] = binary.BigEndian.AppendUint32(nil, uint32(i))
		}
		block, parts, err := state.MakeBlock(height, types.MakeData(txs), lastCommit, nil, state.Validators.Proposer.Address)
		require.NoError(t, err)
		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

		vote, err := types.MakeVote(privVal, block.ChainID, idx, height, 0, cmtproto.PrecommitType, blockID, time.Now())
		require.NoError(t, err)
		commit := &types.Commit{
			Height:     height,
			BlockID:    blockID,
			Signatures: []types.CommitSig{vote.CommitSig()},
		}

		state, err = blockExec.ApplyBlock(state, blockID, block, lastCommit)
		require.NoError(t, err)
		blockStore.SaveBlock(block, parts, commit)
		lastCommit = commit
	}

	reactor := NewReactor(state.Copy(), blockExec, blockStore, true, NopMetrics(), 0)
	reactor.SetLogger(log.TestingLogger().With("module", "blocksync"))
	return activationNode{reactor: reactor, app: proxyApp, store: blockStore}
}

// TestBlockSyncAcrossTxLimitActivation syncs a node through the v11 activation
// height. Oversized responses arrive while the syncing node is still at v10, so
// their transactions are deferred. Each must then be decoded under the app
// version of the state that applies it: legacy blocks are restored in full and
// the first over-limit v11 block is rejected.
func TestBlockSyncAcrossTxLimitActivation(t *testing.T) {
	config = test.ResetTestRoot("blocksync_activation_test")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randGenesisDoc(1, false, 30)
	genDoc.ConsensusParams.ABCI.VoteExtensionsEnableHeight = 0
	genDoc.ConsensusParams.Version.App = types.BlockTxLimitAppVersion - 1

	const (
		activationHeight = int64(3) // the last height applied under v10
		rejectedHeight   = int64(5) // the first oversized height applied under v11
		maxHeight        = int64(7)
	)
	overLimit := types.MaxBlockTxs + 1
	txCounts := map[int64]int{2: overLimit, activationHeight: overLimit, rejectedHeight: overLimit}

	source := newActivationNode(t, genDoc, privVals[0], activationHeight, maxHeight, txCounts)
	syncer := newActivationNode(t, genDoc, privVals[0], activationHeight, 0, nil)
	nodes := []activationNode{source, syncer}
	defer func() {
		for _, n := range nodes {
			_ = n.reactor.Stop()
			require.NoError(t, n.app.Stop())
		}
	}()

	p2p.MakeConnectedSwitches(config.P2P, len(nodes), func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", nodes[i].reactor)
		return s
	}, p2p.Connect2Switches)

	// Rejecting the v11 block disconnects the only peer, leaving the syncer at
	// the height just below it.
	require.Eventually(t, func() bool {
		return syncer.store.Height() == rejectedHeight-1 && syncer.reactor.Switch.Peers().Size() == 0
	}, 30*time.Second, 10*time.Millisecond)

	for _, height := range []int64{2, activationHeight} {
		synced := syncer.store.LoadBlock(height)
		require.NotNil(t, synced, "height %d", height)
		require.Len(t, synced.Txs, overLimit, "deferred v10 transactions must be restored at height %d", height)
		require.Equal(t, source.store.LoadBlock(height).Hash(), synced.Hash())
	}
	require.Nil(t, syncer.store.LoadBlock(rejectedHeight), "an over-limit v11 block must not be stored")
}

func TestBlocksyncChannelTxLimitUsesCommittedVersion(t *testing.T) {
	for _, version := range []uint64{9, 10, 11} {
		stateStore := &smmocks.Store{}
		stateStore.On("Load").Return(sm.State{Version: cmtstate.Version{
			Consensus: cmtversion.Consensus{App: version},
		}}, nil).Once()
		blockExec := sm.NewBlockExecutor(stateStore, nil, nil, nil, nil, nil)
		channel := (&Reactor{blockExec: blockExec}).GetChannels()[0]

		err := channel.RecvMessagePrecheck(blockResponseWithTxs(t, types.MaxBlockTxs+1, nil))
		if version >= types.BlockTxLimitAppVersion {
			require.ErrorIs(t, err, cmtproto.ErrTooManyBlockTxs)
		} else {
			require.NoError(t, err, "the response may belong to a height before the limit")
		}
		stateStore.AssertExpectations(t)
	}

	// Ordinary responses do not add a state-store read to the hot path.
	stateStore := &smmocks.Store{}
	channel := (&Reactor{blockExec: sm.NewBlockExecutor(stateStore, nil, nil, nil, nil, nil)}).GetChannels()[0]
	require.NoError(t, channel.RecvMessagePrecheck(blockResponseWithTxs(t, types.MaxBlockTxs, nil)))
	stateStore.AssertNotCalled(t, "Load")
}
