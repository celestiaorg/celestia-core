package cat

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
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

// When two txs share a priority, gossip order across reactors is
// non-deterministic, so waitForTxsOnReactor must verify set membership rather
// than per-index equality. See issue #2945.
func TestWaitForTxsOnReactor_AcceptsArbitraryOrderForTiedPriorities(t *testing.T) {
	reactor, pool := setupReactor(t)
	t.Cleanup(func() { _ = reactor.Stop() })

	const sharedPriority = int64(5)
	txA := types.Tx(newTx(0, mempool.UnknownPeerID, []byte("A"), sharedPriority))
	txB := types.Tx(newTx(1, mempool.UnknownPeerID, []byte("B"), sharedPriority))

	require.NoError(t, pool.CheckTx(txA, nil, mempool.TxInfo{}))
	require.NoError(t, pool.CheckTx(txB, nil, mempool.TxInfo{}))

	// Pass expected in opposite order from insertion to exercise the
	// order-agnostic comparison.
	expected := types.CachedTxFromTxs(types.Txs{txB, txA})
	waitForTxsOnReactor(t, expected, reactor, 0)
}

func TestReactorSendWantTxAfterReceivingSeenTx(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	t.Cleanup(cleanup)
	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	tx := newDefaultTx("hello")
	key := tx.Key()
	signer := []byte("test-signer")
	app.SetSequence(string(signer), 1)
	msgSeen := &protomem.SeenTx{TxKey: key[:], Signer: signer, Sequence: 1}

	msgWant := &protomem.Message{
		Sum: &protomem.Message_WantTx{WantTx: &protomem.WantTx{TxKey: key[:]}},
	}

	peer := genPeer()
	env := p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message:   msgWant,
	}
	peer.On("TrySend", env).Return(true)

	_, err = reactor.InitPeer(peer)
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
	require.True(t, pool.seenTracker.Has(key, peerID))
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
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	t.Cleanup(cleanup)
	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	tx := newDefaultTx("hello")
	key := tx.Key()
	signer := []byte("test-signer")
	app.SetSequence(string(signer), 1)
	peers := genPeers(2)
	_, err = reactor.InitPeer(peers[0])
	require.NoError(t, err)
	_, err = reactor.InitPeer(peers[1])
	require.NoError(t, err)

	seenMsg := &protomem.SeenTx{TxKey: key[:], Signer: signer, Sequence: 1}

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

	require.True(t, reactor.mempool.seenTracker.Has(key, 2))
	// we should have automatically sent another request out for peer 2
	require.EqualValues(t, 2, reactor.requests.ForTx(key))
	require.True(t, reactor.requests.Has(2, key))
	require.False(t, reactor.mempool.seenTracker.Has(key, 1))
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
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	t.Cleanup(cleanup)
	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	tx := newDefaultTx("rejected tx")
	txKey := tx.Key()
	signer := []byte("test-signer")
	app.SetSequence(string(signer), 1)
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
		Message:   &protomem.SeenTx{TxKey: txKey[:], Signer: signer, Sequence: 1},
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

	_, err = reactor.InitPeer(peer)
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

	entry := &SeenEntry{
		txKey: txKey,
		peers: map[uint16]struct{}{firstPeerID: {}},
		pendingTxInfo: &PendingTxInfo{
			signer:   []byte("signer"),
			sequence: 1,
		},
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
				MaxTxSize:                cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay:           DefaultGossipDelay,
				MaxPersistentStickyPeers: defaultMaxPersistentStickyPeers,
			},
			wantErr: false,
		},
		{
			name: "custom MaxGossipDelay should be preserved",
			opts: ReactorOptions{
				MaxGossipDelay: 30 * time.Second,
			},
			expected: ReactorOptions{
				MaxTxSize:                cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay:           30 * time.Second,
				MaxPersistentStickyPeers: defaultMaxPersistentStickyPeers,
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
		{
			name: "unset MaxPersistentStickyPeers falls back to default",
			opts: ReactorOptions{},
			expected: ReactorOptions{
				MaxTxSize:                cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay:           DefaultGossipDelay,
				MaxPersistentStickyPeers: defaultMaxPersistentStickyPeers,
			},
			wantErr: false,
		},
		{
			name: "negative MaxPersistentStickyPeers falls back to default",
			opts: ReactorOptions{MaxPersistentStickyPeers: -3},
			expected: ReactorOptions{
				MaxTxSize:                cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay:           DefaultGossipDelay,
				MaxPersistentStickyPeers: defaultMaxPersistentStickyPeers,
			},
			wantErr: false,
		},
		{
			name: "custom MaxPersistentStickyPeers is preserved",
			opts: ReactorOptions{MaxPersistentStickyPeers: 7},
			expected: ReactorOptions{
				MaxTxSize:                cfg.DefaultMempoolConfig().MaxTxBytes,
				MaxGossipDelay:           DefaultGossipDelay,
				MaxPersistentStickyPeers: 7,
			},
			wantErr: false,
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
			assert.Equal(t, tt.expected.MaxPersistentStickyPeers, tt.opts.MaxPersistentStickyPeers)
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
	require.Equal(t, len(txs), len(reapedTxs),
		"reactor %d: expected %d txs, got %d", reactorIndex, len(txs), len(reapedTxs))
	// Compare as a set: across reactors, txs with equal priority can be reaped
	// in different orders depending on gossip arrival, so per-index equality is
	// non-deterministic. See issue #2945.
	for _, tx := range txs {
		_ = tx.Hash() // to set the hash field in the cached tx
		require.Contains(t, reapedTxs, tx,
			"reactor %d: missing expected tx %x", reactorIndex, tx)
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
	peer.On("IsPersistent").Return(false).Maybe()
	return peer
}

// sequenceTrackingApp is a test application that implements SequenceQuerier
type sequenceTrackingApp struct {
	*kvstore.Application
	mtx       sync.Mutex
	sequences map[string]uint64
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

	sequence := app.sequences[string(req.Signer)]
	return &abcitypes.ResponseQuerySequence{Sequence: sequence}, nil
}

func (app *sequenceTrackingApp) SetSequence(signer string, sequence uint64) {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	app.sequences[signer] = sequence
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
		t.Cleanup(pool.Flush)
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
	})

	t.Run("mismatched sequence skips tx request", func(t *testing.T) {
		pool.Flush()
		t.Cleanup(pool.Flush)
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
	})

	t.Run("future sequence is saved without tx request", func(t *testing.T) {
		pool.Flush()
		t.Cleanup(pool.Flush)
		tx4 := newDefaultTx("test-tx-4")
		txKey4 := tx4.Key()

		app.SetSequence(string(signer), 10)

		peer := genPeer()
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)

		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    txKey4[:],
				Signer:   signer,
				Sequence: 11,
			},
			Src: peer,
		})

		peer.AssertExpectations(t)
		require.Zero(t, reactor.requests.ForTx(txKey4))

		entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
		require.Len(t, entries, 1)
		require.Equal(t, txKey4, entries[0].txKey)
		require.Equal(t, signer, entries[0].pendingTxInfo.signer)
		require.Equal(t, uint64(11), entries[0].pendingTxInfo.sequence)
		require.Equal(t, []uint16{reactor.ids.GetIDForPeer(peer.ID())}, entries[0].peerIDs())
	})
}

// Checks that an unsolicited tx is first tracked as a peer-only entry,
//
//	then upgraded to pending when a later SeenTx has a future signer/sequence.
func TestSeenTxUpgradesPeerEntryToPending(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)

	tx := types.Tx("invalid-tx-format")
	txKey := tx.Key()
	peerID := reactor.ids.GetIDForPeer(peer.ID())

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.Txs{Txs: [][]byte{tx}},
		Src:       peer,
	})

	require.False(t, pool.Has(txKey))

	entry := reactor.mempool.seenTracker.Get(txKey)
	require.NotNil(t, entry)
	require.Equal(t, map[uint16]struct{}{peerID: {}}, entry.peers)
	require.Nil(t, entry.pendingTxInfo)
	require.Empty(t, reactor.mempool.seenTracker.PendingTxsForSigner([]byte("sender")))

	signer := []byte("sender")
	app.SetSequence(string(signer), 5)

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    txKey[:],
			Signer:   signer,
			Sequence: 6,
		},
		Src: peer,
	})

	peer.AssertExpectations(t)
	require.Zero(t, reactor.requests.ForTx(txKey))

	entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
	require.Len(t, entries, 1)
	require.Equal(t, txKey, entries[0].txKey)
	require.Equal(t, signer, entries[0].pendingTxInfo.signer)
	require.Equal(t, uint64(6), entries[0].pendingTxInfo.sequence)
	require.Equal(t, []uint16{peerID}, entries[0].peerIDs())
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

	require.Len(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer), 1)

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

	require.Eventually(t, func() bool {
		return reactor.requests.ForTx(targetKey) != 0
	}, time.Second, 10*time.Millisecond)

	sourcePeer.AssertExpectations(t)
	entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
	require.Len(t, entries, 1)
	require.True(t, entries[0].requested)
	require.Equal(t, reactor.ids.GetIDForPeer(sourcePeer.ID()), entries[0].lastPeer)
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

	require.Len(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer), 1)

	providerPeer := genPeer()
	_, err = reactor.InitPeer(providerPeer)
	require.NoError(t, err)
	providerPeer.On("Send", mock.Anything).Return(true).Maybe()

	reactor.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message:   &protomem.Txs{Txs: [][]byte{targetTx}},
		Src:       providerPeer,
	})

	require.Empty(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer))
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

	require.Len(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer), 1)

	reactor.RemovePeer(sourcePeer, "disconnect")

	// With its only peer gone, the pending entry is dropped entirely.
	require.Empty(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer))
	require.Nil(t, reactor.mempool.seenTracker.Get(targetKey))
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

	require.Len(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer), 1)

	app.SetSequence(string(signer), 4)
	reactor.processPendingSeenForSigner(signer)

	require.Empty(t, reactor.mempool.seenTracker.PendingTxsForSigner(signer))
	sourcePeer.AssertNotCalled(t, "TrySend", mock.Anything)

}

func TestProcessPendingSeenForSignerRequestsConsecutiveSequences(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 5)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)

	// Add consecutive sequences 5, 6, 7 to pending
	txs := make([]types.Tx, 3)
	keys := make([]types.TxKey, 3)
	for i := 0; i < 3; i++ {
		txs[i] = newDefaultTx("tx-" + strconv.Itoa(i))
		keys[i] = txs[i].Key()
		peerID := reactor.ids.GetIDForPeer(peer.ID())
		reactor.mempool.seenTracker.AddPendingTx(keys[i], peerID, signer, uint64(5+i))
	}

	// Set up expectations for WantTx messages
	for _, key := range keys {
		peer.On("TrySend", p2p.Envelope{
			ChannelID: MempoolWantsChannel,
			Message: &protomem.Message{
				Sum: &protomem.Message_WantTx{
					WantTx: &protomem.WantTx{TxKey: key[:]},
				},
			},
		}).Return(true).Once()
	}

	reactor.processPendingSeenForSigner(signer)

	peer.AssertExpectations(t)

	// All entries should be marked as requested
	entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
	require.Len(t, entries, 3)
	for _, entry := range entries {
		require.True(t, entry.requested, "entry for seq %d should be marked requested", entry.pendingTxInfo.sequence)
	}
}

func TestProcessPendingSeenForSignerStopsAtGap(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 5)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)

	// Add sequences 5, 6, 8 (gap at 7)
	tx5 := newDefaultTx("tx-5")
	tx6 := newDefaultTx("tx-6")
	tx8 := newDefaultTx("tx-8")
	key5 := tx5.Key()
	key6 := tx6.Key()
	key8 := tx8.Key()

	peerID := reactor.ids.GetIDForPeer(peer.ID())
	reactor.mempool.seenTracker.AddPendingTx(key5, peerID, signer, 5)
	reactor.mempool.seenTracker.AddPendingTx(key6, peerID, signer, 6)
	reactor.mempool.seenTracker.AddPendingTx(key8, peerID, signer, 8) // gap - sequence 7 is missing

	// Only sequences 5 and 6 should be requested (stops at gap)
	peer.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: key5[:]},
			},
		},
	}).Return(true).Once()

	peer.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: key6[:]},
			},
		},
	}).Return(true).Once()

	reactor.processPendingSeenForSigner(signer)

	peer.AssertExpectations(t)

	// Entry for sequence 8 should NOT be requested
	entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
	for _, entry := range entries {
		if entry.pendingTxInfo.sequence == 8 {
			require.False(t, entry.requested, "entry for seq 8 should NOT be requested due to gap")
		}
	}
}

func TestProcessPendingSeenForSignerSkipsAlreadyInMempool(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 5)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)
	peer.On("Send", mock.Anything).Return(true).Maybe()

	// Add tx for sequence 5 to mempool first
	tx5 := newDefaultTx("tx-5")
	key5 := tx5.Key()
	err = pool.CheckTx(tx5, nil, mempool.TxInfo{})
	require.NoError(t, err)
	require.True(t, pool.Has(key5))

	// Add sequences 5, 6 to pending (5 is already in mempool)
	tx6 := newDefaultTx("tx-6")
	key6 := tx6.Key()

	peerID := reactor.ids.GetIDForPeer(peer.ID())
	reactor.mempool.seenTracker.AddPendingTx(key5, peerID, signer, 5)
	reactor.mempool.seenTracker.AddPendingTx(key6, peerID, signer, 6)

	// Only sequence 6 should be requested (5 is already in mempool)
	peer.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: key6[:]},
			},
		},
	}).Return(true).Once()

	reactor.processPendingSeenForSigner(signer)

	peer.AssertExpectations(t)

	// Sequence 5 should be removed from pending (was in mempool)
	entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
	for _, entry := range entries {
		require.NotEqual(t, uint64(5), entry.pendingTxInfo.sequence, "entry for seq 5 should be removed")
	}
}

func TestProcessPendingSeenForSignerRespectsMaxBuffer(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)

	// Add more than maxReceivedBufferSize consecutive sequences
	peerID := reactor.ids.GetIDForPeer(peer.ID())
	numTxs := maxReceivedBufferSize + 10
	for i := 0; i < numTxs; i++ {
		tx := newDefaultTx("tx-" + strconv.Itoa(i))
		reactor.mempool.seenTracker.AddPendingTx(tx.Key(), peerID, signer, uint64(1+i))
	}

	// Expect only maxReceivedBufferSize requests
	peer.On("TrySend", mock.Anything).Return(true).Maybe()

	reactor.processPendingSeenForSigner(signer)

	// Count how many were requested
	entries := reactor.mempool.seenTracker.PendingTxsForSigner(signer)
	requestedCount := 0
	for _, entry := range entries {
		if entry.requested {
			requestedCount++
		}
	}

	require.LessOrEqual(t, requestedCount, maxReceivedBufferSize,
		"should not request more than maxReceivedBufferSize transactions")
}

func TestProcessPendingSeenForSignerSkipsAlreadyRequested(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 5)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)

	tx5 := newDefaultTx("tx-5")
	tx6 := newDefaultTx("tx-6")
	key5 := tx5.Key()
	key6 := tx6.Key()

	peerID := reactor.ids.GetIDForPeer(peer.ID())
	reactor.mempool.seenTracker.AddPendingTx(key5, peerID, signer, 5)
	reactor.mempool.seenTracker.AddPendingTx(key6, peerID, signer, 6)

	// Mark sequence 5 as already requested
	reactor.mempool.seenTracker.MarkRequested(key5, peerID)

	// Only sequence 6 should be newly requested
	peer.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: key6[:]},
			},
		},
	}).Return(true).Once()

	reactor.processPendingSeenForSigner(signer)

	peer.AssertExpectations(t)
}

func TestTryRequestQueuedTxRespectsPerPeerLimit(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	// Create two peers
	peer1 := genPeer()
	peer2 := genPeer()
	_, err = reactor.InitPeer(peer1)
	require.NoError(t, err)
	_, err = reactor.InitPeer(peer2)
	require.NoError(t, err)

	peer1ID := reactor.ids.GetIDForPeer(peer1.ID())
	peer2ID := reactor.ids.GetIDForPeer(peer2.ID())

	// Fill peer1 to capacity with fake requests
	for i := 0; i < maxRequestsPerPeer; i++ {
		tx := types.Tx(fmt.Sprintf("fill-tx-%d", i))
		reactor.requests.Add(tx.Key(), peer1ID, nil)
	}
	require.Equal(t, maxRequestsPerPeer, reactor.requests.CountForPeer(peer1ID))

	// Create a pending entry that both peers have seen
	targetTx := newDefaultTx("target-tx")
	targetKey := targetTx.Key()

	// Add both peers to the entry (peer1 first, then peer2)
	reactor.mempool.seenTracker.AddPendingTx(targetKey, peer1ID, signer, 1)
	// Simulate peer2 also seeing the tx by adding it to seenTracker
	reactor.mempool.PeerHasTx(peer2ID, targetKey)

	entry := reactor.mempool.seenTracker.Get(targetKey)
	require.NotNil(t, entry)

	// peer1 is at capacity, so request should NOT go to peer1
	// peer2 should be tried via findNewPeerToRequestTx fallback
	peer2.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: targetKey[:]},
			},
		},
	}).Return(true).Once()

	result := reactor.tryRequestQueuedTx(entry)
	require.True(t, result)

	// Verify the request went to peer2, not peer1
	require.True(t, reactor.requests.Has(peer2ID, targetKey))
	require.False(t, reactor.requests.Has(peer1ID, targetKey))

	peer2.AssertExpectations(t)
}

func TestTryRequestQueuedTxFallsBackToAlternativePeer(t *testing.T) {
	app := newSequenceTrackingApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sender-000-0")
	app.SetSequence(string(signer), 1)

	// Create three peers
	peer1 := genPeer()
	peer2 := genPeer()
	peer3 := genPeer()
	_, err = reactor.InitPeer(peer1)
	require.NoError(t, err)
	_, err = reactor.InitPeer(peer2)
	require.NoError(t, err)
	_, err = reactor.InitPeer(peer3)
	require.NoError(t, err)

	peer1ID := reactor.ids.GetIDForPeer(peer1.ID())
	peer2ID := reactor.ids.GetIDForPeer(peer2.ID())
	peer3ID := reactor.ids.GetIDForPeer(peer3.ID())

	// Fill peer1 and peer2 to capacity
	for i := 0; i < maxRequestsPerPeer; i++ {
		tx1 := types.Tx(fmt.Sprintf("fill-tx-1-%d", i))
		tx2 := types.Tx(fmt.Sprintf("fill-tx-2-%d", i))
		reactor.requests.Add(tx1.Key(), peer1ID, nil)
		reactor.requests.Add(tx2.Key(), peer2ID, nil)
	}

	// Create a pending entry with peer1, peer2, and peer3
	targetTx := newDefaultTx("target-tx")
	targetKey := targetTx.Key()

	// Add all three peers to the pending entry
	reactor.mempool.seenTracker.AddPendingTx(targetKey, peer1ID, signer, 1)
	// We need to manually add peer2 and peer3 to the entry's peer list
	// Since add() only records the first peer, we simulate this by adding to seenTracker
	reactor.mempool.PeerHasTx(peer2ID, targetKey)
	reactor.mempool.PeerHasTx(peer3ID, targetKey)

	entry := reactor.mempool.seenTracker.Get(targetKey)
	require.NotNil(t, entry)

	// peer1 and peer2 are at capacity, so request should go to peer3
	peer3.On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantTx{
				WantTx: &protomem.WantTx{TxKey: targetKey[:]},
			},
		},
	}).Return(true).Once()

	result := reactor.tryRequestQueuedTx(entry)
	require.True(t, result)

	// Verify the request went to peer3
	require.True(t, reactor.requests.Has(peer3ID, targetKey))
	require.False(t, reactor.requests.Has(peer1ID, targetKey))
	require.False(t, reactor.requests.Has(peer2ID, targetKey))

	peer3.AssertExpectations(t)
}

func TestTxsWithTooManyTxsBansPeer(t *testing.T) {
	config := cfg.TestConfig()
	reactors := makeAndConnectReactors(t, config, 2)

	reactor0 := reactors[0]
	reactor1 := reactors[1]

	peers := reactor0.Switch.Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	// Build a Txs message with more than MaxTxsPerMessage transactions
	txs := make([][]byte, mempool.MaxTxsPerMessage+1)
	for i := range txs {
		txs[i] = []byte(fmt.Sprintf("tx-%d", i))
	}

	reactor0.Receive(p2p.Envelope{
		ChannelID: mempool.MempoolChannel,
		Message:   &protomem.Txs{Txs: txs},
		Src:       peer,
	})

	require.Eventually(t, func() bool {
		return len(reactor0.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "peer should be disconnected after sending too many txs")

	require.Eventually(t, func() bool {
		return len(reactor1.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "reactor1 should see peer disconnected")
}

// TestTxsBatchBansPeer verifies that sending a batch of >1 transactions in a
// single Txs message causes the peer to be disconnected. Transaction batching
// was disabled in https://github.com/tendermint/tendermint/pull/5800 so only
// a single transaction per message is expected.
func TestTxsBatchBansPeer(t *testing.T) {
	config := cfg.TestConfig()
	reactors := makeAndConnectReactors(t, config, 2)

	reactor0 := reactors[0]
	reactor1 := reactors[1]

	peers := reactor0.Switch.Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	// Send a Txs message containing 2 valid transactions (a batch).
	reactor0.Receive(p2p.Envelope{
		ChannelID: mempool.MempoolChannel,
		Message:   &protomem.Txs{Txs: [][]byte{{0x01}, {0x02}}},
		Src:       peer,
	})

	require.Eventually(t, func() bool {
		return len(reactor0.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "peer should be disconnected after sending a batch of txs")

	require.Eventually(t, func() bool {
		return len(reactor1.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "reactor1 should see peer disconnected")
}

func TestTxsWithEmptyTxBansPeer(t *testing.T) {
	config := cfg.TestConfig()
	reactors := makeAndConnectReactors(t, config, 2)

	reactor0 := reactors[0]
	reactor1 := reactors[1]

	peers := reactor0.Switch.Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	// Send a Txs message containing one zero-length transaction
	reactor0.Receive(p2p.Envelope{
		ChannelID: mempool.MempoolChannel,
		Message:   &protomem.Txs{Txs: [][]byte{{}}},
		Src:       peer,
	})

	require.Eventually(t, func() bool {
		return len(reactor0.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "peer should be disconnected after sending empty tx")

	require.Eventually(t, func() bool {
		return len(reactor1.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "reactor1 should see peer disconnected")
}

func TestSeenTxWithOversizedSignerBansPeer(t *testing.T) {
	config := cfg.TestConfig()
	reactors := makeAndConnectReactors(t, config, 2)

	reactor0 := reactors[0]
	reactor1 := reactors[1]

	// Get peer reference from reactor0's perspective
	peers := reactor0.Switch.Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	// Create a signer that exceeds the max length
	oversizedSigner := make([]byte, maxSignerLength+1)
	for i := range oversizedSigner {
		oversizedSigner[i] = byte(i % 256)
	}

	tx := newDefaultTx("test-tx")
	key := tx.Key()

	// Send SeenTx with oversized signer
	reactor0.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    key[:],
			Signer:   oversizedSigner,
			Sequence: 1,
		},
		Src: peer,
	})

	// The peer should be disconnected
	require.Eventually(t, func() bool {
		return len(reactor0.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "peer should be disconnected after sending oversized signer")

	// Verify reactor1 also sees the disconnect
	require.Eventually(t, func() bool {
		return len(reactor1.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "reactor1 should see peer disconnected")
}

func TestSeenTxWithValidSignerNotBanned(t *testing.T) {
	config := cfg.TestConfig()
	reactors := makeAndConnectReactors(t, config, 2)

	reactor0 := reactors[0]

	// Get peer reference from reactor0's perspective
	peers := reactor0.Switch.Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	// Create a valid signer (at max length)
	validSigner := make([]byte, maxSignerLength)
	for i := range validSigner {
		validSigner[i] = byte(i % 256)
	}

	tx := newDefaultTx("test-tx")
	key := tx.Key()

	// Send SeenTx with valid signer
	reactor0.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    key[:],
			Signer:   validSigner,
			Sequence: 1,
		},
		Src: peer,
	})

	// Give some time for any potential disconnect
	time.Sleep(100 * time.Millisecond)

	// The peer should NOT be disconnected
	require.Len(t, reactor0.Switch.Peers().List(), 1, "peer should not be disconnected for valid signer length")
}

func TestSeenTxWithEmptySignerDisconnects(t *testing.T) {
	config := cfg.TestConfig()
	reactors := makeAndConnectReactors(t, config, 2)

	reactor0 := reactors[0]

	// Get peer reference from reactor0's perspective
	peers := reactor0.Switch.Peers().List()
	require.Len(t, peers, 1)
	peer := peers[0]

	tx := newDefaultTx("test-tx")
	key := tx.Key()

	// Send SeenTx with an empty signer. A missing signer means the peer is
	// running a very old protocol version, so it should be disconnected.
	reactor0.Receive(p2p.Envelope{
		ChannelID: MempoolDataChannel,
		Message: &protomem.SeenTx{
			TxKey:    key[:],
			Signer:   nil,
			Sequence: 0,
		},
		Src: peer,
	})

	// Give the disconnect time to propagate.
	require.Eventually(t, func() bool {
		return len(reactor0.Switch.Peers().List()) == 0
	}, time.Second, 10*time.Millisecond, "peer should be disconnected for empty signer")
}

// TestSeenTxDirectPathEnforcesPerPeerRequestLimit verifies that the direct
// SeenTx→requestTx code path enforces the maxRequestsPerPeer limit. A single
// peer sending more than maxRequestsPerPeer unique SeenTx messages (with a
// valid signer and the expected sequence) should NOT result in more than
// maxRequestsPerPeer outstanding requests to that peer.
func TestSeenTxDirectPathEnforcesPerPeerRequestLimit(t *testing.T) {
	reactor, _ := setupReactor(t)

	peer := genPeer()
	// Allow unlimited WantTx sends (we're counting how many get through)
	peer.On("TrySend", mock.MatchedBy(func(env p2p.Envelope) bool {
		return env.ChannelID == MempoolWantsChannel
	})).Return(true)

	_, err := reactor.InitPeer(peer)
	require.NoError(t, err)
	peerID := reactor.ids.GetIDForPeer(peer.ID())

	// The test app's QuerySequence returns 0, so a SeenTx with Sequence 0 and a
	// valid signer matches the expected sequence and flows to the request path.
	signer := []byte("test-signer")
	totalMessages := maxRequestsPerPeer * 4 // 120 messages from one peer

	for i := 0; i < totalMessages; i++ {
		tx := types.Tx(fmt.Sprintf("fake-seen-tx-%d", i))
		key := tx.Key()
		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    key[:],
				Signer:   signer,
				Sequence: 0,
			},
			Src: peer,
		})
	}

	requestCount := reactor.requests.CountForPeer(peerID)
	t.Logf("peer=%d requests=%d maxRequestsPerPeer=%d", peerID, requestCount, maxRequestsPerPeer)

	// The per-peer request limit should be enforced on the direct SeenTx path.
	// If this assertion fails, it means the SeenTx handler is calling requestTx
	// without checking maxRequestsPerPeer first.
	assert.LessOrEqual(t, requestCount, maxRequestsPerPeer,
		"SeenTx direct path should not exceed maxRequestsPerPeer (%d) but got %d requests",
		maxRequestsPerPeer, requestCount)
}

// plannedPeer is a deterministic peer descriptor used to build mocks.Peer
// instances with known IDs and pre-computed rendezvous rankings.
type plannedPeer struct {
	id         p2p.ID
	persistent bool
}

// planPeers generates `total` peers with deterministic IDs, computes their
// rendezvous ranking against (signer, salt), and marks the bottom
// `persistentBottom` ranked peers as persistent (or top `persistentTop` if set
// instead). Use this to control which peers fall outside the natural sticky cap.
func planPeers(signer, salt []byte, total, persistentBottom, persistentTop int) []plannedPeer {
	plans := make([]plannedPeer, total)
	for i := range plans {
		plans[i].id = p2p.ID(fmt.Sprintf("test-peer-%04d-deterministic-id-padding-fillerz", i))
	}
	type scored struct {
		idx   int
		score uint64
	}
	s := make([]scored, total)
	for i := range plans {
		s[i] = scored{i, stickyScore64(signer, string(plans[i].id), salt)}
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].score == s[j].score {
			return plans[s[i].idx].id < plans[s[j].idx].id
		}
		return s[i].score > s[j].score
	})
	for i := 0; i < persistentTop; i++ {
		plans[s[i].idx].persistent = true
	}
	for i := total - persistentBottom; i < total; i++ {
		plans[s[i].idx].persistent = true
	}
	return plans
}

// newPlannedPeer creates a *mocks.Peer with the given ID, IsPersistent flag,
// and a permissive Send stub.
func newPlannedPeer(p plannedPeer) *mocks.Peer {
	peer := &mocks.Peer{}
	peer.On("ID").Return(p.id)
	peer.On("Get", types.PeerStateKey).Return(nil).Maybe()
	peer.On("IsPersistent").Return(p.persistent).Maybe()
	peer.On("Send", mock.Anything).Return(true).Maybe()
	return peer
}

// stickyTestReactor builds a reactor wired with the planned peers.
func stickyTestReactor(t *testing.T, opts *ReactorOptions, plans []plannedPeer) (*Reactor, []*mocks.Peer) {
	t.Helper()
	app := &application{kvstore.NewApplication(db.NewMemDB())}
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	t.Cleanup(cleanup)
	if opts == nil {
		opts = &ReactorOptions{}
	}
	reactor, err := NewReactor(pool, opts)
	require.NoError(t, err)

	mockPeers := make([]*mocks.Peer, len(plans))
	for i, p := range plans {
		mockPeers[i] = newPlannedPeer(p)
		_, err := reactor.InitPeer(mockPeers[i])
		require.NoError(t, err)
	}
	return reactor, mockPeers
}

// sendRecipientIDs returns the set of peer IDs whose Send mock was invoked.
func sendRecipientIDs(peers []*mocks.Peer) map[p2p.ID]bool {
	recipients := make(map[p2p.ID]bool)
	for _, p := range peers {
		for _, c := range p.Calls {
			if c.Method == "Send" {
				recipients[p.ID()] = true
				break
			}
		}
	}
	return recipients
}

// TestBroadcastSeenTxIncludesPersistentBeyondCap verifies persistent peers ranked
// outside the natural top-maxSeenTxBroadcast still receive the SeenTx, additively.
func TestBroadcastSeenTxIncludesPersistentBeyondCap(t *testing.T) {
	const totalPeers = 30
	const persistentCount = 4

	signer := []byte("signer-A")
	salt := []byte("salt-A")
	plans := planPeers(signer, salt, totalPeers, persistentCount, 0)

	reactor, peers := stickyTestReactor(t, &ReactorOptions{
		StickyPeerSalt:           salt,
		MaxPersistentStickyPeers: persistentCount,
	}, plans)

	persistentIDs := make(map[p2p.ID]bool)
	for _, p := range plans {
		if p.persistent {
			persistentIDs[p.id] = true
		}
	}
	require.Len(t, persistentIDs, persistentCount)

	ordered := selectStickyPeers(signer, reactor.ids.GetAll(), totalPeers, salt)

	txKey := types.TxKey{}
	copy(txKey[:], bytes.Repeat([]byte{0xab}, 32))
	reactor.broadcastSeenTxWithHeight(txKey, 1, signer, 1)

	recipients := sendRecipientIDs(peers)
	for i := 0; i < maxSeenTxBroadcast; i++ {
		require.True(t, recipients[ordered[i].peer.ID()], "natural top-%d peer missing at index %d", maxSeenTxBroadcast, i)
	}
	for id := range persistentIDs {
		require.True(t, recipients[id], "persistent peer %s missing from broadcast", id)
	}
	require.Len(t, recipients, maxSeenTxBroadcast+persistentCount)
}

// TestBroadcastSeenTxNoPersistentNoChange verifies behavior is unchanged when no
// persistent peers are connected: only the natural top-maxSeenTxBroadcast receive.
func TestBroadcastSeenTxNoPersistentNoChange(t *testing.T) {
	const totalPeers = 30

	signer := []byte("signer-B")
	salt := []byte("salt-B")
	plans := planPeers(signer, salt, totalPeers, 0, 0)

	reactor, peers := stickyTestReactor(t, &ReactorOptions{StickyPeerSalt: salt}, plans)
	ordered := selectStickyPeers(signer, reactor.ids.GetAll(), totalPeers, salt)

	txKey := types.TxKey{}
	copy(txKey[:], bytes.Repeat([]byte{0xcd}, 32))
	reactor.broadcastSeenTxWithHeight(txKey, 1, signer, 1)

	recipients := sendRecipientIDs(peers)
	require.Len(t, recipients, maxSeenTxBroadcast)
	for i := 0; i < maxSeenTxBroadcast; i++ {
		require.True(t, recipients[ordered[i].peer.ID()])
	}
}

// TestBroadcastSeenTxPersistentInTopCapNoExtras verifies the broadcast set does
// not grow when persistent peers are already inside the natural top set.
func TestBroadcastSeenTxPersistentInTopCapNoExtras(t *testing.T) {
	const totalPeers = 30

	signer := []byte("signer-C")
	salt := []byte("salt-C")
	// Mark only top-2 ranked peers as persistent so they're already in top-15.
	plans := planPeers(signer, salt, totalPeers, 0, 2)

	reactor, peers := stickyTestReactor(t, &ReactorOptions{StickyPeerSalt: salt}, plans)

	txKey := types.TxKey{}
	copy(txKey[:], bytes.Repeat([]byte{0xef}, 32))
	reactor.broadcastSeenTxWithHeight(txKey, 1, signer, 1)

	recipients := sendRecipientIDs(peers)
	require.Len(t, recipients, maxSeenTxBroadcast)
}

// TestBroadcastSeenTxRespectsMaxPersistent verifies extra persistent peers beyond
// MaxPersistentStickyPeers are not added.
func TestBroadcastSeenTxRespectsMaxPersistent(t *testing.T) {
	const totalPeers = 30
	const persistentBottom = 10
	const maxPersistent = 4

	signer := []byte("signer-D")
	salt := []byte("salt-D")
	plans := planPeers(signer, salt, totalPeers, persistentBottom, 0)

	reactor, peers := stickyTestReactor(t, &ReactorOptions{
		StickyPeerSalt:           salt,
		MaxPersistentStickyPeers: maxPersistent,
	}, plans)

	txKey := types.TxKey{}
	copy(txKey[:], bytes.Repeat([]byte{0x12}, 32))
	reactor.broadcastSeenTxWithHeight(txKey, 1, signer, 1)

	recipients := sendRecipientIDs(peers)
	require.Len(t, recipients, maxSeenTxBroadcast+maxPersistent)
}

// TestBroadcastSeenTxDeterministicPersistentSelection verifies that the same
// persistent peers are picked across reactors with identical (signer, salt, peers).
func TestBroadcastSeenTxDeterministicPersistentSelection(t *testing.T) {
	const totalPeers = 30
	const persistentBottom = 10
	const maxPersistent = 3

	signer := []byte("signer-E")
	salt := []byte("salt-E")
	plans := planPeers(signer, salt, totalPeers, persistentBottom, 0)

	r1, peers1 := stickyTestReactor(t, &ReactorOptions{
		StickyPeerSalt:           salt,
		MaxPersistentStickyPeers: maxPersistent,
	}, plans)
	r2, peers2 := stickyTestReactor(t, &ReactorOptions{
		StickyPeerSalt:           salt,
		MaxPersistentStickyPeers: maxPersistent,
	}, plans)

	txKey := types.TxKey{}
	copy(txKey[:], bytes.Repeat([]byte{0x77}, 32))
	r1.broadcastSeenTxWithHeight(txKey, 1, signer, 1)
	r2.broadcastSeenTxWithHeight(txKey, 1, signer, 1)

	rec1 := sendRecipientIDs(peers1)
	rec2 := sendRecipientIDs(peers2)
	require.Equal(t, rec1, rec2, "persistent peer selection must be deterministic across reactors")
	require.Len(t, rec1, maxSeenTxBroadcast+maxPersistent)
}
