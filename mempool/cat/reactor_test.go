package cat

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log/term"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	db "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/internal/test"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"

	cfg "github.com/cometbft/cometbft/config"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

type peerState struct {
	height int64
}

func (ps peerState) GetHeight() int64 {
	return ps.height
}

// Send a bunch of txs to the first reactor's mempool and wait for them all to
// be received in the others.
func TestReactorBroadcastTxsMessage(t *testing.T) {
	config := cfg.TestConfig()
	const N = 20
	reactors := makeAndConnectReactors(t, config, N)

	txs := checkTxs(t, reactors[0].mempool, 10, mempool.UnknownPeerID)
	sort.Slice(txs, func(i, j int) bool {
		return txs[i].priority > txs[j].priority // N.B. higher priorities first
	})
	transactions := make(types.Txs, len(txs))
	for idx, tx := range txs {
		transactions[idx] = tx.tx
	}

	waitForTxsOnReactors(t, transactions, reactors)
}

func TestReactorSendWantTxAfterReceivingSeenTx(t *testing.T) {
	reactor, _ := setupReactor(t)

	tx := newDefaultTx("hello")
	key := tx.Key()
	msgSeen := &protomem.SeenTx{TxKey: key[:]}

	msgWant := &protomem.Message{
		Sum: &protomem.Message_WantTx{WantTx: &protomem.WantTx{TxKey: key[:]}},
	}

	peer := genPeer()
	env := p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message:   msgWant,
	}
	peer.On("TrySend", env).Return(true)

	_, err := reactor.InitPeer(peer)
	require.NoError(t, err)
	reactor.Receive(
		p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message:   msgSeen,
			Src:       peer,
		},
	)

	peer.AssertExpectations(t)
}

func TestReactorSendsTxAfterReceivingWantTx(t *testing.T) {
	reactor, pool := setupReactor(t)

	tx := newDefaultTx("hello")
	key := tx.Key()
	txEnvelope := p2p.Envelope{
		Message:   &protomem.Txs{Txs: [][]byte{tx}},
		ChannelID: MempoolDataChannel,
	}

	msgWant := &protomem.WantTx{TxKey: key[:]}

	peer := genPeer()
	peer.On("Send", txEnvelope).Return(true)

	// add the transaction to the nodes pool. It's not connected to
	// any peers so it shouldn't broadcast anything yet
	require.NoError(t, pool.CheckTx(tx, nil, mempool.TxInfo{}))

	// Add the peer
	_, err := reactor.InitPeer(peer)
	require.NoError(t, err)
	// The peer sends a want msg for this tx
	reactor.Receive(
		p2p.Envelope{
			ChannelID: MempoolWantsChannel,
			Message:   msgWant,
			Src:       peer,
		},
	)

	// Should send the tx to the peer in response
	peer.AssertExpectations(t)

	// pool should have marked the peer as having seen the tx
	peerID := reactor.ids.GetIDForPeer(peer.ID())
	require.True(t, pool.seenByPeersSet.Has(key, peerID))
}

func TestReactorBroadcastsSeenTxAfterReceivingTx(t *testing.T) {
	reactor, _ := setupReactor(t)

	tx := newDefaultTx("hello")
	key := tx.Key()
	txMsg := &protomem.Txs{Txs: [][]byte{tx}}

	seenMsg := &protomem.Message{
		Sum: &protomem.Message_SeenTx{SeenTx: &protomem.SeenTx{
			TxKey:    key[:],
			Signer:   []byte("sender-000-0"),
			Sequence: 0,
		}},
	}

	peers := genPeers(2)
	// only peer 1 should receive the seen tx message as peer 0 broadcasted
	// the transaction in the first place
	env := p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   seenMsg,
	}
	peers[1].On("Send", env).Return(true)

	_, err := reactor.InitPeer(peers[0])
	require.NoError(t, err)
	_, err = reactor.InitPeer(peers[1])
	require.NoError(t, err)
	reactor.Receive(
		p2p.Envelope{
			ChannelID: mempool.MempoolChannel,
			Message:   txMsg,
			Src:       peers[0],
		},
	)

	peers[0].AssertExpectations(t)
	peers[1].AssertExpectations(t)
}

func TestRemovePeerRequestFromOtherPeer(t *testing.T) {
	reactor, _ := setupReactor(t)

	tx := newDefaultTx("hello")
	key := tx.Key()
	peers := genPeers(2)
	_, err := reactor.InitPeer(peers[0])
	require.NoError(t, err)
	_, err = reactor.InitPeer(peers[1])
	require.NoError(t, err)

	seenMsg := &protomem.SeenTx{TxKey: key[:]}

	wantMsg := &protomem.Message{
		Sum: &protomem.Message_WantTx{WantTx: &protomem.WantTx{TxKey: key[:]}},
	}
	env := p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message:   wantMsg,
	}
	peers[0].On("TrySend", env).Return(true)
	peers[1].On("TrySend", env).Return(true)

	reactor.Receive(p2p.Envelope{
		Src:       peers[0],
		Message:   seenMsg,
		ChannelID: MempoolDataChannel,
	})
	time.Sleep(100 * time.Millisecond)
	reactor.Receive(p2p.Envelope{
		Src:       peers[1],
		Message:   seenMsg,
		ChannelID: MempoolDataChannel,
	})

	reactor.RemovePeer(peers[0], "test")

	peers[0].AssertExpectations(t)
	peers[1].AssertExpectations(t)

	require.True(t, reactor.mempool.seenByPeersSet.Has(key, 2))
	// we should have automatically sent another request out for peer 2
	require.EqualValues(t, 2, reactor.requests.ForTx(key))
	require.True(t, reactor.requests.Has(2, key))
	require.False(t, reactor.mempool.seenByPeersSet.Has(key, 1))
}

func TestMempoolVectors(t *testing.T) {
	testCases := []struct {
		testName string
		tx       []byte
		expBytes string
	}{
		{"tx 1", []byte{123}, "0a030a017b"},
		{"tx 2", []byte("proto encoding in mempool"), "0a1b0a1970726f746f20656e636f64696e6720696e206d656d706f6f6c"},
	}

	for _, tc := range testCases {
		tc := tc

		msg := protomem.Message{
			Sum: &protomem.Message_Txs{
				Txs: &protomem.Txs{Txs: [][]byte{tc.tx}},
			},
		}
		bz, err := msg.Marshal()
		require.NoError(t, err, tc.testName)

		require.Equal(t, tc.expBytes, hex.EncodeToString(bz), tc.testName)
	}
}

func TestLegacyReactorReceiveBasic(t *testing.T) {
	config := cfg.TestConfig()
	// if there were more than two reactors, the order of transactions could not be
	// asserted in waitForTxsOnReactors (due to transactions gossiping). If we
	// replace Connect2Switches (full mesh) with a func, which connects first
	// reactor to others and nothing else, this test should also pass with >2 reactors.
	const N = 1
	reactors := makeAndConnectReactors(t, config, N)
	var (
		reactor = reactors[0]
		peer    = p2pmock.NewPeer(nil)
	)
	defer func() {
		err := reactor.Stop()
		assert.NoError(t, err)
	}()

	_, err := reactor.InitPeer(peer)
	require.NoError(t, err)
	reactor.AddPeer(peer)

	msg := &protomem.Message{
		Sum: &protomem.Message_Txs{
			Txs: &protomem.Txs{Txs: [][]byte{}},
		},
	}

	assert.NotPanics(t, func() {
		reactor.Receive(
			p2p.Envelope{
				ChannelID: mempool.MempoolChannel,
				Message:   msg,
				Src:       peer,
			},
		)
	})
}

func TestReactorReceiveRejectedTx(t *testing.T) {
	reactor, _ := setupReactor(t)

	tx := newDefaultTx("rejected tx")
	txKey := tx.Key()
	peer := genPeer()

	// Add transaction to rejection cache to simulate it was previously rejected
	reactor.mempool.rejectedTxCache.Push(txKey, 1, "tx rejected")
	rejected, code, log := reactor.mempool.WasRecentlyRejected(txKey)
	assert.True(t, rejected)
	assert.Equal(t, uint32(1), code)
	assert.Equal(t, "tx rejected", log)

	// Send SeenTx message
	envelope := p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.SeenTx{TxKey: txKey[:]},
		Src:       peer,
	}

	// Expect WantTx to be sent back
	peer.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: txKey[:]},
			},
		},
	}).Return(true)

	_, err := reactor.InitPeer(peer)
	require.NoError(t, err)

	reactor.Receive(envelope)

	peer.AssertExpectations(t)
}

func TestTryRequestQueuedTxRequestsFirstPeerOnly(t *testing.T) {
	reactor, _ := setupReactor(t)

	tx := newDefaultTx("request-first-peer")
	txKey := tx.Key()
	wantEnv := p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: txKey[:]},
			},
		},
	}

	peers := genPeers(2)
	for _, peer := range peers {
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)
	}

	firstPeerID := reactor.ids.GetIDForPeer(peers[0].ID())
	secondPeerID := reactor.ids.GetIDForPeer(peers[1].ID())
	require.NotZero(t, firstPeerID)
	require.NotZero(t, secondPeerID)

	entry := &pendingSeenTx{
		txKey:    txKey,
		peers:    []uint16{firstPeerID, secondPeerID},
		signer:   []byte("signer"),
		sequence: 1,
	}

	peers[0].On("TrySend", wantEnv).Return(true).Once()

	require.True(t, reactor.tryRequestQueuedTx(entry))

	peers[0].AssertExpectations(t)
	peers[1].AssertNotCalled(t, "TrySend", mock.Anything)
}

func TestDefaultGossipDelay(t *testing.T) {
	// Test that DefaultGossipDelay is set to the expected value
	expectedDelay := 60 * time.Second
	assert.Equal(t, expectedDelay, DefaultGossipDelay, "DefaultGossipDelay should be 60 seconds")
}

func TestReactorOptionsVerifyAndComplete(t *testing.T) {
	tests := []struct {
		name     string
		opts     ReactorOptions
		expected ReactorOptions
		wantErr  bool
	}{
		{
			name: "default options should use DefaultGossipDelay",
			opts: ReactorOptions{},
			expected: ReactorOptions{
				MaxTxSize:      cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay: DefaultGossipDelay,
			},
			wantErr: false,
		},
		{
			name: "custom MaxGossipDelay should be preserved",
			opts: ReactorOptions{
				MaxGossipDelay: 30 * time.Second,
			},
			expected: ReactorOptions{
				MaxTxSize:      cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay: 30 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "negative MaxGossipDelay should return error",
			opts: ReactorOptions{
				MaxGossipDelay: -1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "negative MaxTxSize should return error",
			opts: ReactorOptions{
				MaxTxSize: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.VerifyAndComplete()
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected.MaxTxSize, tt.opts.MaxTxSize)
			assert.Equal(t, tt.expected.MaxGossipDelay, tt.opts.MaxGossipDelay)
		})
	}
}

func setupReactor(t *testing.T) (*Reactor, *TxPool) {
	app := &application{kvstore.NewApplication(db.NewMemDB())}
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	t.Cleanup(cleanup)
	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)
	return reactor, pool
}

func makeAndConnectReactors(t *testing.T, config *cfg.Config, n int) []*Reactor {
	reactors := make([]*Reactor, n)
	logger := mempoolLogger()
	for i := 0; i < n; i++ {
		var pool *TxPool
		reactors[i], pool = setupReactor(t)
		reactors[i].SetStickySalt([]byte{byte(i + 1)})
		pool.logger = logger.With("validator", i)
		reactors[i].SetLogger(logger.With("validator", i))
	}

	switches := p2p.MakeConnectedSwitches(config.P2P, n, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("MEMPOOL", reactors[i])
		return s
	}, p2p.Connect2Switches)

	t.Cleanup(func() {
		for _, s := range switches {
			if err := s.Stop(); err != nil {
				assert.NoError(t, err)
			}
		}
	})

	for _, r := range reactors {
		for _, peer := range r.Switch.Peers().List() {
			peer.Set(types.PeerStateKey, peerState{1})
		}
	}
	return reactors
}

// mempoolLogger is a TestingLogger which uses a different
// color for each validator ("validator" key must exist).
func mempoolLogger() log.Logger {
	return log.TestingLoggerWithColorFn(func(keyvals ...interface{}) term.FgBgColor {
		for i := 0; i < len(keyvals)-1; i += 2 {
			if keyvals[i] == "validator" {
				return term.FgBgColor{Fg: term.Color(uint8(keyvals[i+1].(int) + 1))}
			}
		}
		return term.FgBgColor{}
	})
}

func newMempoolWithApp(cc proxy.ClientCreator) (*TxPool, func()) {
	conf := test.ResetTestRoot("mempool_test")

	mp, cu := newMempoolWithAppAndConfig(cc, conf)
	return mp, cu
}

func newMempoolWithAppAndConfig(cc proxy.ClientCreator, conf *cfg.Config) (*TxPool, func()) {
	appConnMem, _ := cc.NewABCIClient()
	appConnMem.SetLogger(log.TestingLogger().With("module", "abci-client", "connection", "mempool"))
	err := appConnMem.Start()
	if err != nil {
		panic(err)
	}

	mp := NewTxPool(log.TestingLogger(), conf.Mempool, appConnMem, 1)

	return mp, func() { os.RemoveAll(conf.RootDir) }
}

func waitForTxsOnReactors(t *testing.T, txs types.Txs, reactors []*Reactor) {
	// wait for the txs in all mempools
	wg := new(sync.WaitGroup)
	for i, reactor := range reactors {
		wg.Add(1)
		go func(r *Reactor, reactorIndex int) {
			defer wg.Done()
			waitForTxsOnReactor(t, types.CachedTxFromTxs(txs), r, reactorIndex)
		}(reactor, i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	timer := time.After(120 * time.Second)
	select {
	case <-timer:
		t.Fatal("Timed out waiting for txs")
	case <-done:
	}
}

func waitForTxsOnReactor(t *testing.T, txs []*types.CachedTx, reactor *Reactor, reactorIndex int) {
	mempool := reactor.mempool
	for mempool.Size() < len(txs) {
		time.Sleep(time.Millisecond * 100)
	}

	reapedTxs := mempool.ReapMaxTxs(len(txs))
	for i, tx := range txs {
		_ = tx.Hash() // to set the hash field in the cached tx
		require.Contains(t, reapedTxs, tx)
		require.Equal(t, tx, reapedTxs[i],
			"txs at index %d on reactor %d don't match: %x vs %x", i, reactorIndex, tx, reapedTxs[i])
	}
}

func genPeers(n int) []*mocks.Peer {
	peers := make([]*mocks.Peer, n)
	for i := 0; i < n; i++ {
		peers[i] = genPeer()
	}
	return peers

}

func genPeer() *mocks.Peer {
	peer := &mocks.Peer{}
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	peer.On("ID").Return(nodeKey.ID())
	peer.On("Get", types.PeerStateKey).Return(nil).Maybe()
	return peer
}

// sequenceTrackingApp is a test application that implements SequenceQuerier
type sequenceTrackingApp struct {
	*kvstore.Application
	mtx        sync.Mutex
	sequences  map[string]uint64
	queryCalls int
}

func newSequenceTrackingApp() *sequenceTrackingApp {
	return &sequenceTrackingApp{
		Application: kvstore.NewApplication(db.NewMemDB()),
		sequences:   make(map[string]uint64),
	}
}

func (app *sequenceTrackingApp) CheckTx(ctx context.Context, req *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	var (
		priority int64
		sender   string
	)

	parts := bytes.Split(req.Tx, []byte("="))
	if len(parts) == 3 {
		v, err := strconv.ParseInt(string(parts[2]), 10, 64)
		if err != nil {
			return &abcitypes.ResponseCheckTx{
				Priority:  priority,
				Code:      100,
				GasWanted: 1,
			}, nil
		}
		priority = v
		sender = string(parts[0])
	} else {
		return &abcitypes.ResponseCheckTx{
			Priority:  priority,
			Code:      101,
			GasWanted: 1,
			Log:       "invalid-tx-format",
		}, nil
	}

	return &abcitypes.ResponseCheckTx{
		Priority:  priority,
		Address:   []byte(sender),
		Code:      abcitypes.CodeTypeOK,
		GasWanted: 1,
	}, nil
}

func (app *sequenceTrackingApp) QuerySequence(ctx context.Context, req *abcitypes.RequestQuerySequence) (*abcitypes.ResponseQuerySequence, error) {
	app.mtx.Lock()
	defer app.mtx.Unlock()

	app.queryCalls++
	sequence := app.sequences[string(req.Signer)]
	return &abcitypes.ResponseQuerySequence{Sequence: sequence}, nil
}

func (app *sequenceTrackingApp) SetSequence(signer string, sequence uint64) {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	app.sequences[signer] = sequence
}

func (app *sequenceTrackingApp) ResetQueryCount() {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	app.queryCalls = 0
}

func (app *sequenceTrackingApp) QueryCallCount() int {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	return app.queryCalls
}

func TestReactorSequenceValidation(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("test-signer")

	t.Run("matching sequence requests tx", func(t *testing.T) {
		pool.Flush()
		app.ResetQueryCount()
		t.Cleanup(pool.Flush)
		t.Cleanup(app.ResetQueryCount)

		tx1 := newDefaultTx("test-tx-1")
		txKey1 := tx1.Key()

		// Set expected sequence in app
		app.SetSequence(string(signer), 5)

		peer := genPeer()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)

		// Expect WantTx to be sent
		peer.On("TrySend", p2p.Envelope{
			ChannelID: MempoolWantsChannel,
			Message: &protomem.Message{
				Sum: &protomem.Message_WantTx{
					WantTx: &protomem.WantTx{TxKey: txKey1[:]},
				},
			},
		}).Return(true)

		// Send SeenTx with matching sequence
		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    txKey1[:],
				Signer:   signer,
				Sequence: 5,
			},
			Src: peer,
		})

		peer.AssertExpectations(t)
		require.Equal(t, 1, app.QueryCallCount())
	})

	t.Run("mismatched sequence skips tx request", func(t *testing.T) {
		pool.Flush()
		app.ResetQueryCount()
		t.Cleanup(pool.Flush)
		t.Cleanup(app.ResetQueryCount)

		tx2 := newDefaultTx("test-tx-2")
		txKey2 := tx2.Key()

		// Set expected sequence in app
		app.SetSequence(string(signer), 10)

		peer := genPeer()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)

		// Should NOT expect WantTx to be sent (sequence mismatch)
		// Don't set up any expectations on the peer mock

		// Send SeenTx with mismatched sequence
		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    txKey2[:],
				Signer:   signer,
				Sequence: 7, // mismatch with expected 10
			},
			Src: peer,
		})

		// Verify no WantTx was sent
		peer.AssertExpectations(t)
		require.Equal(t, 1, app.QueryCallCount())
	})

	t.Run("no sequence info requests tx", func(t *testing.T) {
		pool.Flush()
		app.ResetQueryCount()
		t.Cleanup(pool.Flush)
		t.Cleanup(app.ResetQueryCount)

		tx3 := newDefaultTx("test-tx-3")
		txKey3 := tx3.Key()

		peer := genPeer()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)

		// Expect WantTx to be sent (backward compatibility)
		peer.On("TrySend", p2p.Envelope{
			ChannelID: MempoolWantsChannel,
			Message: &protomem.Message{
				Sum: &protomem.Message_WantTx{
					WantTx: &protomem.WantTx{TxKey: txKey3[:]},
				},
			},
		}).Return(true)

		// Send SeenTx without sequence info
		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    txKey3[:],
				Signer:   nil,
				Sequence: 0,
			},
			Src: peer,
		})

		peer.AssertExpectations(t)
		require.Equal(t, 0, app.QueryCallCount())
	})

	t.Run("local sequence match uses mempool state", func(t *testing.T) {
		pool.Flush()
		app.ResetQueryCount()
		t.Cleanup(pool.Flush)
		t.Cleanup(app.ResetQueryCount)

		existingTx := newDefaultTx("local-seq-match-existing")
		localWtx := newWrappedTx(existingTx.ToCachedTx(), pool.Height(), 1, 1, signer, 5)
		require.True(t, pool.store.set(localWtx))

		targetTx := newDefaultTx("local-seq-match-target")
		txKey := targetTx.Key()

		peer := genPeer()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)

		peer.On("TrySend", p2p.Envelope{
			ChannelID: MempoolWantsChannel,
			Message: &protomem.Message{
				Sum: &protomem.Message_WantTx{
					WantTx: &protomem.WantTx{TxKey: txKey[:]},
				},
			},
		}).Return(true)

		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    txKey[:],
				Signer:   signer,
				Sequence: 5,
			},
			Src: peer,
		})

		peer.AssertExpectations(t)
		require.Equal(t, 0, app.QueryCallCount())
	})

	t.Run("local sequence mismatch skips tx request", func(t *testing.T) {
		pool.Flush()
		app.ResetQueryCount()
		t.Cleanup(pool.Flush)
		t.Cleanup(app.ResetQueryCount)

		existingTx := newDefaultTx("local-seq-mismatch-existing")
		localWtx := newWrappedTx(existingTx.ToCachedTx(), pool.Height(), 1, 1, signer, 10)
		require.True(t, pool.store.set(localWtx))

		targetTx := newDefaultTx("local-seq-mismatch-target")
		txKey := targetTx.Key()

		peer := genPeer()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)

		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    txKey[:],
				Signer:   signer,
				Sequence: 9,
			},
			Src: peer,
		})

		peer.AssertExpectations(t)
		require.Equal(t, 0, app.QueryCallCount())
	})
}

func TestReactorRequestsQueuedTxAfterSequenceBecomesAvailable(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	sourcePeer := genPeer()
	_, err = reactor.InitPeer(sourcePeer)
	require.NoError(t, err)
	sourcePeer.On("Send", mock.Anything).Return(true).Maybe()

	targetTx := newDefaultTx("future-tx")
	targetKey := targetTx.Key()

	wantEnvelope := p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: targetKey[:]},
			},
		},
	}
	sourcePeer.On("TrySend", wantEnvelope).Return(true)

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    targetKey[:],
			Signer:   signer,
			Sequence: 2,
		},
		Src: sourcePeer,
	})

	require.Len(t, reactor.pendingSeen.entriesForSigner(signer), 1)

	providerPeer := genPeer()
	_, err = reactor.InitPeer(providerPeer)
	require.NoError(t, err)
	providerPeer.On("Send", mock.Anything).Return(true).Maybe()

	app.SetSequence(string(signer), 2)

	priorTx := newDefaultTx("prior-tx")
	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.Txs{Txs: [][]byte{priorTx}},
		Src:       providerPeer,
	})

	sourcePeer.AssertExpectations(t)
	require.Empty(t, reactor.pendingSeen.entriesForSigner(signer))
	require.EqualValues(t, reactor.ids.GetIDForPeer(sourcePeer.ID()), reactor.requests.ForTx(targetKey))
}

func TestPendingSeenClearedWhenTxArrives(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	sourcePeer := genPeer()
	_, err = reactor.InitPeer(sourcePeer)
	require.NoError(t, err)
	sourcePeer.On("Send", mock.Anything).Return(true).Maybe()

	targetTx := newDefaultTx("future-tx")
	targetKey := targetTx.Key()

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    targetKey[:],
			Signer:   signer,
			Sequence: 2,
		},
		Src: sourcePeer,
	})

	require.Len(t, reactor.pendingSeen.entriesForSigner(signer), 1)

	providerPeer := genPeer()
	_, err = reactor.InitPeer(providerPeer)
	require.NoError(t, err)
	providerPeer.On("Send", mock.Anything).Return(true).Maybe()

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.Txs{Txs: [][]byte{targetTx}},
		Src:       providerPeer,
	})

	require.Empty(t, reactor.pendingSeen.entriesForSigner(signer))
	sourcePeer.AssertNotCalled(t, "TrySend", mock.Anything)
}

func TestPendingSeenClearedOnPeerRemoval(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	sourcePeer := genPeer()
	_, err = reactor.InitPeer(sourcePeer)
	require.NoError(t, err)

	targetTx := newDefaultTx("future-tx")
	targetKey := targetTx.Key()

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    targetKey[:],
			Signer:   signer,
			Sequence: 2,
		},
		Src: sourcePeer,
	})

	require.Len(t, reactor.pendingSeen.entriesForSigner(signer), 1)

	reactor.RemovePeer(sourcePeer, "disconnect")

	require.Empty(t, reactor.pendingSeen.entriesForSigner(signer))
}

func TestPendingSeenDroppedWhenSequenceAdvances(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	sourcePeer := genPeer()
	_, err = reactor.InitPeer(sourcePeer)
	require.NoError(t, err)
	sourcePeer.On("TrySend", mock.Anything).Return(true).Maybe()
	sourcePeer.On("Send", mock.Anything).Return(true).Maybe()

	targetTx := newDefaultTx("future-tx")
	targetKey := targetTx.Key()

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    targetKey[:],
			Signer:   signer,
			Sequence: 2,
		},
		Src: sourcePeer,
	})

	require.Len(t, reactor.pendingSeen.entriesForSigner(signer), 1)

	app.SetSequence(string(signer), 4)
	reactor.processPendingSeenForSigner(signer)

	require.Empty(t, reactor.pendingSeen.entriesForSigner(signer))
	sourcePeer.AssertNotCalled(t, "TrySend", mock.Anything)
}
