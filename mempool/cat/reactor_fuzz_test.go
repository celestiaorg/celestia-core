package cat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abciclient "github.com/cometbft/cometbft/abci/client"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

const (
	fzMaxInput   = 1024
	fzMaxOps     = 32
	fzPeers      = 3
	fzSigners    = 3
	fzSeenLimit  = 8 // lowered tracker caps so short sequences reach overflow paths
	fzIterBudget = 20 * time.Second

	// Excluded pending PROTOCO-3514; remove with the fix.
	fzCheckStaleBuffered = false
	// Excluded pending PROTOCO-3524; remove with the fix.
	fzCheckCountLimit = false
)

// fzPoolLimits derives the pool's count and byte limits from the first input
// byte. Generated txs are 11-13 bytes, so with an even byte the byte limit
// binds (96 bytes holds at most 8 txs, below the count limit of 16) and with an
// odd byte the count limit (2-5 txs) binds well before the byte limit.
func fzPoolLimits(b byte) (txs int, bytes int64) {
	if b&1 == 0 {
		return 16, 96
	}
	return 2 + int(b>>1)%4, 4096
}

// FuzzReactorReceive drives the CAT reactor's Receive with a short sequence of
// Txs/SeenTx/WantTx messages from up to three mock peers, interleaved with
// peer churn, request timeouts, block commits and signer sequence changes.
// The pool runs against an in-process app that enforces per-signer sequences,
// so the per-signer buffering and gap-fill paths are reachable.
func FuzzReactorReceive(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		// The first byte selects the pool limits (see fzPoolLimits); 0 makes the
		// byte limit bind.
		// SeenTx at the expected sequence, delivery, WantTx for it, commit
		{0, 0, 0, 0, 2, 3, 0, 4, 0, 0, 0, 4, 1, 0, 0, 7, 0, 1},
		// future-sequence burst, gap fill, out-of-order delivery, sequence jump
		{0, 1, 0, 0, 1, 5, 0, 1, 0, 2, 3, 1, 4, 0, 0, 0, 3, 0, 4, 0, 0, 2, 3, 0, 4, 0, 0, 0, 3, 0, 4, 0, 0, 1, 3, 0, 4, 0, 0, 4, 9, 0, 0, 3},
		// malformed SeenTx key, empty Txs, reconnect, malformed WantTx key
		{0, 2, 0, 6, 0, 0, 0, 5, 1, 2, 3, 4, 5, 3, 1, 0, 5, 0, 4, 0, 0, 0x40, 3, 1, 2, 3},
		// request, timeout and re-request, churn, failed sends, commit
		{0, 0, 0, 0, 2, 0, 1, 0, 2, 8, 0, 0, 6, 1, 5, 1, 10, 0, 0, 0, 0, 3, 7, 0, 2},
		// SeenTx with another signer's metadata, then delivery
		{0, 2, 0, 8, 1, 3, 2, 3, 0, 4, 0, 0, 0},
		// count limit binds (2 txs) for a single delivery
		{1, 0, 0, 0, 2, 3, 0, 4, 0, 2, 0, 0, 0},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fzMaxInput {
			return
		}
		// A hung iteration (deadlock) crashes with every goroutine's stack.
		watchdog := time.AfterFunc(fzIterBudget, func() { panic("FuzzReactorReceive: iteration hung") })
		defer watchdog.Stop()

		in := &fzInput{data: data}
		h := newCatFuzzHarness(t, in.byte())
		for op := 0; op < fzMaxOps && len(in.data) > 0; op++ {
			h.step(in)
			h.checkInvariants()
		}
		// Once every peer is gone, nothing may stay tracked on their behalf.
		for i := range h.peers {
			if h.peers[i] != nil {
				h.sw.StopPeerForError(h.peers[i], "fuzz teardown", h.r.String())
				h.peers[i] = nil
			}
		}
		h.checkInvariants()
		require.Zero(t, h.pool.seenTracker.Len())
		h.r.requests.mtx.Lock()
		byTx, byPeer := len(h.r.requests.requestsByTx), len(h.r.requests.requestsByPeer)
		h.r.requests.mtx.Unlock()
		require.Zero(t, byTx)
		require.Zero(t, byPeer)
	})
}

type fzInput struct{ data []byte }

func (in *fzInput) byte() byte {
	if len(in.data) == 0 {
		return 0
	}
	b := in.data[0]
	in.data = in.data[1:]
	return b
}

func (in *fzInput) bytes(n int) []byte {
	if n > len(in.data) {
		n = len(in.data)
	}
	out := append([]byte(nil), in.data[:n]...)
	in.data = in.data[n:]
	return out
}

// fzTxMeta is the true signer/sequence of a generated tx.
type fzTxMeta struct {
	tx     types.Tx
	signer []byte
	seq    uint64
}

func fzKey(tx types.Tx) []byte {
	k := tx.Key()
	return k[:]
}

func fzSigner(s int) []byte { return []byte("sig" + strconv.Itoa(s)) }

// fuzzSeqApp accepts "signer=seq=priority=variant" txs only at the signer's
// expected sequence, advancing it, like an account-sequence ante handler.
type fuzzSeqApp struct {
	abci.BaseApplication
	mtx      sync.Mutex
	seqs     map[string]uint64
	accepted map[types.TxKey]bool
}

var errFzUnknownSigner = errors.New("unknown signer")

func (app *fuzzSeqApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	parts := bytes.Split(req.Tx, []byte("="))
	if len(parts) != 4 {
		return &abci.ResponseCheckTx{Code: 101}, nil
	}
	seq, err1 := strconv.ParseUint(string(parts[1]), 10, 64)
	prio, err2 := strconv.ParseInt(string(parts[2]), 10, 64)
	if err1 != nil || err2 != nil {
		return &abci.ResponseCheckTx{Code: 100}, nil
	}
	app.mtx.Lock()
	defer app.mtx.Unlock()
	signer := string(parts[0])
	if seq != app.seqs[signer] {
		return &abci.ResponseCheckTx{Code: 32, Address: parts[0], Sequence: seq}, nil
	}
	app.seqs[signer]++
	app.accepted[types.Tx(req.Tx).Key()] = true
	return &abci.ResponseCheckTx{Address: parts[0], Sequence: seq, Priority: prio, GasWanted: 1}, nil
}

func (app *fuzzSeqApp) QuerySequence(_ context.Context, req *abci.RequestQuerySequence) (*abci.ResponseQuerySequence, error) {
	if !bytes.HasPrefix(req.Signer, []byte("sig")) {
		return nil, errFzUnknownSigner
	}
	app.mtx.Lock()
	defer app.mtx.Unlock()
	return &abci.ResponseQuerySequence{Sequence: app.seqs[string(req.Signer)]}, nil
}

func (app *fuzzSeqApp) seq(signer []byte) uint64 {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	return app.seqs[string(signer)]
}

// fuzzPeer records what the reactor sends and can be told to fail sends.
type fuzzPeer struct {
	*mock.Peer
	id     p2p.ID
	mtx    sync.Mutex
	sendOK bool
	sent   []p2p.Envelope
}

func (p *fuzzPeer) ID() p2p.ID { return p.id }

func (p *fuzzPeer) TrySend(e p2p.Envelope) bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if !p.sendOK {
		return false
	}
	p.sent = append(p.sent, e)
	return true
}

func (p *fuzzPeer) drain() []p2p.Envelope {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	out := p.sent
	p.sent = nil
	return out
}

// nopTransport lets the real Switch stop peers without a network.
type nopTransport struct{ p2p.Transport }

func (nopTransport) Cleanup(p2p.Peer) {}

type catFuzzHarness struct {
	t        *testing.T
	app      *fuzzSeqApp
	cfg      *cmtcfg.MempoolConfig
	pool     *TxPool
	r        *Reactor
	sw       *p2p.Switch
	peers    [fzPeers]*fuzzPeer
	wants    [fzPeers][]types.TxKey // WantTx keys sent to each peer, in order
	txs      map[types.TxKey]fzTxMeta
	poisoned map[types.TxKey]bool
	height   int64
}

func newCatFuzzHarness(t *testing.T, limits byte) *catFuzzHarness {
	app := &fuzzSeqApp{seqs: map[string]uint64{}, accepted: map[types.TxKey]bool{}}
	conn := abciclient.NewLocalClient(new(cmtsync.Mutex), app)
	require.NoError(t, conn.Start())
	cfg := cmtcfg.TestMempoolConfig()
	cfg.Recheck = false
	cfg.Broadcast = false
	cfg.Size, cfg.MaxTxsBytes = fzPoolLimits(limits)
	cfg.CacheSize = 100
	pool := NewTxPool(log.NewNopLogger(), cfg, conn, 1)
	pool.seenTracker.perPeerLimit = fzSeenLimit
	pool.seenTracker.perSignerLimit = fzSeenLimit
	// Timers never fire on their own; timeouts are driven explicitly.
	r, err := NewReactor(pool, &ReactorOptions{MaxGossipDelay: time.Hour, MaxTxSize: 4096})
	require.NoError(t, err)
	sw := p2p.NewSwitch(cmtcfg.DefaultP2PConfig(), nopTransport{})
	sw.AddReactor("MEMPOOL", r)
	h := &catFuzzHarness{
		t: t, app: app, cfg: cfg, pool: pool, r: r, sw: sw, height: 1,
		txs: map[types.TxKey]fzTxMeta{}, poisoned: map[types.TxKey]bool{},
	}
	t.Cleanup(func() {
		r.requests.Close()
		// Reactor.OnStop does not cancel the base reactor's processor
		// goroutine, so cancel it directly to not leak one per iteration.
		r.BaseReactor.OnStop()
		require.NoError(t, conn.Stop())
	})
	h.connect(0)
	h.connect(1)
	return h
}

func (h *catFuzzHarness) connect(i int) {
	if h.peers[i] != nil {
		return
	}
	p := &fuzzPeer{Peer: mock.NewPeer(nil), id: p2p.ID(fmt.Sprintf("%040x", i+1)), sendOK: true}
	_, err := h.r.InitPeer(p)
	require.NoError(h.t, err)
	h.r.AddPeer(p)
	h.peers[i] = p
	h.wants[i] = nil
}

func (h *catFuzzHarness) peerID(i int) uint16 { return h.r.ids.GetIDForPeer(h.peers[i].ID()) }

// tx returns the generated tx for signer s at seq with variant v.
func (h *catFuzzHarness) tx(s int, seq uint64, v byte) fzTxMeta {
	prio := 1 + (seq*7+uint64(v)*3+uint64(s))%5
	m := fzTxMeta{
		tx:     types.Tx(fmt.Sprintf("sig%d=%d=%d=v%d", s, seq, prio, v&1)),
		signer: fzSigner(s),
		seq:    seq,
	}
	h.txs[m.tx.Key()] = m
	return m
}

// seqNear picks a sequence around the signer's expected one.
func (h *catFuzzHarness) seqNear(s int, b byte) uint64 {
	seq := int64(h.app.seq(fzSigner(s))) + int64(b%12) - 2
	if seq < 0 {
		seq = 0
	}
	return uint64(seq)
}

func (h *catFuzzHarness) receive(i int, msg p2p.Wrapper, ch byte) {
	h.r.Receive(p2p.Envelope{ChannelID: ch, Message: msg, Src: h.peers[i]})
}

func (h *catFuzzHarness) step(in *fzInput) {
	op := in.byte() % 11
	i := int(in.byte() % fzPeers)
	if h.peers[i] == nil && op != 5 && op != 7 && op != 9 {
		return
	}
	expectBan := false
	switch op {
	case 0: // honest SeenTx near the expected sequence
		s := int(in.byte() % fzSigners)
		b := in.byte()
		m := h.tx(s, h.seqNear(s, b), b>>7)
		h.receive(i, &protomem.SeenTx{TxKey: fzKey(m.tx), Signer: m.signer, Sequence: m.seq}, MempoolDataChannel)
	case 1: // burst of consecutive honest SeenTx
		s := int(in.byte() % fzSigners)
		b := in.byte()
		start := h.seqNear(s, b%4+2)
		n := uint64(in.byte() % 31)
		for k := uint64(0); k <= n; k++ {
			m := h.tx(s, start+k, b>>7)
			h.receive(i, &protomem.SeenTx{TxKey: fzKey(m.tx), Signer: m.signer, Sequence: m.seq}, MempoolDataChannel)
		}
	case 2: // arbitrary or malformed SeenTx
		sel := in.byte()
		s := int(in.byte() % fzSigners)
		seq := h.seqNear(s, in.byte())
		m := h.tx(s, h.seqNear(s, in.byte()), sel>>7)
		key := fzKey(m.tx)
		switch {
		case sel&6 == 6:
			key = in.bytes(int(in.byte() % 40))
		case sel&1 == 1:
			key = in.bytes(32)
		}
		var signer []byte
		switch (sel >> 3) % 4 {
		case 0:
			signer = m.signer
		case 1:
			signer = fzSigner(s)
		case 3:
			signer = in.bytes(int(in.byte() % 70))
		}
		expectBan = len(key) != 32 || len(signer) == 0 || len(signer) > maxSignerLength
		if len(key) == 32 {
			if tm, ok := h.txs[types.TxKey(key)]; ok && (!bytes.Equal(tm.signer, signer) || tm.seq != seq) {
				h.poisoned[types.TxKey(key)] = true
			}
		}
		h.receive(i, &protomem.SeenTx{TxKey: key, Signer: signer, Sequence: seq}, MempoolDataChannel)
	case 3: // Txs: malformed, unsolicited, or a response to an outstanding WantTx
		var txs [][]byte
		switch mode := in.byte() % 8; mode {
		case 0:
			expectBan = true
		case 1:
			expectBan = true
			txs = [][]byte{[]byte("a"), []byte("b")}
		case 2:
			expectBan = true
			txs = [][]byte{{}}
		case 3:
			txs = [][]byte{append([]byte{'x'}, in.bytes(int(in.byte()%32))...)}
		default:
			s := int(in.byte() % fzSigners)
			m := h.tx(s, h.seqNear(s, in.byte()), mode&1)
			if w := h.wants[i]; len(w) > 0 {
				if wm, ok := h.txs[w[int(in.byte())%len(w)]]; ok && mode != 7 {
					m = wm
				}
			}
			txs = [][]byte{m.tx}
		}
		h.receive(i, &protomem.Txs{Txs: txs}, MempoolDataChannel)
	case 4: // WantTx for a known tx or a malformed key
		s := int(in.byte() % fzSigners)
		b := in.byte()
		key := fzKey(h.tx(s, uint64(b%16), b>>7).tx)
		if b&0x40 != 0 {
			key = in.bytes(int(in.byte() % 40))
		}
		expectBan = len(key) != 32
		var pooled *types.CachedTx
		if !expectBan {
			if tx, ok := h.pool.GetTxByKey(types.TxKey(key)); ok {
				pooled = tx
			}
		}
		h.peers[i].drain()
		h.receive(i, &protomem.WantTx{TxKey: key}, MempoolWantsChannel)
		sent := h.peers[i].drain()
		if pooled != nil && h.peers[i].sendOK && !expectBan {
			require.Len(h.t, sent, 1)
			require.Equal(h.t, &protomem.Txs{Txs: [][]byte{pooled.Tx}}, sent[0].Message)
		} else {
			require.Empty(h.t, sent)
		}
	case 5:
		h.connect(i)
	case 6:
		h.sw.StopPeerForError(h.peers[i], "fuzz disconnect", h.r.String())
		require.False(h.t, h.peers[i].IsRunning())
		h.peers[i] = nil
	case 7: // commit a block with some reaped txs
		txs := h.pool.ReapMaxTxs(int(in.byte() % 4))
		res := make([]*abci.ExecTxResult, len(txs))
		for k := range res {
			res[k] = &abci.ExecTxResult{}
		}
		h.height++
		h.pool.Lock()
		require.NoError(h.t, h.pool.Update(h.height, txs, res, nil, nil))
		h.pool.Unlock()
		h.r.refreshPendingSeenQueues()
		h.checkNoStaleBuffered()
	case 8: // the response timer for one outstanding request fires
		h.timeout(i, in.byte())
	case 9: // the signer's sequence advances outside this node's mempool
		s := fzSigner(int(in.byte() % fzSigners))
		h.app.mtx.Lock()
		h.app.seqs[string(s)] += uint64(1 + in.byte()%4)
		h.app.mtx.Unlock()
		h.r.refreshPendingSeenQueues()
		h.checkNoStaleBuffered()
	case 10:
		h.peers[i].mtx.Lock()
		h.peers[i].sendOK = !h.peers[i].sendOK
		h.peers[i].mtx.Unlock()
	}
	if p := h.peers[i]; p != nil && op <= 4 {
		require.Equal(h.t, expectBan, !p.IsRunning(), "op %d: peer stopped mismatch", op)
		if expectBan {
			h.peers[i] = nil
		}
	}
	// Excluded pending PROTOCO-3515; remove with the fix.
	h.checkSent(op == 6 || op == 8 || expectBan)
}

// timeout replicates the request scheduler's response timer firing.
func (h *catFuzzHarness) timeout(i int, b byte) {
	pid := h.peerID(i)
	rq := h.r.requests
	rq.mtx.Lock()
	keys := make([]types.TxKey, 0, len(rq.requestsByPeer[pid]))
	for k := range rq.requestsByPeer[pid] {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, c int) bool { return bytes.Compare(keys[a][:], keys[c][:]) < 0 })
	if len(keys) == 0 {
		rq.mtx.Unlock()
		return
	}
	key := keys[int(b)%len(keys)]
	if rq.requestsByTx[key] != pid {
		if b&0x80 != 0 { // late-response slot garbage-collected
			rq.requestsByPeer[pid][key].Stop()
			rq.deletePeerRequest(pid, key)
		}
		rq.mtx.Unlock()
		return
	}
	delete(rq.requestsByTx, key)
	rq.mtx.Unlock()
	h.r.onRequestTimeout(key, pid)
}

// checkSent validates every message the reactor sent during the last op.
func (h *catFuzzHarness) checkSent(reRequested bool) {
	for i, p := range h.peers {
		if p == nil {
			continue
		}
		for _, e := range p.drain() {
			switch m := e.Message.(type) {
			case *protomem.Message: // SeenTx / WantTx are sent wrapped
				switch inner := m.Sum.(type) {
				case *protomem.Message_SeenTx:
					key, err := types.TxKeyFromBytes(inner.SeenTx.TxKey)
					require.NoError(h.t, err)
					h.app.mtx.Lock()
					accepted := h.app.accepted[key]
					h.app.mtx.Unlock()
					require.True(h.t, accepted, "SeenTx relayed for a tx the app never accepted")
					// Metadata of a tx announced with false signer/sequence
					// may be relayed as-is; that is PROTOCO-3429.
					if !h.poisoned[key] {
						tm := h.txs[key]
						require.Equal(h.t, tm.signer, inner.SeenTx.Signer)
						require.Equal(h.t, tm.seq, inner.SeenTx.Sequence)
					}
				case *protomem.Message_WantTx:
					key, err := types.TxKeyFromBytes(inner.WantTx.TxKey)
					require.NoError(h.t, err)
					if !reRequested {
						require.False(h.t, h.pool.Has(key), "requested a tx already in the pool")
					}
					h.wants[i] = append(h.wants[i], key)
				default:
					h.t.Fatalf("unexpected message %T", inner)
				}
			default:
				h.t.Fatalf("unexpected unsolicited message %T", m)
			}
		}
	}
}

// checkInvariants checks the pool, request, tracker and buffer state. Each
// locked section releases its lock in a defer, so a failing assertion (which
// exits the goroutine) never leaves a lock held for the cleanup to block on.
func (h *catFuzzHarness) checkInvariants() {
	t := h.t
	require.LessOrEqual(t, h.pool.SizeBytes(), h.cfg.MaxTxsBytes)
	if fzCheckCountLimit {
		require.LessOrEqual(t, h.pool.Size(), h.cfg.Size)
	}
	func() {
		h.app.mtx.Lock()
		defer h.app.mtx.Unlock()
		for _, k := range h.pool.store.getAllKeys() {
			require.True(t, h.app.accepted[k], "pool holds a tx the app never accepted")
		}
	}()

	active := func(id uint16) bool { return id != 0 && h.r.ids.GetPeer(id) != nil }

	func() {
		rq := h.r.requests
		rq.mtx.Lock()
		defer rq.mtx.Unlock()
		h.checkRequests(active)
	}()
	func() {
		st := h.pool.seenTracker
		st.mtx.Lock()
		defer st.mtx.Unlock()
		h.checkSeenTracker(active)
	}()
	func() {
		rb := h.r.receivedBuffer
		rb.mu.Lock()
		defer rb.mu.Unlock()
		h.checkReceivedBuffer()
	}()
}

// checkRequests runs with h.r.requests.mtx held.
func (h *catFuzzHarness) checkRequests(active func(uint16) bool) {
	t := h.t
	rq := h.r.requests
	for pid, set := range rq.requestsByPeer {
		require.True(t, active(pid), "requests held for inactive peer %d", pid)
		require.LessOrEqual(t, len(set), maxRequestsPerPeer)
	}
	for key, owner := range rq.requestsByTx {
		_, ok := rq.requestsByPeer[owner][key]
		require.True(t, ok, "request owner has no slot")
	}
}

// checkSeenTracker runs with h.pool.seenTracker.mtx held.
func (h *catFuzzHarness) checkSeenTracker(active func(uint16) bool) {
	t := h.t
	st := h.pool.seenTracker
	counts := map[uint16]int{}
	pending := 0
	for key, e := range st.txByKey {
		require.Equal(t, key, e.txKey)
		require.NotEmpty(t, e.peers, "tracked tx with no peers")
		for pid := range e.peers {
			require.True(t, active(pid), "tracker holds inactive peer %d", pid)
			counts[pid]++
		}
		if e.pendingTxInfo != nil {
			pending++
		}
	}
	require.Equal(t, len(counts), len(st.txCountByPeer))
	for pid, n := range counts {
		require.Equal(t, n, st.txCountByPeer[pid])
		require.LessOrEqual(t, n, st.perPeerLimit)
	}
	queued := 0
	for signerKey, q := range st.pendingTxsBySigner {
		require.NotEmpty(t, q)
		require.LessOrEqual(t, len(q), st.perSignerLimit)
		for k, e := range q {
			require.NotNil(t, e.pendingTxInfo)
			require.Equal(t, signerKey, e.pendingTxInfo.signerKey)
			require.Same(t, st.txByKey[e.txKey], e)
			if k > 0 {
				require.LessOrEqual(t, q[k-1].pendingTxInfo.sequence, e.pendingTxInfo.sequence)
			}
		}
		queued += len(q)
	}
	require.Equal(t, pending, queued)
}

// checkReceivedBuffer runs with h.r.receivedBuffer.mu held.
func (h *catFuzzHarness) checkReceivedBuffer() {
	t := h.t
	rb := h.r.receivedBuffer
	byPeer := map[string]int{}
	for _, buf := range rb.buffers {
		require.NotEmpty(t, buf)
		require.LessOrEqual(t, len(buf), maxReceivedBufferSize)
		for seq, e := range buf {
			require.Equal(t, seq, e.sequence)
			byPeer[e.peerID]++
		}
	}
	require.Equal(t, byPeer, rb.countByPeer)
	for _, n := range rb.countByPeer {
		require.LessOrEqual(t, n, maxRequestsPerPeer)
	}
}

// checkNoStaleBuffered runs after a refresh and checks that no buffered tx is
// below its signer's expected sequence.
func (h *catFuzzHarness) checkNoStaleBuffered() {
	if !fzCheckStaleBuffered {
		return
	}
	rb := h.r.receivedBuffer
	rb.mu.Lock()
	defer rb.mu.Unlock()
	for signer, buf := range rb.buffers {
		expected := h.app.seq([]byte(signer))
		for seq := range buf {
			require.GreaterOrEqual(h.t, seq, expected, "signer %q", signer)
		}
	}
}
