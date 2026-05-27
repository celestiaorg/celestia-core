package cat

import (
	"crypto/rand"
	"os"
	"sync"
	"testing"

	db "github.com/cometbft/cometbft-db"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/internal/test"
	cmtbits "github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat/chunked"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mocks"
	cmtcrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

// newMempoolWithApp builds a TxPool wired to the given ABCI client creator,
// using a fresh temp root. Cleanup removes the temp root.
func newMempoolWithApp(cc proxy.ClientCreator) (*TxPool, func()) {
	conf := test.ResetTestRoot("mempool_test")
	return newMempoolWithAppAndConfig(cc, conf)
}

func newMempoolWithAppAndConfig(cc proxy.ClientCreator, conf *cfg.Config) (*TxPool, func()) {
	appConnMem, _ := cc.NewABCIClient()
	appConnMem.SetLogger(log.TestingLogger().With("module", "abci-client", "connection", "mempool"))
	if err := appConnMem.Start(); err != nil {
		panic(err)
	}
	mp := NewTxPool(log.TestingLogger(), conf.Mempool, appConnMem, 1)
	return mp, func() { os.RemoveAll(conf.RootDir) }
}

// ---------------------------------------------------------------------------
// Test fixture
// ---------------------------------------------------------------------------

// recordingPeer wraps mocks.Peer and captures all envelopes sent to it.
type recordingPeer struct {
	*mocks.Peer
	id  p2p.ID
	mu  sync.Mutex
	out []p2p.Envelope
}

func newRecordingPeer(t *testing.T) *recordingPeer {
	t.Helper()
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	pid := nodeKey.ID()
	p := &mocks.Peer{}
	p.On("ID").Return(pid)
	p.On("Get", types.PeerStateKey).Return(nil).Maybe()
	p.On("IsPersistent").Return(false).Maybe()
	rp := &recordingPeer{Peer: p, id: pid}
	captureFn := func(args mock.Arguments) {
		env := args.Get(0).(p2p.Envelope)
		rp.mu.Lock()
		rp.out = append(rp.out, env)
		rp.mu.Unlock()
	}
	p.On("Send", mock.AnythingOfType("p2p.Envelope")).Return(true).Run(captureFn).Maybe()
	p.On("TrySend", mock.AnythingOfType("p2p.Envelope")).Return(true).Run(captureFn).Maybe()
	return rp
}

func (rp *recordingPeer) envelopes() []p2p.Envelope {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	out := make([]p2p.Envelope, len(rp.out))
	copy(out, rp.out)
	return out
}

type chunkedFixture struct {
	t       *testing.T
	reactor *Reactor
	pool    *TxPool
	peers   []*recordingPeer
	cleanup func()
}

func newChunkedFixture(t *testing.T, numPeers int) *chunkedFixture {
	t.Helper()
	app := &application{kvstore.NewApplication(db.NewMemDB())}
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)
	reactor.SetLogger(log.TestingLogger())

	peers := make([]*recordingPeer, numPeers)
	for i := 0; i < numPeers; i++ {
		peers[i] = newRecordingPeer(t)
		_, err := reactor.InitPeer(peers[i])
		require.NoError(t, err)
	}
	return &chunkedFixture{
		t:       t,
		reactor: reactor,
		pool:    pool,
		peers:   peers,
		cleanup: cleanup,
	}
}

// extractInner walks p2p.Envelopes (Message wrappers from .Send) and returns
// the inner-typed messages of type T. TrySend bypasses the wrapper, so it
// also matches direct-typed envelopes.
func extractInner[T any](envs []p2p.Envelope) []T {
	out := []T{}
	for _, e := range envs {
		switch m := e.Message.(type) {
		case T:
			out = append(out, m)
		case *protomem.Message:
			switch inner := m.Sum.(type) {
			case *protomem.Message_SeenLargeTx:
				if v, ok := any(inner.SeenLargeTx).(T); ok {
					out = append(out, v)
				}
			case *protomem.Message_HaveTxChunks:
				if v, ok := any(inner.HaveTxChunks).(T); ok {
					out = append(out, v)
				}
			case *protomem.Message_WantTxChunks:
				if v, ok := any(inner.WantTxChunks).(T); ok {
					out = append(out, v)
				}
			case *protomem.Message_TxChunks:
				if v, ok := any(inner.TxChunks).(T); ok {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func randomBody(t *testing.T, size int) []byte {
	t.Helper()
	body := make([]byte, size)
	_, err := rand.Read(body)
	require.NoError(t, err)
	return body
}

// validBody returns a CheckTx-passing tx of the requested size. The
// application in pool_test.go expects "sender=key=value" with value as int64;
// we shape the body as "s=<random-padding>=1" with the padding random bytes
// scrubbed of '=' so the parse succeeds.
func validBody(t *testing.T, size int) []byte {
	t.Helper()
	const prefix = "s="
	const suffix = "=1"
	require.GreaterOrEqual(t, size, len(prefix)+len(suffix)+1)
	body := make([]byte, size)
	copy(body, prefix)
	copy(body[size-len(suffix):], suffix)
	padding := body[len(prefix) : size-len(suffix)]
	_, err := rand.Read(padding)
	require.NoError(t, err)
	// Strip any '=' bytes from padding so the application parses as 3 parts.
	for i, b := range padding {
		if b == '=' {
			padding[i] = '.'
		}
	}
	return body
}

// ---------------------------------------------------------------------------
// Tests — Origination paths
// ---------------------------------------------------------------------------

// Default RPC origination (Fix B): SeenLargeTx + HaveTxChunks(partial) bundled
// to ALL peers; each chunk announced to exactly DefaultHaveTxChunksRedundancy
// peers across the full peer set so origin's upload load stays bounded while
// every peer learns the tx's shape in one round.
func TestDefaultRPCOrigination_AllPeersAndRedundancy(t *testing.T) {
	const numPeers = 20
	f := newChunkedFixture(t, numPeers)
	defer f.cleanup()

	body := randomBody(t, 6*chunked.ChunkSize)
	tx := types.Tx(body)
	wtx := &wrappedTx{tx: tx.ToCachedTx()}
	f.reactor.broadcastNewLargeTxDefault(wtx)

	state := f.reactor.chunkedStore.Get(tx.Key())
	require.NotNil(t, state, "PartsState should be inserted on origination")
	numParts := int(state.NumParts)

	seenLargeCount := 0
	hitCount := make([]int, numParts)
	for _, p := range f.peers {
		envs := p.envelopes()
		seenLargeCount += len(extractInner[*protomem.SeenLargeTx](envs))
		for _, h := range extractInner[*protomem.HaveTxChunks](envs) {
			ba := cmtbits.NewBitArray(numParts)
			ba.FromProto(&h.Parts)
			for _, idx := range ba.GetTrueIndices() {
				hitCount[idx]++
			}
		}
	}

	require.Equal(t, numPeers, seenLargeCount,
		"Default RPC should send SeenLargeTx to every connected peer")
	for i, c := range hitCount {
		require.Equalf(t, DefaultHaveTxChunksRedundancy, c,
			"chunk %d announced %d times, want exactly %d", i, c, DefaultHaveTxChunksRedundancy)
	}
}

// Push RPC origination: SeenLargeTx + HaveTxChunks(full) to ALL peers, plus
// each chunk pushed to DefaultPushRPCChunkRedundancy peers round-robin.
func TestPushRPCOrigination_FullBitmapAndChunkPush(t *testing.T) {
	const numPeers = 12
	f := newChunkedFixture(t, numPeers)
	defer f.cleanup()
	f.reactor.opts.RPCPushMode = true

	body := randomBody(t, 4*chunked.ChunkSize)
	tx := types.Tx(body)
	wtx := &wrappedTx{tx: tx.ToCachedTx()}
	f.reactor.broadcastNewLargeTxPush(wtx)

	state := f.reactor.chunkedStore.Get(tx.Key())
	require.NotNil(t, state)
	numParts := int(state.NumParts)

	seenLargeCount := 0
	fullHaveCount := 0
	chunkPushPerIdx := make(map[uint32]int)
	for _, p := range f.peers {
		envs := p.envelopes()
		seenLargeCount += len(extractInner[*protomem.SeenLargeTx](envs))
		for _, h := range extractInner[*protomem.HaveTxChunks](envs) {
			ba := cmtbits.NewBitArray(numParts)
			ba.FromProto(&h.Parts)
			if len(ba.GetTrueIndices()) == numParts {
				fullHaveCount++
			}
		}
		for _, batch := range extractInner[*protomem.TxChunks](envs) {
			for _, c := range batch.Chunks {
				chunkPushPerIdx[c.Index]++
			}
		}
	}

	require.Equal(t, numPeers, seenLargeCount,
		"Push RPC: SeenLargeTx should go to every peer")
	require.Equal(t, numPeers, fullHaveCount,
		"Push RPC: full-bitmap HaveTxChunks should go to every peer")
	// Adaptive push: for a small body (~256 KiB) and 12 peers, redundancy
	// hits the numPeers cap (saturation) — every chunk goes to every peer.
	expectedRedundancy := adaptivePushRedundancy(len(body), numPeers)
	for i := 0; i < numParts; i++ {
		require.Equal(t, expectedRedundancy, chunkPushPerIdx[uint32(i)],
			"chunk %d pushed %d times, want %d", i, chunkPushPerIdx[uint32(i)], expectedRedundancy)
	}
}

// adaptivePushRedundancy must clamp to numPeers (saturation) for small txs
// and floor at DefaultPushRPCChunkRedundancy for large txs.
func TestAdaptivePushRedundancy(t *testing.T) {
	cases := []struct {
		name     string
		txSize   int
		numPeers int
		want     int
	}{
		{"saturate tiny tx on small mesh", 1 << 18, 12, 12}, // 256 KiB on 12 peers → cap at 12
		{"saturate 1 MiB on big mesh", 1 << 20, 89, 89},     // 1 MiB on 89 peers → cap at 89
		{"4 MiB drops below saturation", 4 << 20, 89, 25},   // 100/4 = 25
		{"8 MiB drops further", 8 << 20, 89, 12},            // 100/8 = 12
		{"32 MiB hits floor", 32 << 20, 89, DefaultPushRPCChunkRedundancy},
		{"zero peers", 1 << 20, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, adaptivePushRedundancy(tc.txSize, tc.numPeers))
		})
	}
}

// ---------------------------------------------------------------------------
// Tests — Intermediate flow
// ---------------------------------------------------------------------------

// Cross-peer Want dedup: with chunk in-flight to peer 1, an identical
// HaveTxChunks from peer 2 must not trigger a duplicate Want.
func TestIntermediate_CrossPeerWantDedup(t *testing.T) {
	const numPeers = 3
	f := newChunkedFixture(t, numPeers)
	defer f.cleanup()

	body := randomBody(t, 4*chunked.ChunkSize)
	tx := types.Tx(body)
	enc, err := chunked.Encode(body)
	require.NoError(t, err)
	txKey := tx.Key()

	// Insert PartsState by sending SeenLargeTx from peer 0.
	f.reactor.handleSeenLargeTx(f.peers[0], &protomem.SeenLargeTx{
		TxKey:      txKey[:],
		PartsRoot:  enc.PartsRoot,
		NumParts:   enc.NumParts(),
		LastLength: enc.LastLength,
		LeafHashes: enc.LeafHashes,
	})
	require.NotNil(t, f.reactor.chunkedStore.Get(txKey))

	full := cmtbits.NewBitArray(int(enc.NumParts()))
	full.Fill()

	// Peer 1 announces all chunks → should trigger one Want to peer 1.
	f.reactor.handleHaveTxChunks(f.peers[1], &protomem.HaveTxChunks{
		TxKey: txKey[:],
		Parts: *full.ToProto(),
	})
	want1 := extractInner[*protomem.WantTxChunks](f.peers[1].envelopes())
	require.Len(t, want1, 1, "peer 1 should receive exactly one WantTxChunks")

	// Peer 2 announces full bitmap — chunks are in-flight, so no Want to peer 2.
	f.reactor.handleHaveTxChunks(f.peers[2], &protomem.HaveTxChunks{
		TxKey: txKey[:],
		Parts: *full.ToProto(),
	})
	want2 := extractInner[*protomem.WantTxChunks](f.peers[2].envelopes())
	require.Empty(t, want2,
		"cross-peer dedup: peer 2 should NOT receive a Want for already-in-flight chunks")
}

// Reconstruction: feed K chunks → tx admitted to mempool.
func TestIntermediate_ReconstructAndAdmit(t *testing.T) {
	const numPeers = 2
	f := newChunkedFixture(t, numPeers)
	defer f.cleanup()

	body := validBody(t, 3*chunked.ChunkSize)
	tx := types.Tx(body)
	enc, err := chunked.Encode(body)
	require.NoError(t, err)
	txKey := tx.Key()

	f.reactor.handleSeenLargeTx(f.peers[0], &protomem.SeenLargeTx{
		TxKey:      txKey[:],
		PartsRoot:  enc.PartsRoot,
		NumParts:   enc.NumParts(),
		LastLength: enc.LastLength,
		LeafHashes: enc.LeafHashes,
	})

	// Feed K (= numParts/2) data chunks from peer 1.
	K := enc.NumParts() / 2
	chunks := make([]protomem.TxChunk, 0, K)
	for i := uint32(0); i < K; i++ {
		var proof cmtcrypto.Proof
		if enc.NumParts() > 1 {
			proof = *enc.Proofs[i].ToProto()
		}
		chunks = append(chunks, protomem.TxChunk{
			Index: i, Data: enc.Chunk(i), Proof: proof,
		})
	}
	f.reactor.handleTxChunks(f.peers[1], &protomem.TxChunks{
		TxKey:  txKey[:],
		Chunks: chunks,
	})

	require.True(t, f.pool.Has(txKey),
		"tx should be admitted to mempool after K-of-2K chunks installed")
}

// Block inclusion drops PartsState (ADR-013 #26).
func TestBlockInclusion_DropsPartsState(t *testing.T) {
	f := newChunkedFixture(t, 1)
	defer f.cleanup()

	body := validBody(t, chunked.ChunkSize+128)
	tx := types.Tx(body)
	wtx := &wrappedTx{tx: tx.ToCachedTx()}
	f.reactor.broadcastNewLargeTxDefault(wtx)
	txKey := tx.Key()
	require.NotNil(t, f.reactor.chunkedStore.Get(txKey),
		"PartsState present after origination")

	rsp, err := f.pool.TryAddNewTx(tx.ToCachedTx(), txKey, mempool.TxInfo{})
	require.NoError(t, err)
	require.Zero(t, rsp.Code)

	err = f.pool.Update(2,
		[]*types.CachedTx{tx.ToCachedTx()},
		[]*abci.ExecTxResult{{}},
		nil, nil)
	require.NoError(t, err)

	require.Nil(t, f.reactor.chunkedStore.Get(txKey),
		"PartsState should be dropped on block inclusion")
}
