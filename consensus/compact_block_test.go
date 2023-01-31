package consensus

import (
	"context"
	"fmt"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/tendermint/tm-db"

	abcicli "github.com/tendermint/tendermint/abci/client"
	"github.com/tendermint/tendermint/abci/example/kvstore"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/evidence"
	"github.com/tendermint/tendermint/libs/log"
	tmsync "github.com/tendermint/tendermint/libs/sync"
	mempl "github.com/tendermint/tendermint/mempool"
	mempoolv2 "github.com/tendermint/tendermint/mempool/cat"
	"github.com/tendermint/tendermint/p2p"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

func TestCompactBlocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	const nValidators = 4
	testName := "consensus_compact_block_test"
	tickerFunc := newMockTickerFunc(true)

	genDoc, privVals := randGenesisDoc(nValidators, false, 30)
	css := make([]*State, nValidators)
	memR := make([]*mempoolv2.Reactor, nValidators)
	mempools := make([]*mempoolv2.TxPool, nValidators)
	csR := make([]*Reactor, nValidators)
	subs := make([]types.Subscription, nValidators)
	bs := make([]*store.BlockStore, nValidators)

	for i := 0; i < nValidators; i++ {
		logger := consensusLogger().With("validator", i)
		stateDB := dbm.NewMemDB() // each state needs its own db
		stateStore := sm.NewStore(stateDB, sm.StoreOptions{
			DiscardABCIResponses: false,
		})
		state, _ := stateStore.LoadFromDBOrGenesisDoc(genDoc)
		require.NoError(t, stateStore.Save(state))
		thisConfig := ResetConfig(fmt.Sprintf("%s_%d", testName, i))
		thisConfig.Consensus.CompactBlocks = true
		defer os.RemoveAll(thisConfig.RootDir)
		ensureDir(path.Dir(thisConfig.Consensus.WalFile()), 0700) // dir for wal
		app := kvstore.NewApplication()
		vals := types.TM2PB.ValidatorUpdates(state.Validators)
		app.InitChain(abci.RequestInitChain{Validators: vals})

		blockDB := dbm.NewMemDB()
		blockStore := store.NewBlockStore(blockDB)
		bs[i] = blockStore

		mtx := new(tmsync.Mutex)
		// one for mempool, one for consensus
		proxyAppConnCon := abcicli.NewLocalClient(mtx, app)
		proxyAppConnConMem := abcicli.NewLocalClient(mtx, app)

		mempools[i] = mempoolv2.NewTxPool(
			log.NewNopLogger(),
			config.Mempool,
			proxyAppConnConMem,
			state.LastBlockHeight,
			mempoolv2.WithPreCheck(sm.TxPreCheck(state)),
			mempoolv2.WithPostCheck(sm.TxPostCheck(state)),
		)
		var err error
		memR[i], err = mempoolv2.NewReactor(mempools[i], &mempoolv2.ReactorOptions{
			MaxTxSize: thisConfig.Mempool.MaxTxBytes,
		})
		require.NoError(t, err)
		memR[i].SetLogger(logger.With("module", "mempool"))

		if thisConfig.Consensus.WaitForTxs() {
			mempool.EnableTxsAvailable()
		}

		// Make a full instance of the evidence pool
		evidenceDB := dbm.NewMemDB()
		evpool, err := evidence.NewPool(evidenceDB, stateStore, blockStore)
		require.NoError(t, err)
		// evpool.SetLogger(logger.With("module", "evidence"))

		// Make State
		blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyAppConnCon, mempools[i], evpool)
		cs := NewState(thisConfig.Consensus, state, blockExec, blockStore, mempools[i], memR[i], evpool)
		cs.SetLogger(cs.Logger)
		// set private validator
		pv := privVals[i]
		cs.SetPrivValidator(pv)

		eventBus := types.NewEventBus()
		// eventBus.SetLogger(logger.With("module", "events"))
		err = eventBus.Start()
		require.NoError(t, err)
		cs.SetEventBus(eventBus)

		cs.SetTimeoutTicker(tickerFunc())
		cs.SetLogger(logger)

		css[i] = cs

		csR[i] = NewReactor(cs, false)
		csR[i].SetLogger(logger)
		csR[i].SetEventBus(css[i].eventBus)

		subs[i], err = css[i].eventBus.Subscribe(ctx, testSubscriber, types.EventQueryNewBlockHeader, 30)
		require.NoError(t, err)
	}

	switches := p2p.MakeConnectedSwitches(config.P2P, nValidators, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("MEMPOOL", memR[i])
		s.AddReactor("CONSENSUS", csR[i])
		return s
	}, p2p.Connect2Switches)
	for _, sw := range switches {
		sw.SetLogger(log.NewNopLogger())
	}
	defer func() {
		for _, r := range csR {
			// this will also stop the mempool reactors
			r.Switch.Stop()
			r.eventBus.Stop()
		}
	}()

	numTxs := 100
	wg := new(sync.WaitGroup)
	wg.Add(1)
	// continually send txs to the first validator
	go func(ctx context.Context) {
		defer wg.Done()
		for i := 0; i < numTxs; i++ {
			if ctx.Err() != nil {
				return
			}
			tx := []byte(fmt.Sprintf("TX%d", i))
			require.NoError(t, mempools[0].CheckTx(tx, nil, mempl.TxInfo{}))
			time.Sleep(100 * time.Millisecond)
		}
		t.Log("finished sending all txs")
	}(ctx)
	for i := 0; i < nValidators; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			txCounter := 0
			blockCounter := 0
			for {
				select {
				case <-ctx.Done():
					t.Logf("received %d txs out of %d over %d blocks", txCounter, numTxs, blockCounter)
					return
				case msg := <-subs[i].Out():
					header := msg.Data().(types.EventDataNewBlockHeader)
					blockCounter++
					txCounter += int(header.NumTxs)
					if txCounter == numTxs {
						return
					}
					block := bs[i].LoadBlock(header.Header.Height)
					require.NotNil(t, block)
					require.Equal(t, header.Header.DataHash, block.Data.Hash())
				}
			}
		}(i)
	}
	wg.Wait()
	require.NoError(t, ctx.Err())

}
