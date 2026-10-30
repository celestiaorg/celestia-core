package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/evidence"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool/cat"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	sm "github.com/cometbft/cometbft/state"
	blockidxkv "github.com/cometbft/cometbft/state/indexer/block/kv"
	"github.com/cometbft/cometbft/state/txindex/kv"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

const (
	// EnvTestHeight is the number of blocks committed by NewTestEnvironment.
	EnvTestHeight = 4
	// EnvTestTxsPerBlock is the number of kvstore txs in every committed block.
	EnvTestTxsPerBlock = 3
	// EnvTestMaxTxBytes is the mempool tx size limit used by NewTestEnvironment.
	EnvTestMaxTxBytes = 4096
)

type testConsensusReactor struct{}

func (testConsensusReactor) WaitSync() bool     { return false }
func (testConsensusReactor) IsCatchingUp() bool { return false }

// EnvTestTx returns the i-th tx committed at height h by NewTestEnvironment.
func EnvTestTx(h int64, i int) types.Tx {
	return types.Tx(fmt.Sprintf("key%d-%d=value%d", h, i, i))
}

// NewTestEnvironment returns an RPC Environment backed by in-memory stores, a
// kvstore app, a CAT mempool, a real evidence pool and kv indexers, with
// EnvTestHeight blocks applied, saved and indexed. It is deterministic: the
// validator key and all timestamps are fixed. Services stop on tb cleanup.
func NewEnvForTest(tb testing.TB) *Environment {
	tb.Helper()
	logger := log.NewNopLogger()

	privKey := ed25519.GenPrivKeyFromSecret([]byte("rpc-core-fuzz"))
	pv := types.NewMockPVWithParams(privKey, false, false)
	pubKey := privKey.PubKey()
	genTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	genDoc := &types.GenesisDoc{
		ChainID:         "rpc-core-fuzz",
		GenesisTime:     genTime,
		InitialHeight:   1,
		ConsensusParams: types.DefaultConsensusParams(),
		Validators:      []types.GenesisValidator{{Address: pubKey.Address(), PubKey: pubKey, Power: 10}},
	}
	require.NoError(tb, genDoc.ValidateAndComplete())

	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(kvstore.NewInMemoryApplication()), proxy.NopMetrics())
	require.NoError(tb, proxyApp.Start())
	tb.Cleanup(func() { _ = proxyApp.Stop() })

	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{DiscardABCIResponses: false})
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	state, err := sm.MakeGenesisState(genDoc)
	require.NoError(tb, err)
	require.NoError(tb, stateStore.Save(state))

	mcfg := cmtcfg.DefaultMempoolConfig()
	mcfg.Broadcast = false
	mcfg.MaxTxBytes = EnvTestMaxTxBytes
	mp := cat.NewTxPool(logger, mcfg, proxyApp.Mempool(), 0)

	txIndexer := kv.NewTxIndex(dbm.NewMemDB())
	blockIndexer := blockidxkv.New(dbm.NewMemDB())
	blockExec := sm.NewBlockExecutor(stateStore, logger, proxyApp.Consensus(), mp, sm.EmptyEvidencePool{}, blockStore)

	lastCommit := &types.Commit{}
	for h := int64(1); h <= EnvTestHeight; h++ {
		txs := make([]types.Tx, EnvTestTxsPerBlock)
		for i := range txs {
			txs[i] = EnvTestTx(h, i)
		}
		block, parts, err := state.MakeBlock(h, types.MakeData(txs), lastCommit, nil, state.Validators.Proposer.Address)
		require.NoError(tb, err)
		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		vote, err := types.MakeVote(pv, genDoc.ChainID, 0, h, 0, cmtproto.PrecommitType, blockID, genTime.Add(time.Duration(h)*time.Second))
		require.NoError(tb, err)
		seenCommit := &types.Commit{Height: h, BlockID: blockID, Signatures: []types.CommitSig{vote.CommitSig()}}
		blockStore.SaveBlock(block, parts, seenCommit)

		state, err = blockExec.ApplyBlock(state, blockID, block, lastCommit)
		require.NoError(tb, err)

		resp, err := stateStore.LoadFinalizeBlockResponse(h)
		require.NoError(tb, err)
		for i, tx := range txs {
			require.NoError(tb, txIndexer.Index(&abci.TxResult{Height: h, Index: uint32(i), Tx: tx, Result: *resp.TxResults[i]})) //nolint:gosec
		}
		require.NoError(tb, blockIndexer.Index(types.EventDataNewBlockEvents{Height: h, Events: resp.Events, NumTxs: int64(len(txs))}))
		lastCommit = seenCommit
	}

	evpool, err := evidence.NewPool(dbm.NewMemDB(), stateStore, blockStore)
	require.NoError(tb, err)
	evpool.SetLogger(logger)

	eventBus := types.NewEventBus()
	eventBus.SetLogger(logger)
	require.NoError(tb, eventBus.Start())
	tb.Cleanup(func() { _ = eventBus.Stop() })

	env := &Environment{
		ProxyAppQuery:    proxyApp.Query(),
		ProxyAppMempool:  proxyApp.Mempool(),
		StateStore:       stateStore,
		BlockStore:       blockStore,
		EvidencePool:     evpool,
		ConsensusReactor: testConsensusReactor{},
		PubKey:           pubKey,
		GenDoc:           genDoc,
		TxIndexer:        txIndexer,
		BlockIndexer:     blockIndexer,
		EventBus:         eventBus,
		Mempool:          mp,
		Logger:           logger,
		Config:           *cmtcfg.DefaultRPCConfig(),
	}
	require.NoError(tb, env.InitGenesisChunks())
	return env
}

func TestNewEnvForTest(t *testing.T) {
	env := NewEnvForTest(t)
	ctx := &rpctypes.Context{}
	require.EqualValues(t, EnvTestHeight, env.BlockStore.Height())
	res, err := env.TxSearch(ctx, "tx.height>=1", false, nil, nil, "asc")
	require.NoError(t, err)
	require.Equal(t, EnvTestHeight*EnvTestTxsPerBlock, res.TotalCount)
	blocks, err := env.BlockSearch(ctx, "block.height>=1", nil, nil, "")
	require.NoError(t, err)
	require.Equal(t, EnvTestHeight, blocks.TotalCount)
	st, err := env.TxStatus(ctx, EnvTestTx(2, 1).Hash())
	require.NoError(t, err)
	require.Equal(t, TxStatusCommitted, st.Status)
	_, err = env.DataCommitment(ctx, 1, EnvTestHeight+1)
	require.NoError(t, err)
}
