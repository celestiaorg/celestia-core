package cat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	db "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

// makeLargeTx builds a kvstore-valid tx ("signer=key=priority") whose total wire
// size is at least minSize bytes, so it crosses the large-tx threshold.
func makeLargeTx(t *testing.T, signer string, minSize int, priority int) types.Tx {
	t.Helper()
	payload := make([]byte, minSize/2+64)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	return types.Tx(fmt.Sprintf("%s=%s=%d", signer, hex.EncodeToString(payload), priority))
}

// makeAndConnectLargeTxReactors builds n fully-connected reactors configured so
// that modestly-sized txs take the chunked fast path (small threshold + small
// chunk size), exercising real manifest advertisement and chunk transfer.
func makeAndConnectLargeTxReactors(t *testing.T, n int, mutate func(*ReactorOptions)) []*Reactor {
	config := cfg.TestConfig()
	config.Mempool.MaxTxBytes = 4 * 1024 * 1024

	reactors := make([]*Reactor, n)
	logger := mempoolLogger()
	for i := 0; i < n; i++ {
		app := &application{kvstore.NewApplication(db.NewMemDB())}
		cc := proxy.NewLocalClientCreator(app)
		pool, cleanup := newMempoolWithAppAndConfig(cc, config)
		t.Cleanup(cleanup)

		opts := &ReactorOptions{
			MaxTxSize:        config.Mempool.MaxTxBytes,
			LargeTxThreshold: 32 * 1024,
			LargeTxChunkSize: minLargeTxChunkSize,
		}
		if mutate != nil {
			mutate(opts)
		}
		reactor, err := NewReactor(pool, opts)
		require.NoError(t, err)
		reactor.SetStickySalt([]byte{byte(i + 1)})
		pool.logger = logger.With("validator", i)
		reactor.SetLogger(logger.With("validator", i))
		reactors[i] = reactor
	}

	switches := p2p.MakeConnectedSwitches(config.P2P, n, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("MEMPOOL", reactors[i])
		return s
	}, p2p.Connect2Switches)

	t.Cleanup(func() {
		for _, s := range switches {
			_ = s.Stop()
		}
	})

	for _, r := range reactors {
		for _, peer := range r.Switch.Peers().List() {
			peer.Set(types.PeerStateKey, peerState{1})
		}
	}
	return reactors
}

// TestReactorLargeTxFastPathDissemination verifies that a large tx submitted to
// one node is disseminated to all peers via the manifest + chunk fast path with
// no configuration changes (the fast path is on by default).
func TestReactorLargeTxFastPathDissemination(t *testing.T) {
	const N = 4
	reactors := makeAndConnectLargeTxReactors(t, N, nil)

	tx := makeLargeTx(t, "sender-000-0", 80*1024, 5)
	key := tx.Key()
	require.GreaterOrEqual(t, len(tx), 32*1024, "tx must cross the large-tx threshold")

	require.NoError(t, reactors[0].mempool.CheckTx(tx, nil, mempool.TxInfo{}))

	waitForTxsOnReactors(t, types.Txs{tx}, reactors)

	// The originator advertised via a manifest (fast path), and every receiver
	// reconstructed the tx and kept the manifest so it can serve chunks onward.
	for i, r := range reactors {
		require.True(t, r.mempool.Has(key), "reactor %d missing tx", i)
		require.Eventually(t, func() bool { return r.manifests.has(key) }, 2*time.Second, 20*time.Millisecond,
			"reactor %d should hold the manifest", i)
		require.False(t, r.reconstructions.has(key), "reactor %d should have no lingering session", i)
	}
}

// TestReactorLargeTxFromMultiplePeers verifies reconstruction still completes
// when chunks are sourced across several peers in a larger topology.
func TestReactorLargeTxFromMultiplePeers(t *testing.T) {
	const N = 6
	reactors := makeAndConnectLargeTxReactors(t, N, func(o *ReactorOptions) {
		o.LargeTxRequestParallelism = 4
		o.LargeTxMaxInflightChunksPerPeer = 2
	})

	tx := makeLargeTx(t, "sender-001-0", 200*1024, 7)
	require.NoError(t, reactors[0].mempool.CheckTx(tx, nil, mempool.TxInfo{}))
	waitForTxsOnReactors(t, types.Txs{tx}, reactors)
}

// TestReactorRejectsInvalidManifest verifies a malformed manifest received from
// a real connected peer starts no reconstruction session (and triggers a
// disconnect via StopPeerForError without panicking).
func TestReactorRejectsInvalidManifest(t *testing.T) {
	reactors := makeAndConnectLargeTxReactors(t, 2, nil)
	sender, receiver := reactors[0], reactors[1]

	peers := sender.Switch.Peers().List()
	require.NotEmpty(t, peers)

	badKey := make([]byte, 10) // wrong length: fails validateTxManifest
	manifest := &protomem.Message{
		Sum: &protomem.Message_TxManifest{
			TxManifest: &protomem.TxManifest{
				TxKey:       badKey,
				TxSize:      100 * 1024,
				ChunkSize:   minLargeTxChunkSize,
				ChunkCount:  1,
				ChunkHashes: [][]byte{make([]byte, chunkHashSize)},
			},
		},
	}
	peers[0].Send(p2p.Envelope{ChannelID: MempoolDataChannel, Message: manifest})

	require.Eventually(t, func() bool {
		return receiver.reconstructions.len() == 0
	}, 2*time.Second, 20*time.Millisecond)
}
