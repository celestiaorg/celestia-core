package cat

import (
	"encoding/hex"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log/term"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	db "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
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

func TestShufflePeers(t *testing.T) {
	tests := []struct {
		name        string
		setupPeers  func() map[uint16]p2p.Peer
		expectedLen int
		validate    func(t *testing.T, original, shuffled map[uint16]p2p.Peer)
	}{
		{
			name: "empty map",
			setupPeers: func() map[uint16]p2p.Peer {
				return make(map[uint16]p2p.Peer)
			},
			expectedLen: 0,
			validate: func(t *testing.T, original, shuffled map[uint16]p2p.Peer) {
				assert.Empty(t, shuffled)
				assert.True(t, len(original) == 0 && len(shuffled) == 0)
			},
		},
		{
			name: "single peer",
			setupPeers: func() map[uint16]p2p.Peer {
				peer := &mocks.Peer{}
				return map[uint16]p2p.Peer{1: peer}
			},
			expectedLen: 1,
			validate: func(t *testing.T, original, shuffled map[uint16]p2p.Peer) {
				assert.Equal(t, original, shuffled)
				for id, peer := range original {
					assert.Contains(t, shuffled, id)
					assert.Same(t, peer, shuffled[id])
				}
			},
		},
		{
			name: "two peers",
			setupPeers: func() map[uint16]p2p.Peer {
				peer1 := &mocks.Peer{}
				peer2 := &mocks.Peer{}
				return map[uint16]p2p.Peer{
					1: peer1,
					2: peer2,
				}
			},
			expectedLen: 2,
			validate: func(t *testing.T, original, shuffled map[uint16]p2p.Peer) {
				assert.Equal(t, len(original), len(shuffled))
				for id, peer := range original {
					assert.Contains(t, shuffled, id)
					assert.Same(t, peer, shuffled[id])
				}
				assert.True(t, &original != &shuffled, "Expected different map instances")
			},
		},
		{
			name: "multiple peers",
			setupPeers: func() map[uint16]p2p.Peer {
				peers := make(map[uint16]p2p.Peer)
				for i := uint16(1); i <= 10; i++ {
					peer := &mocks.Peer{}
					peers[i] = peer
				}
				return peers
			},
			expectedLen: 10,
			validate: func(t *testing.T, original, shuffled map[uint16]p2p.Peer) {
				assert.Equal(t, len(original), len(shuffled))
				for id, peer := range original {
					assert.Contains(t, shuffled, id)
					assert.Same(t, peer, shuffled[id])
				}
				assert.True(t, &original != &shuffled, "Expected different map instances")
			},
		},
		{
			name: "large peer set",
			setupPeers: func() map[uint16]p2p.Peer {
				peers := make(map[uint16]p2p.Peer)
				for i := uint16(1); i <= 100; i++ {
					peer := &mocks.Peer{}
					peers[i] = peer
				}
				return peers
			},
			expectedLen: 100,
			validate: func(t *testing.T, original, shuffled map[uint16]p2p.Peer) {
				assert.Equal(t, len(original), len(shuffled))
				for id, peer := range original {
					assert.Contains(t, shuffled, id)
					assert.Same(t, peer, shuffled[id])
				}
				assert.True(t, &original != &shuffled, "Expected different map instances")
			},
		},
		{
			name: "non-sequential IDs",
			setupPeers: func() map[uint16]p2p.Peer {
				peer1 := &mocks.Peer{}
				peer2 := &mocks.Peer{}
				peer3 := &mocks.Peer{}
				return map[uint16]p2p.Peer{
					5:    peer1,
					100:  peer2,
					9999: peer3,
				}
			},
			expectedLen: 3,
			validate: func(t *testing.T, original, shuffled map[uint16]p2p.Peer) {
				assert.Equal(t, len(original), len(shuffled))
				for id, peer := range original {
					assert.Contains(t, shuffled, id)
					assert.Same(t, peer, shuffled[id])
				}
				assert.True(t, &original != &shuffled, "Expected different map instances")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalPeers := tt.setupPeers()
			shuffledPeers := ShufflePeers(originalPeers)
			require.Equal(t, tt.expectedLen, len(shuffledPeers))
			tt.validate(t, originalPeers, shuffledPeers)
		})
	}
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
	peer.On("Send", env).Return(true)

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
		Sum: &protomem.Message_SeenTx{SeenTx: &protomem.SeenTx{TxKey: key[:]}},
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
	peers[0].On("Send", env).Return(true)
	peers[1].On("Send", env).Return(true)

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
	peer.On("Send", p2p.Envelope{
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
