package propagation

// Model-based tests: replay ITF traces of the Quint model in
// spec/consensus/propagation (propagation.qnt; its README is the contract)
// against the real propagation reactor, built by the fuzz harness
// (reactor_fuzz_test.go).
//
// How a trace is replayed
//
// Each state of a trace records the step that led to it (lastAction).
// Environment steps (env = true) are applied to the reactor through the same
// entry points the fuzz ops use: peer messages go through proto encoding and
// decoding and Reactor.Receive on one goroutine (the driver's RECV goroutine,
// standing in for the p2p processor), consensus calls are direct method calls.
// Internal steps of the model are not driven, with two exceptions: the
// consumer of proposalChan and partChan is the driver, which reads one item at
// each syncTakeProp / syncTakePart step, and consApply (see below). Both
// channels have the model's capacity 1. After each step the driver waits
// until every reactor goroutine is idle or parked on a channel send, then
// compares.
//
// SetConsensusState is split as the model splits it (README abstraction 11):
// enterRound installs the height, round and proposer (the first half of
// SetConsensusState); when the model picks a cached compact block, its
// consApply step runs the body of applyCachedProposalIfAvailable's loop for
// the peer the model picked (Go map order is a free choice, abstraction 16):
// validateCompactBlock, handleCachedCompactBlock, grantPushToCachedSenders,
// DeleteUnverifiedProposal. When the model picks nothing, enterRound calls the
// whole SetConsensusState.
//
// Fixtures (README "Scope"): A is fuzz fixture 0 (round 0) and 2 (round 1).
// B is a 4-part block built here (fuzz fixture 1 has 2 parts, not K = 4), at
// round 0 and 1. X is A's compact block with another block hash, re-signed
// (fuzz op 1, mutation 4). The mempool holds the transactions that rebuild
// exactly one original part of A (and X) and of B; that part is canonical
// original 0 (MEMPOOL = 1). The other canonical originals map to the other
// part indexes in increasing order; parity K+j is part K+j.
//
// What is compared after each step (strict mode)
//
// The model runs goroutine steps in any order; Go runs them eagerly. A model
// state is settled when no RECV or DELIV step is enabled (each is idle or
// blocked on a full channel); there the model and the reactor must agree
// exactly. Elsewhere Go may be ahead by the pending work.
//
// In every state:
//   - the reactor's round (prop.round) and height (H = 5);
//   - per peer, the unverified compact block cached for H (has, round,
//     identity) (prop.unverified);
//   - per round, the entry's kind (none / full / stub), identity (block hash
//     mapped to A/B/X, part set header mapped to A/B), forwarded and catchup
//     flags (prop.entries);
//   - each item the driver reads from proposalChan (round, identity, from =
//     peer or self) or partChan (round) at a syncTake step against the head
//     the model takes (obs.propLog is the sequence of these sends).
//     Producers blocked on the same full channel may enter it in another
//     order in Go than in the trace; an item read out of order must be one
//     the model still has in the channel or a producer about to send it, and
//     at the end the multiset of proposals Go delivers (read, then drained)
//     must equal the model's (taken, in the channel, or about to be sent).
//
// In settled states, also:
//   - held parts per round (orig mapped to canonical indexes, parity count),
//     whether the block is complete follows;
//   - prop.gossiped against the compact blocks the mock peers received from
//     broadcastCompactBlock (round, identity);
//   - the number of items in proposalChan and partChan;
//   - whether RECV is busy (blocked on a send) and the number of live
//     deliverCachedCompactBlock goroutines.
//
// In unsettled states, held parts must contain the model's for rounds with
// pending part work and equal them elsewhere; gossip must contain the
// model's and be within it plus a pending gossip.
//
// Parity: the model builds parity in the step that completes the originals
// (withParity; decoding rebuilds all parts at once, abstraction 3). Go builds
// it in buildParity after handing the rebuilt or received originals to
// consensus, so while a RECV or DELIV send is still parked Go's parity only
// has to be within the model's.
//
// What is not compared
//
//   - request bookkeeping (prop.reqO / reqP versus per-index request bits,
//     push allowances; abstractions 6, 7, 14). When the model admits a part
//     that Go would not admit (Go requests per index, asynchronously, and the
//     model folds the request into the have), the driver records the request
//     with PeerState.RequestParts, as the reactor does when it sends a want,
//     before sending the part. When the model drops a part only because it
//     was not requested but Go would admit it (Go requested it, e.g. from the
//     retryWants that AddCommitment starts, or the sender may push it), the
//     part is not sent: the model says nothing about that part.
//   - consensus-side state (cons, syncd, pmq, obs.consLog, votes): no
//     consensus runs here; vote steps do nothing.
//   - hist flags; the model's own properties are checked by the model.
//   - want handling beyond "it does not break anything" (abstraction 8).
//
// Schedule divergence
//
// If the trace applies an environment step while the model still has
// pending RECV/DELIV work that the step does not commute with (a round change
// that makes a pending mempool recovery irrelevant, an AddCommitment that
// replaces an entry while recovery or decoding is pending, a part for a round
// whose mempool recovery is pending), or that leaves two producers (RECV and
// a DELIV, or two DELIVs) with more sends pending to one channel than it can
// hold (which of them takes the next slot is up to the Go runtime, and Go's
// eager producer already took it), or while Go's RECV is still busy after
// producers swapped places on a channel, the trace follows an interleaving
// that Go's eager execution did not take. From that step on the replay only
// checks that the reactor never hangs and never forwards a (round, identity)
// twice. Every committed fixed_* trace must replay without divergence.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/p2p"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	cmttypes "github.com/cometbft/cometbft/types"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// mbtHeight is the model's height H (the fuzz harness's height 5).
	mbtHeight = int64(5)
	// mbtK is the model's K: original parts per block.
	mbtK = 4
	// mbtCallTimeout bounds every consensus call into the reactor: the
	// consensus receive routine must never block on the propagator.
	mbtCallTimeout = 20 * time.Second
	// mbtQuiesceTimeout bounds the wait for reactor goroutines to go idle.
	mbtQuiesceTimeout = 30 * time.Second
)

// mbtFixtures maps the model's blocks to signed compact blocks and parts.
type mbtFixtures struct {
	fx *fuzzFixtures
	// cbs[block][round] is the compact block the model calls block at round.
	cbs map[string]map[int]*proptypes.CompactBlock
	// parts[psh] holds the parts of the part set header psh ("A" or "B").
	parts map[string]*fuzzBlock
	// orig[psh][i] is the part index of canonical original i.
	orig    map[string][]uint32
	mempool cmttypes.Txs
	// names maps block hashes and part set header hashes to model names.
	blockNames map[string]string
	pshNames   map[string]string
}

var (
	mbtFixturesOnce sync.Once
	mbtFixturesVal  *mbtFixtures
)

func loadMBTFixtures(tb testing.TB) *mbtFixtures {
	fx := loadFuzzFixtures(tb)
	mbtFixturesOnce.Do(func() {
		a0, a1 := fx.blocks[0], fx.blocks[2]
		txsB := deterministicTxs(8, 30_000, 50_000, 70_000, 40_000, 60_000)
		b0, b1 := fx.makeBlock(tb, mbtHeight, 0, txsB), fx.makeBlock(tb, mbtHeight, 1, txsB)
		require.EqualValues(tb, mbtK, a0.original.Total())
		require.EqualValues(tb, mbtK, b0.original.Total())
		require.True(tb, b0.original.Header().Equals(b1.original.Header()))
		require.True(tb, a0.original.Header().Equals(a1.original.Header()))
		fx.extra = append(fx.extra, b0)

		xHash := tmhash.Sum([]byte("model block X"))
		x0, x1 := cloneCompactBlock(tb, a0.cb), cloneCompactBlock(tb, a1.cb)
		for _, x := range []*proptypes.CompactBlock{x0, x1} {
			x.Proposal.BlockID.Hash = xHash
			fx.sign(tb, x, true)
		}

		m := &mbtFixtures{
			fx: fx,
			cbs: map[string]map[int]*proptypes.CompactBlock{
				"A": {0: a0.cb, 1: a1.cb},
				"B": {0: b0.cb, 1: b1.cb},
				"X": {0: x0, 1: x1},
			},
			parts:      map[string]*fuzzBlock{"A": a0, "B": b0},
			orig:       map[string][]uint32{},
			blockNames: map[string]string{string(a0.cb.Proposal.BlockID.Hash): "A", string(b0.cb.Proposal.BlockID.Hash): "B", string(xHash): "X"},
			pshNames:   map[string]string{string(a0.original.Hash()): "A", string(b0.original.Hash()): "B"},
		}
		for _, name := range []string{"A", "B"} {
			txs, idx := mempoolForOnePart(tb, m.parts[name])
			m.mempool = append(m.mempool, txs...)
			order := []uint32{idx}
			for i := uint32(0); i < mbtK; i++ {
				if i != idx {
					order = append(order, i)
				}
			}
			m.orig[name] = order
		}
		mbtFixturesVal = m
	})
	return mbtFixturesVal
}

// mempoolForOnePart returns the smallest set of b's transactions from which
// recoverPartsFromMempool rebuilds exactly one original part, and that part.
func mempoolForOnePart(tb testing.TB, b *fuzzBlock) (cmttypes.Txs, uint32) {
	n := len(b.txs)
	masks := make([]int, 0, 1<<n)
	for mask := 1; mask < 1<<n; mask++ {
		masks = append(masks, mask)
	}
	sort.SliceStable(masks, func(i, j int) bool { return popcount(masks[i]) < popcount(masks[j]) })
	for _, mask := range masks {
		var txs cmttypes.Txs
		var found []proptypes.UnmarshalledTx
		for i := 0; i < n; i++ {
			if mask&(1<<i) == 0 {
				continue
			}
			tx := b.txs[i]
			prefix := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.BytesType), uint64(len(tx)))
			found = append(found, proptypes.UnmarshalledTx{MetaData: b.cb.Blobs[i], Key: tx.Key(), Prefix: prefix, TxBytes: tx})
			txs = append(txs, tx)
		}
		parts, err := proptypes.TxsToParts(found, b.original.Total(), cmttypes.BlockPartSizeBytes, b.cb.LastLen)
		require.NoError(tb, err)
		if len(parts) == 1 {
			return txs, parts[0].Index
		}
	}
	tb.Fatal("no mempool rebuilds exactly one part")
	return nil, 0
}

func popcount(x int) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

// goIdx maps a canonical model part index of part set psh to a part index.
func (m *mbtFixtures) goIdx(psh string, idx int) uint32 {
	if idx < mbtK {
		return m.orig[psh][idx]
	}
	return uint32(idx)
}

// canonOrig maps an original part index of psh to its canonical index.
func (m *mbtFixtures) canonOrig(psh string, idx int) int {
	for i, g := range m.orig[psh] {
		if int(g) == idx {
			return i
		}
	}
	return -1
}

func (m *mbtFixtures) blockName(hash []byte) string {
	if n, ok := m.blockNames[string(hash)]; ok {
		return n
	}
	return fmt.Sprintf("?%X", hash)
}

func (m *mbtFixtures) pshName(psh cmttypes.PartSetHeader) string {
	if n, ok := m.pshNames[string(psh.Hash)]; ok && psh.Total == mbtK {
		return n
	}
	return fmt.Sprintf("?%v", psh)
}

// mbtMode selects what a replay checks.
type mbtMode int

const (
	// mbtConformance compares the reactor with the model after each step.
	mbtConformance mbtMode = iota
	// mbtRegression replays only the environment steps of a trace of a
	// model with ab6a3df reverted, and checks the fixed reactor against the
	// properties the reverted model violates. This is a regression check,
	// not conformance: the reactor is not expected to match those traces.
	mbtRegression
)

type mbtPart struct {
	round int
	index uint32
}

// mbtStats counts what a replay did, for the test log.
type mbtStats struct {
	steps, envSteps, strictSteps, settledChecks, takes int
	skippedParts, requestedParts, reorders             int
	divergedAt                                         int
	divergeReason                                      string
}

type mbtDriver struct {
	t    *testing.T
	m    *mbtFixtures
	f    *reactorFuzzer
	r    *Reactor
	mode mbtMode
	tr   *itfTrace

	peers map[string]*fuzzPeer
	names map[p2p.ID]string

	recvQ       chan p2p.Envelope
	recvPending atomic.Int32
	done        chan struct{}

	gidMtx sync.Mutex
	gids   map[string]bool // goroutines of this driver

	obsMtx   sync.Mutex
	gossiped map[itfRoundBlock]int

	// consumer side
	goProps                 []itfProposal // read from proposalChan
	goParts                 []mbtPart     // read from partChan
	modelProps              []itfProposal // taken by the model
	extraGoProp, extraMProp []itfProposal // read out of order
	extraGoPart, extraMPart []int
	// rounds an AddCommitment replaced: a second forward there is by design
	replaced map[int]bool
	// picked is the cached compact block SetConsensusState's loop picked
	// and has not applied yet (between enterRound and consApply)
	picked *proptypes.CompactBlock

	strict bool
	step   int
	act    itfAction
	stats  mbtStats
}

func newMBTDriver(t *testing.T, m *mbtFixtures, tr *itfTrace, mode mbtMode) *mbtDriver {
	d := &mbtDriver{
		t: t, m: m, mode: mode, tr: tr,
		peers:    map[string]*fuzzPeer{},
		names:    map[p2p.ID]string{},
		recvQ:    make(chan p2p.Envelope, 64),
		done:     make(chan struct{}),
		gids:     map[string]bool{curGID(): true},
		gossiped: map[itfRoundBlock]int{},
		replaced: map[int]bool{},
		strict:   mode == mbtConformance,
	}
	// empty mask (the mempool is the replay's), two peers at height 5.
	rd := &fuzzReader{data: []byte{0, 0, 0, 0, 1, 1, 1}}
	d.f = newReactorFuzzerWith(t, m.fx, rd, fuzzerOptions{
		noDrain: true,
		chanCap: 1,
		mempool: m.mempool,
		observe: d.observe,
	})
	d.r = d.f.r
	require.Len(t, d.f.peers, 2)
	for i, name := range []string{"p1", "p2"} {
		d.peers[name] = d.f.peers[i]
		d.names[d.f.peers[i].ID()] = name
	}
	d.names[d.r.self] = "self"
	d.f.addSigned(mbtHeight, m.parts["B"].original.Header())
	go d.recvLoop()
	t.Cleanup(func() { close(d.done) })
	return d
}

func curGID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	f := strings.Fields(string(buf))
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

func (d *mbtDriver) addGID(id string) {
	d.gidMtx.Lock()
	d.gids[id] = true
	d.gidMtx.Unlock()
}

func (d *mbtDriver) isOwnGID(id string) bool {
	d.gidMtx.Lock()
	defer d.gidMtx.Unlock()
	return d.gids[id]
}

// recvLoop stands in for the p2p processor: one goroutine handles the peer
// messages in order.
func (d *mbtDriver) recvLoop() {
	d.addGID(curGID())
	for {
		select {
		case <-d.done:
			return
		case <-d.r.ctx.Done():
			return
		case e := <-d.recvQ:
			d.r.Receive(e)
			d.recvPending.Add(-1)
		}
	}
}

func (d *mbtDriver) observe(peer *fuzzPeer, e p2p.Envelope) {
	cb, ok := e.Message.(*propproto.CompactBlock)
	if !ok {
		return
	}
	k := itfRoundBlock{Round: int(cb.Proposal.Round), Block: d.m.blockName(cb.Proposal.BlockID.Hash)}
	d.obsMtx.Lock()
	d.gossiped[k]++
	d.obsMtx.Unlock()
}

func (d *mbtDriver) failf(format string, args ...any) {
	d.t.Helper()
	d.t.Fatalf("trace %s, step %d, action %s: %s", d.tr.Name, d.step, fmtAction(d.act), fmt.Sprintf(format, args...))
}

func fmtAction(a itfAction) string {
	s := a.Kind
	if a.Peer != "" {
		s += " peer=" + a.Peer
	}
	if a.Round >= 0 {
		s += " round=" + strconv.Itoa(a.Round)
	}
	for _, kv := range [][2]string{{"block", a.Block}, {"psh", a.Psh}, {"part", a.Part}} {
		if kv[1] != "" {
			s += " " + kv[0] + "=" + kv[1]
		}
	}
	if a.Kind == "part" || a.Kind == "have" {
		s += fmt.Sprintf(" idx=%d corrupt=%v proof=%v", a.Idx, a.Corrupt, a.Proof)
	}
	return s + fmt.Sprintf(" accepted=%v", a.Accepted)
}

// mismatch fails the test with both views.
func (d *mbtDriver) mismatch(what string, model, goView any) {
	d.t.Helper()
	d.failf("%s differs\n  model: %+v\n  go:    %+v", what, model, goView)
}

func (d *mbtDriver) diverge(reason string) {
	if !d.strict {
		return
	}
	d.strict = false
	d.stats.divergedAt = d.step
	d.stats.divergeReason = reason
}

// ---------------------------------------------------------------- replay

func (d *mbtDriver) replay() {
	tr := d.tr
	for i := 1; i < len(tr.States); i++ {
		pre, post := &tr.States[i-1], &tr.States[i]
		d.step, d.act = i, post.LastAction
		d.stats.steps++
		if d.act.Env {
			d.stats.envSteps++
		}
		if d.strict {
			if reason := mbtInterference(pre, post, d.act); reason != "" {
				d.diverge(reason)
			}
		}
		d.apply(pre, post)
		d.quiesce()
		if d.strict {
			d.stats.strictSteps++
			d.compare(post)
		}
	}
	d.finish(&tr.States[len(tr.States)-1])
}

// call runs a consensus call into the reactor; it must return in bounded
// time, as the consensus receive routine holds its locks during it.
func (d *mbtDriver) call(name string, fn func()) {
	d.t.Helper()
	done := make(chan struct{})
	go func() {
		d.addGID(curGID())
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(mbtCallTimeout):
		d.failf("%s did not return within %v (blocked hand-off)\n%s", name, mbtCallTimeout, allStacks())
	}
	d.r.ticker.Stop() // retries are driven by retryWants steps
}

func (d *mbtDriver) apply(pre, post *itfState) {
	a := d.act
	switch a.Kind {
	case "compactBlock":
		d.sendPeer(a.Peer, DataChannel, &propproto.Message{Sum: &propproto.Message_CompactBlock{CompactBlock: d.m.cbs[a.Block][a.Round].ToProto()}})
	case "have":
		d.applyHave(pre, a)
	case "want":
		ba := bits.NewBitArray(2 * mbtK)
		ba.Fill()
		want := &propproto.WantParts{Height: mbtHeight, Round: int32(a.Round), Parts: *ba.ToProto(), MissingPartsCount: mbtK}
		d.sendPeer(a.Peer, WantChannel, &propproto.Message{Sum: &propproto.Message_WantParts{WantParts: want}})
	case "part":
		d.applyPart(pre, a)
	case "enterRound":
		d.applyEnterRound(post, a)
	case "consApply":
		d.applyCached(pre, post, a)
	case "addCommitment":
		if e := pre.Prop.Entries[a.Round]; e.Kind == "none" || e.Psh != a.Psh {
			d.replaced[a.Round] = true
		}
		psh := d.m.parts[a.Psh].original.Header()
		d.call("AddCommitment", func() { d.r.AddCommitment(mbtHeight, int32(a.Round), &psh) })
	case "prune":
		d.call("Prune", func() { d.r.Prune(mbtHeight) })
	case "retryWants":
		d.call("retryWants", d.r.retryWants)
	case "syncTakeProp":
		d.takeProp(pre, post)
	case "syncTakePart":
		d.takePart(pre, post)
	default:
		// vote, and goroutine steps the reactor runs by itself
	}
}

// sendPeer delivers msg from peer through the wire decoding path, on the
// RECV goroutine.
func (d *mbtDriver) sendPeer(peer string, ch byte, msg *propproto.Message) {
	d.t.Helper()
	if d.recvPending.Load() != 0 {
		if d.strict && len(d.extraGoProp)+len(d.extraMProp)+len(d.extraGoPart)+len(d.extraMPart) == 0 {
			d.mismatch("RECV before a peer message (the model's RECV is idle)", "idle", "busy")
		}
		d.diverge("Go's RECV still busy after producers swapped places on a channel")
	}
	raw, err := proto.Marshal(msg)
	require.NoError(d.t, err)
	var pb propproto.Message
	require.NoError(d.t, proto.Unmarshal(raw, &pb))
	decoded, err := proptypes.MsgFromProto(&pb)
	require.NoError(d.t, err, "driver built an undecodable message")
	require.NoError(d.t, decoded.ValidateBasic(), "driver built an invalid message")
	p := d.peers[peer]
	d.recvPending.Add(1)
	d.recvQ <- p2p.Envelope{Src: p, ChannelID: ch, Message: &pb}
}

func (d *mbtDriver) peerState(name string) *PeerState {
	ps := d.r.getPeer(d.peers[name].ID())
	require.NotNil(d.t, ps, "peer %s was removed", name)
	return ps
}

// pshOf returns the part set an action on round r refers to: the entry's,
// else A's.
func pshOf(s *itfState, r int) string {
	if e := s.Prop.Entries[r]; e.Kind != "none" && e.Psh != "" {
		return e.Psh
	}
	return "A"
}

// heldIdx returns a part index the entry already holds, for model actions
// whose canonical index is out of range (nothing missing of that kind).
func (d *mbtDriver) heldIdx(psh, kind string) uint32 {
	if kind == "orig" {
		return d.m.goIdx(psh, 0)
	}
	return mbtK
}

func (d *mbtDriver) applyHave(pre *itfState, a itfAction) {
	psh := pshOf(pre, a.Round)
	idx := d.heldIdx(psh, a.Part)
	if a.Idx >= 0 && a.Idx < 2*mbtK {
		idx = d.m.goIdx(psh, a.Idx)
	}
	p, _ := d.m.parts[psh].part(idx)
	have := &propproto.HaveParts{Height: mbtHeight, Round: int32(a.Round), Parts: []*propproto.PartMetaData{{Index: idx, Hash: p.Proof.LeafHash}}}
	d.sendPeer(a.Peer, DataChannel, &propproto.Message{Sum: &propproto.Message_HaveParts{HaveParts: have}})
}

// mbtPartCheck is the model's partAccepted, split into the request check and
// the rest.
func mbtPartCheck(s *itfState, a itfAction) (others, requested bool) {
	e := s.Prop.Entries[a.Round]
	known := e.Kind != "none" && (a.Proof || a.Round >= s.Prop.Round)
	missing := len(e.Orig) < mbtK
	reqs := s.Prop.ReqO
	if a.Part == "parity" {
		missing = e.Par < mbtK
		reqs = s.Prop.ReqP
	}
	parityOK := a.Part == "orig" || e.Kind == "full"
	proofOK := e.Kind == "full" || a.Proof
	requested = slices.Contains(reqs, itfPeerRound{Peer: a.Peer, Round: a.Round})
	return known && missing && parityOK && !a.Corrupt && proofOK, requested
}

func (d *mbtDriver) applyPart(pre *itfState, a itfAction) {
	others, requested := mbtPartCheck(pre, a)
	if a.Accepted != (others && requested) {
		d.failf("driver's reading of partAccepted (%v, requested %v) disagrees with the trace", others, requested)
	}
	psh := pshOf(pre, a.Round)
	idx := d.heldIdx(psh, a.Part)
	if a.Idx >= 0 && a.Idx < 2*mbtK {
		idx = d.m.goIdx(psh, a.Idx)
	}
	if others {
		ps := d.peerState(a.Peer)
		goAdmits := ps.Requested(mbtHeight, int32(a.Round), idx) || hasPushAllowance(ps, int32(a.Round), idx)
		switch {
		case requested && !goAdmits:
			// the model folds the want into the have (abstraction 6): record
			// the request as the reactor does when it sends a want.
			ba := bits.NewBitArray(2 * mbtK)
			ba.SetIndex(int(idx), true)
			want := &propproto.WantParts{Height: mbtHeight, Round: int32(a.Round), Parts: *ba.ToProto(), MissingPartsCount: 1}
			require.True(d.t, ps.RequestParts(mbtHeight, int32(a.Round), ba, p2p.Envelope{ChannelID: WantChannel, Message: want}))
			d.stats.requestedParts++
		case !requested && goAdmits:
			// dropped by the model only for request bookkeeping it does not
			// track per index (abstractions 6, 7, 14): not sent.
			d.stats.skippedParts++
			return
		}
	}
	p, _ := d.m.parts[psh].part(idx)
	data := append([]byte(nil), p.Bytes...)
	if a.Corrupt {
		data[0] ^= 1
	}
	rp := &propproto.RecoveryPart{Height: mbtHeight, Round: int32(a.Round), Index: idx, Data: data}
	if a.Proof {
		rp.Proof = *p.Proof.ToProto()
		if a.Corrupt {
			// passes ValidateBasic, fails verification against the root
			rp.Proof.LeafHash = merkle.LeafHash(data)
		}
	}
	d.sendPeer(a.Peer, DataChannel, &propproto.Message{Sum: &propproto.Message_RecoveryPart{RecoveryPart: rp}})
}

func hasPushAllowance(ps *PeerState, round int32, idx uint32) bool {
	ps.mtx.RLock()
	defer ps.mtx.RUnlock()
	st := ps.state[mbtHeight][round]
	return st != nil && slices.Contains(st.pushAllowance, idx)
}

func (d *mbtDriver) applyEnterRound(post *itfState, a itfAction) {
	round := int32(a.Round)
	if a.Peer == "" || d.mode == mbtRegression {
		d.call("SetConsensusState", func() { d.r.SetConsensusState(mbtHeight, round, d.m.fx.pub) })
		return
	}
	// the first half of SetConsensusState, up to the peer pick;
	// consApply runs the rest
	d.call("SetConsensusState (install)", func() {
		d.r.pmtx.Lock()
		d.r.currentProposer = d.m.fx.pub
		d.r.setHeightAndRoundLocked(mbtHeight, round)
		d.r.pmtx.Unlock()
		d.r.ResetRequestCounts()
	})
	d.pickCached(post)
}

// pickCached is the start of applyCachedProposalIfAvailable's loop body for
// the peer the model picked (post.Cons.Peer): read that peer's cached compact
// block and validate it. The loop keeps the compact block it read, so a later
// StoreUnverifiedProposal from RECV, before handleCachedCompactBlock runs,
// does not change what is applied (it is the slot DeleteUnverifiedProposal
// then clears).
func (d *mbtDriver) pickCached(post *itfState) {
	d.picked = nil
	peer := post.Cons.Peer
	cb := d.peerState(peer).GetUnverifiedProposal(mbtHeight)
	want := itfUnverified{Has: true, Round: post.Cons.CbRound, Block: post.Cons.CbBlock}
	if cb == nil || int(cb.Proposal.Round) != want.Round || d.m.blockName(cb.Proposal.BlockID.Hash) != want.Block {
		if d.strict {
			d.mismatch("cached compact block of "+peer+" at the pick", want, d.goUnverified(peer))
		}
		return
	}
	var err error
	d.call("SetConsensusState (validate cached)", func() { err = d.r.validateCompactBlock(cb) })
	if err != nil {
		if d.strict {
			d.mismatch("cached compact block of "+peer+" validates", "valid", err)
		}
		return
	}
	d.picked = cb
}

// applyCached runs the rest of applyCachedProposalIfAvailable's loop body for
// the compact block picked from pre.Cons.Peer: handleCachedCompactBlock,
// grantPushToCachedSenders, DeleteUnverifiedProposal. On a conflict the loop
// moves on to the next peer, which the model does in the same step.
func (d *mbtDriver) applyCached(pre, post *itfState, a itfAction) {
	if d.mode == mbtRegression {
		return // SetConsensusState already ran whole
	}
	cb := d.picked
	d.picked = nil
	if cb == nil {
		return // only after divergence: pickCached fails otherwise
	}
	ps := d.peerState(pre.Cons.Peer)
	var applied bool
	d.call("SetConsensusState (apply cached)", func() {
		if d.r.handleCachedCompactBlock(cb) {
			d.r.grantPushToCachedSenders(d.r.getPeers(), cb)
			ps.DeleteUnverifiedProposal(mbtHeight)
			applied = true
		}
	})
	if d.strict && applied != a.Accepted {
		d.mismatch("cached compact block applied", a.Accepted, applied)
	}
	if !applied && post.Cons.PC == "apply" {
		d.pickCached(post) // the loop's next peer
	}
}

// --------------------------------------------------------------- consumer

func (d *mbtDriver) goProposal(p ProposalAndSrc) itfProposal {
	from, ok := d.names[p.From]
	if !ok {
		from = "?" + string(p.From)
	}
	return itfProposal{Round: int(p.Proposal.Round), Block: d.m.blockName(p.Proposal.BlockID.Hash), From: from}
}

func (d *mbtDriver) takeProp(pre, post *itfState) {
	d.stats.takes++
	want := pre.PropChan[0]
	d.modelProps = append(d.modelProps, want)
	var got ProposalAndSrc
	select {
	case got = <-d.r.proposalChan:
	default:
		if d.strict {
			if len(d.extraGoProp)+len(d.extraMProp) == 0 {
				d.mismatch("proposal taken from proposalChan", want, "channel empty")
			}
			d.diverge("Go's proposalChan empty after producers swapped places")
		}
		return
	}
	require.Equal(d.t, mbtHeight, got.Proposal.Height)
	g := d.goProposal(got)
	d.goProps = append(d.goProps, g)
	if !d.strict || g == want {
		return
	}
	d.stats.reorders++
	d.extraGoProp, d.extraMProp = settleDebt(d.extraGoProp, d.extraMProp, g, want)
	for _, x := range d.extraGoProp {
		if !slices.Contains(mbtPendingProps(post), x) {
			d.mismatch("proposal taken from proposalChan (no model producer holds Go's item)", want, g)
		}
	}
}

// settleDebt records that Go delivered g where the model took want.
func settleDebt[T comparable](extraGo, extraM []T, g, want T) ([]T, []T) {
	if i := slices.Index(extraM, g); i >= 0 {
		extraM = slices.Delete(extraM, i, i+1)
	} else {
		extraGo = append(extraGo, g)
	}
	if i := slices.Index(extraGo, want); i >= 0 {
		extraGo = slices.Delete(extraGo, i, i+1)
	} else {
		extraM = append(extraM, want)
	}
	return extraGo, extraM
}

func (d *mbtDriver) takePart(pre, post *itfState) {
	d.stats.takes++
	want := pre.PartChan[0]
	var got cmttypes.PartInfo
	select {
	case got = <-d.r.partChan:
	default:
		if d.strict {
			if len(d.extraGoPart)+len(d.extraMPart) == 0 {
				d.mismatch("part taken from partChan (round)", want, "channel empty")
			}
			d.diverge("Go's partChan empty after producers swapped places")
		}
		return
	}
	d.checkPartData(got)
	g := int(got.Round)
	d.goParts = append(d.goParts, mbtPart{round: g, index: got.Index})
	if !d.strict || g == want {
		return
	}
	d.stats.reorders++
	d.extraGoPart, d.extraMPart = settleDebt(d.extraGoPart, d.extraMPart, g, want)
	pending := mbtPendingPartRounds(post)
	for _, x := range d.extraGoPart {
		if !pending[x] {
			d.mismatch("part taken from partChan (round; no model producer holds Go's item)", want, g)
		}
	}
}

// checkPartData checks that a part handed to consensus carries the data of a
// signed block.
func (d *mbtDriver) checkPartData(pi cmttypes.PartInfo) {
	require.Equal(d.t, mbtHeight, pi.Height)
	for _, name := range []string{"A", "B"} {
		if p, ok := d.m.parts[name].part(pi.Index); ok && pi.Index < mbtK && bytes.Equal(p.Bytes, pi.Bytes) {
			return
		}
	}
	d.failf("part %d of round %d sent to consensus matches no signed block", pi.Index, pi.Round)
}

// mbtPendingProps are the proposals the model holds in proposalChan or that a
// producer is about to send.
func mbtPendingProps(s *itfState) []itfProposal {
	out := append([]itfProposal(nil), s.PropChan...)
	if s.Recv.PC == "sendProp" {
		out = append(out, itfProposal{Round: s.Recv.Round, Block: s.Recv.Block, From: s.Recv.From})
	}
	for r, dl := range s.Deliv {
		if dl.PC == "sendProp" {
			out = append(out, itfProposal{Round: r, Block: dl.Block, From: "self"})
		}
	}
	return out
}

// mbtPendingPartRounds are the rounds whose parts the model holds in partChan
// or may still send.
func mbtPendingPartRounds(s *itfState) map[int]bool {
	out := map[int]bool{}
	for _, r := range s.PartChan {
		out[r] = true
	}
	if s.Recv.PC != "idle" {
		out[s.Recv.Round] = true
	}
	for r, dl := range s.Deliv {
		if dl.PC != "none" {
			out[r] = true
		}
	}
	return out
}

// ------------------------------------------------------- model scheduling

// mbtSettled reports whether no RECV or DELIV step is enabled in s (the model
// configurations replayed here have channel capacities 1).
func mbtSettled(s *itfState) bool {
	propFull, partFull := len(s.PropChan) >= 1, len(s.PartChan) >= 1
	blockedOrIdle := func(pc, idle string) bool {
		return pc == idle || (pc == "sendProp" && propFull) || (pc == "sendParts" && partFull)
	}
	if !blockedOrIdle(s.Recv.PC, "idle") {
		return false
	}
	for _, dl := range s.Deliv {
		if !blockedOrIdle(dl.PC, "none") {
			return false
		}
	}
	return true
}

// mbtPendingRecover returns the rounds where a RECV or DELIV step of s will
// still run recoverPartsFromMempool and the entry lacks the mempool part, and
// whether RECV will still try to decode.
func mbtPendingRecover(s *itfState) (map[int]bool, bool) {
	rounds := map[int]bool{}
	lacks := func(r int) bool { return !slices.Contains(s.Prop.Entries[r].Orig, 0) }
	if (s.Recv.PC == "sendProp" && s.Recv.Added) || s.Recv.PC == "gossip" {
		if lacks(s.Recv.Round) {
			rounds[s.Recv.Round] = true
		}
	}
	for r, dl := range s.Deliv {
		if (dl.PC == "sendProp" || dl.PC == "recover") && lacks(r) {
			rounds[r] = true
		}
	}
	return rounds, s.Recv.PC == "sendParts" && s.Recv.DecodeAfter
}

// mbtPendingPartWork returns the rounds whose held parts pending RECV or DELIV
// steps of s may still change.
func mbtPendingPartWork(s *itfState) map[int]bool {
	rounds := map[int]bool{}
	if (s.Recv.PC == "sendProp" && s.Recv.Added) || s.Recv.PC == "gossip" || (s.Recv.PC == "sendParts" && s.Recv.DecodeAfter) {
		rounds[s.Recv.Round] = true
	}
	for r, dl := range s.Deliv {
		if dl.PC == "sendProp" || dl.PC == "recover" {
			rounds[r] = true
		}
	}
	return rounds
}

// mbtInterference reports why environment step a, taken in s, does not
// commute with the RECV/DELIV work pending in s (Go ran that work before a),
// or "" if it commutes.
func mbtInterference(s, post *itfState, a itfAction) string {
	if reason := mbtProducerRace(s, post, a); reason != "" {
		return reason
	}
	if !a.Env {
		return ""
	}
	recover, decode := mbtPendingRecover(s)
	switch a.Kind {
	case "enterRound":
		// a recovery that was relevant becomes irrelevant: the model then
		// rebuilds nothing, Go already did
		for r := range recover {
			if r >= s.Prop.Round && r < a.Round {
				return fmt.Sprintf("enterRound(%d) with mempool recovery pending for round %d", a.Round, r)
			}
		}
	case "addCommitment":
		e := s.Prop.Entries[a.Round]
		if e.Kind != "none" && e.Psh == a.Psh {
			return "" // the entry is kept
		}
		// the replaced entry's pending recovery or decoding goes to the stub
		// in the model and went to the old part set in Go; the round reset
		// to 0 makes an irrelevant pending recovery relevant again
		// RECV parked on the full proposalChan has not reached the gossip in
		// Go either; after the replacement both skip the gossip and the
		// mempool recovery (broadcastCompactBlock's identity check).
		recvParked := s.Recv.PC == "sendProp" && s.Recv.Added && s.Recv.Round == a.Round && len(s.PropChan) >= 1
		delivRecover := a.Round < len(s.Deliv) && (s.Deliv[a.Round].PC == "sendProp" || s.Deliv[a.Round].PC == "recover")
		if (recover[a.Round] && (!recvParked || delivRecover)) || (decode && s.Recv.Round == a.Round) {
			return fmt.Sprintf("addCommitment(%d, %s) replaces an entry with recovery or decoding pending", a.Round, a.Psh)
		}
		for r := range recover {
			if r < s.Prop.Round {
				return fmt.Sprintf("addCommitment(%d, %s) resets the round with recovery pending for round %d", a.Round, a.Psh, r)
			}
		}
	case "part":
		if recover[a.Round] {
			return fmt.Sprintf("part for round %d with mempool recovery pending", a.Round)
		}
	}
	return ""
}

// mbtProducerRace reports a step after which two goroutines (RECV and a
// DELIV, or two DELIVs) have sends to the same channel pending while that
// channel cannot take them all (capacity 1). Go ran the older producer
// eagerly, so it took the free slot first; the trace may give it to the
// newer one, and with both waiting the Go runtime picks either. Which
// producer stays blocked is then no longer determined by the trace.
func mbtProducerRace(s, post *itfState, a itfAction) string {
	newProducer := false
	switch a.Kind {
	case "compactBlock", "part":
		newProducer = post.Recv.PC != "idle"
	case "consApply":
		newProducer = s.Deliv[a.Round].PC == "none" && post.Deliv[a.Round].PC != "none"
	}
	if !newProducer {
		return ""
	}
	props, parts := mbtPendingSends(post)
	for name, ch := range map[string]struct {
		producers, items int
	}{"proposalChan": {len(props), len(post.PropChan)}, "partChan": {len(parts), len(post.PartChan)}} {
		total := ch.items
		for _, n := range props {
			if name == "proposalChan" {
				total += n
			}
		}
		for _, n := range parts {
			if name == "partChan" {
				total += n
			}
		}
		if ch.producers > 1 && total > 1 {
			return fmt.Sprintf("%s leaves two producers with sends to %s pending", a.Kind, name)
		}
	}
	return ""
}

// mbtPendingSends returns, per producer ("recv", "deliv0", "deliv1"), how many
// proposals and how many parts it may still send in s. A pending mempool
// recovery counts one part for the first producer per round only, since the
// one that runs first rebuilds the part; a pending decode counts two.
func mbtPendingSends(s *itfState) (props, parts map[string]int) {
	props, parts = map[string]int{}, map[string]int{}
	recovering := map[int]bool{}
	lacks := func(r int) bool { return !slices.Contains(s.Prop.Entries[r].Orig, 0) }
	recoverPart := func(who string, r int) {
		if lacks(r) && !recovering[r] {
			recovering[r] = true
			parts[who]++
		}
	}
	switch rv := s.Recv; rv.PC {
	case "sendProp":
		props["recv"]++
		if rv.Added {
			recoverPart("recv", rv.Round)
		}
	case "gossip":
		recoverPart("recv", rv.Round)
	case "sendParts":
		parts["recv"] += rv.N
		if rv.DecodeAfter {
			parts["recv"] += 2
		}
	}
	for r := 0; r < len(s.Deliv); r++ {
		dl, who := s.Deliv[r], "deliv"+strconv.Itoa(r)
		switch dl.PC {
		case "sendProp":
			props[who]++
			recoverPart(who, r)
		case "recover":
			recoverPart(who, r)
		case "sendParts":
			parts[who] += dl.N
		}
	}
	for k, n := range parts {
		if n == 0 {
			delete(parts, k)
		}
	}
	return props, parts
}

// ------------------------------------------------------------ go views

// mbtIdent is what the replay compares of an entry in every state.
type mbtIdent struct {
	Kind, Block, Psh   string
	Forwarded, Catchup bool
}

type mbtGoEntry struct {
	Kind, Block, Psh   string
	Forwarded, Catchup bool
	Orig               []int // canonical
	Parity             []int // parity-local indexes
}

func (d *mbtDriver) goEntry(round int) mbtGoEntry {
	d.r.pmtx.Lock()
	pd := d.r.proposals[mbtHeight][int32(round)]
	d.r.pmtx.Unlock()
	if pd == nil {
		return mbtGoEntry{Kind: "none"}
	}
	d.r.pmtx.Lock()
	e := mbtGoEntry{Forwarded: pd.forwarded, Catchup: pd.catchup}
	cb := pd.compactBlock
	d.r.pmtx.Unlock()
	e.Psh = d.m.pshName(pd.block.Original().Header())
	if cb.Proposal.BlockID.IsZero() {
		e.Kind = "stub"
	} else {
		e.Kind, e.Block = "full", d.m.blockName(cb.Proposal.BlockID.Hash)
	}
	for _, i := range pd.block.BitArray().GetTrueIndices() {
		if i < mbtK {
			e.Orig = append(e.Orig, d.m.canonOrig(e.Psh, i))
		} else {
			e.Parity = append(e.Parity, i-mbtK)
		}
	}
	sort.Ints(e.Orig)
	return e
}

func (d *mbtDriver) goUnverified(peer string) itfUnverified {
	cb := d.peerState(peer).GetUnverifiedProposal(mbtHeight)
	if cb == nil {
		return itfUnverified{Round: -1}
	}
	return itfUnverified{Has: true, Round: int(cb.Proposal.Round), Block: d.m.blockName(cb.Proposal.BlockID.Hash)}
}

func (d *mbtDriver) goGossiped() []itfRoundBlock {
	d.obsMtx.Lock()
	defer d.obsMtx.Unlock()
	out := make([]itfRoundBlock, 0, len(d.gossiped))
	for k := range d.gossiped {
		out = append(out, k)
	}
	return out
}

func sortRB(s []itfRoundBlock) []itfRoundBlock {
	out := append([]itfRoundBlock{}, s...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Round != out[j].Round {
			return out[i].Round < out[j].Round
		}
		return out[i].Block < out[j].Block
	})
	return out
}

// ------------------------------------------------------------- compare

func (d *mbtDriver) compare(post *itfState) {
	d.t.Helper()
	d.r.pmtx.Lock()
	height, round := d.r.height, d.r.round
	d.r.pmtx.Unlock()
	if height != mbtHeight || int(round) != post.Prop.Round {
		d.mismatch("reactor height/round (prop.round)", fmt.Sprintf("%d/%d", mbtHeight, post.Prop.Round), fmt.Sprintf("%d/%d", height, round))
	}
	for _, peer := range []string{"p1", "p2"} {
		m := post.Prop.Unverified[peer]
		if !m.Has {
			m = itfUnverified{Round: -1}
		}
		if g := d.goUnverified(peer); g != m {
			d.mismatch("unverified compact block of "+peer+" (prop.unverified)", m, g)
		}
	}

	settled := mbtSettled(post)
	pending := mbtPendingPartWork(post)
	senderParked := d.recvPending.Load() != 0 || d.liveDelivers() > 0
	for r := 0; r < 2; r++ {
		m, g := post.Prop.Entries[r], d.goEntry(r)
		mIdent := mbtIdent{Kind: m.Kind, Block: m.Block, Psh: m.Psh, Forwarded: m.Forwarded, Catchup: m.Catchup}
		gIdent := mbtIdent{Kind: g.Kind, Block: g.Block, Psh: g.Psh, Forwarded: g.Forwarded, Catchup: g.Catchup}
		if m.Kind == "none" {
			mIdent.Psh = ""
		}
		if mIdent != gIdent {
			d.mismatch(fmt.Sprintf("entry of round %d: kind, identity, forwarded, catchup (prop.entries)", r), mIdent, gIdent)
		}
		mPar := make([]int, 0, m.Par)
		for i := 0; i < m.Par; i++ {
			mPar = append(mPar, i)
		}
		mOrig := append([]int{}, m.Orig...)
		gOrig, gPar := append([]int{}, g.Orig...), append([]int{}, g.Parity...)
		exact := settled || !pending[r]
		// The model builds parity together with the last original
		// (withParity, and decoding rebuilds all parts at once, abstraction
		// 3). Go builds it in buildParity after the rebuilt originals were
		// handed to consensus, so it lags while a sender is still parked.
		parityLags := len(mOrig) == mbtK && senderParked
		parOK := slices.Equal(mPar, gPar) || (parityLags && isSubset(gPar, mPar))
		ok := slices.Equal(mOrig, gOrig) && parOK
		if !exact {
			ok = isSubset(mOrig, gOrig) && (isSubset(mPar, gPar) || parityLags)
		}
		if !ok {
			d.mismatch(fmt.Sprintf("held parts of round %d (orig canonical, parity local; exact=%v)", r, exact),
				fmt.Sprintf("orig %v parity %v", mOrig, mPar), fmt.Sprintf("orig %v parity %v", gOrig, gPar))
		}
	}

	mg, gg := sortRB(post.Prop.Gossiped), sortRB(d.goGossiped())
	if settled {
		if !slices.Equal(mg, gg) {
			d.mismatch("compact blocks gossiped (prop.gossiped)", mg, gg)
		}
	} else {
		upper := append([]itfRoundBlock{}, mg...)
		if post.Recv.PC == "gossip" || (post.Recv.PC == "sendProp" && post.Recv.Added) {
			upper = append(upper, itfRoundBlock{Round: post.Recv.Round, Block: post.Recv.Block})
		}
		if !isSubset(mg, gg) || !isSubset(gg, upper) {
			d.mismatch("compact blocks gossiped (prop.gossiped, pending gossip allowed)", mg, gg)
		}
	}

	if !settled {
		return
	}
	d.stats.settledChecks++
	if lp, lc := len(d.r.proposalChan), len(d.r.partChan); lp != len(post.PropChan) || lc != len(post.PartChan) {
		d.mismatch("items in proposalChan/partChan", fmt.Sprintf("%d/%d", len(post.PropChan), len(post.PartChan)), fmt.Sprintf("%d/%d", lp, lc))
	}
	mRecv := post.Recv.PC != "idle"
	if gRecv := d.recvPending.Load() != 0; gRecv != mRecv {
		d.mismatch("RECV blocked on a send", fmt.Sprintf("%v (pc %s)", mRecv, post.Recv.PC), gRecv)
	}
	mDeliv := 0
	for _, dl := range post.Deliv {
		if dl.PC != "none" {
			mDeliv++
		}
	}
	if gDeliv := d.liveDelivers(); gDeliv != mDeliv {
		d.mismatch("live deliverCachedCompactBlock goroutines", mDeliv, gDeliv)
	}
}

func isSubset[T comparable](sub, super []T) bool {
	for _, x := range sub {
		if !slices.Contains(super, x) {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------- finish

// finish drains both channels, then checks the proposals Go delivered.
func (d *mbtDriver) finish(last *itfState) {
	d.t.Helper()
	d.step, d.act = len(d.tr.States), itfAction{Kind: "end of trace (consumer drains)", Round: -1}
	drained := d.drain()
	all := append(append([]itfProposal{}, d.goProps...), drained...)

	// never the same (round, identity) twice (S2_forwardOnce)
	seen := map[itfRoundBlock]int{}
	perRound := map[int]int{}
	for _, p := range all {
		seen[itfRoundBlock{Round: p.Round, Block: p.Block}]++
		perRound[p.Round]++
	}
	for k, n := range seen {
		if n > 1 {
			d.failf("reactor forwarded (round %d, %s) %d times; delivered %+v", k.Round, k.Block, n, all)
		}
	}
	if d.mode == mbtRegression {
		for r, n := range perRound {
			if n > 1 && !d.replaced[r] {
				d.failf("reactor forwarded %d proposals for round %d; delivered %+v", n, r, all)
			}
		}
		return
	}
	if !d.strict {
		return
	}
	model := append(append([]itfProposal{}, d.modelProps...), mbtPendingProps(last)...)
	if !sameMultiset(model, all) {
		d.mismatch("proposals delivered to consensus (taken, then drained; as multisets)", model, all)
	}
}

// drain reads both channels until the reactor is idle with both empty.
func (d *mbtDriver) drain() []itfProposal {
	var out []itfProposal
	for i := 0; i < 1000; i++ {
		d.quiesce()
		progressed := false
		for {
			select {
			case p := <-d.r.proposalChan:
				out = append(out, d.goProposal(p))
				progressed = true
				continue
			case pi := <-d.r.partChan:
				d.checkPartData(pi)
				progressed = true
				continue
			default:
			}
			break
		}
		if !progressed && d.recvPending.Load() == 0 && d.liveDelivers() == 0 {
			return out
		}
	}
	d.failf("reactor still sending after draining\n%s", allStacks())
	return nil
}

func sameMultiset(a, b []itfProposal) bool {
	if len(a) != len(b) {
		return false
	}
	rest := append([]itfProposal{}, b...)
	for _, x := range a {
		i := slices.Index(rest, x)
		if i < 0 {
			return false
		}
		rest = slices.Delete(rest, i, i+1)
	}
	return true
}

// ----------------------------------------------------------- quiescence

type mbtGoroutine struct {
	id, state, top, createdBy, creator, text string
}

func allStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

func parseGoroutines(dump string) []mbtGoroutine {
	var out []mbtGoroutine
	for _, block := range strings.Split(dump, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "goroutine ") {
			continue
		}
		g := mbtGoroutine{text: block}
		hdr := strings.TrimSuffix(strings.TrimPrefix(lines[0], "goroutine "), ":")
		if i := strings.Index(hdr, " ["); i >= 0 {
			g.id = hdr[:i]
			g.state = strings.TrimSuffix(hdr[i+2:], "]")
			if j := strings.Index(g.state, ","); j >= 0 {
				g.state = g.state[:j]
			}
		}
		for _, l := range lines[1:] {
			if strings.HasPrefix(l, "\t") {
				continue
			}
			if strings.HasPrefix(l, "created by ") {
				cb := strings.TrimPrefix(l, "created by ")
				if i := strings.Index(cb, " in goroutine "); i >= 0 {
					g.createdBy, g.creator = cb[:i], cb[i+len(" in goroutine "):]
				}
				continue
			}
			if g.top == "" && !strings.HasPrefix(l, "runtime.") {
				fn := l
				if strings.HasSuffix(fn, ")") {
					fn = fn[:strings.LastIndex(fn, "(")]
				}
				g.top = fn
			}
		}
		out = append(out, g)
	}
	return out
}

// mbtParkSites are where a reactor goroutine (or the RECV goroutine) may wait
// once it has no work: requestFromPeer's loop, the channel sends to
// consensus, and RECV waiting for the next message.
var mbtParkSites = []string{
	"propagation.(*Reactor).requestFromPeer",
	"propagation.(*Reactor).processValidatedCompactBlock",
	"propagation.(*Reactor).handleRecoveryPart",
	"propagation.(*Reactor).publishLocalParts",
	"propagation.(*Reactor).deliverCachedCompactBlock",
	"propagation.(*mbtDriver).recvLoop",
}

// idleSnapshot returns a fingerprint of the parked goroutines, and false if a
// goroutine running reactor code is not parked.
func (d *mbtDriver) idleSnapshot() (string, bool) {
	self := curGID()
	var parked []string
	for _, g := range parseGoroutines(allStacks()) {
		if g.id == self {
			continue
		}
		if !strings.Contains(g.text, "propagation.(*Reactor).") && !strings.Contains(g.text, "propagation.(*mbtDriver).") {
			continue
		}
		waiting := g.state == "select" || g.state == "chan send" || g.state == "chan receive"
		site := false
		for _, s := range mbtParkSites {
			if strings.HasSuffix(g.top, s) {
				site = true
			}
		}
		if !waiting || !site {
			return "", false
		}
		parked = append(parked, g.id+":"+g.top)
	}
	sort.Strings(parked)
	return strings.Join(parked, ","), true
}

// quiesce waits until every reactor goroutine is idle or parked on a send,
// twice in a row with the same goroutines.
func (d *mbtDriver) quiesce() {
	d.t.Helper()
	deadline := time.Now().Add(mbtQuiesceTimeout)
	prev, sleep := "", 200*time.Microsecond
	for time.Now().Before(deadline) {
		snap, ok := d.idleSnapshot()
		if ok && snap == prev {
			return
		}
		if ok {
			prev = snap
		} else {
			prev = ""
		}
		time.Sleep(sleep)
		sleep = min(2*sleep, 5*time.Millisecond)
	}
	d.failf("reactor goroutines did not go idle within %v\n%s", mbtQuiesceTimeout, allStacks())
}

// liveDelivers counts the deliverCachedCompactBlock goroutines this driver
// started.
func (d *mbtDriver) liveDelivers() int {
	n := 0
	for _, g := range parseGoroutines(allStacks()) {
		if strings.HasSuffix(g.createdBy, "propagation.(*Reactor).handleCachedCompactBlock") && d.isOwnGID(g.creator) {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------------ tests

func replayTraceFile(t *testing.T, m *mbtFixtures, path string, mode mbtMode) mbtStats {
	tr, err := readITFTrace(path)
	require.NoError(t, err)
	for i, s := range tr.States {
		require.LessOrEqual(t, len(s.PropChan), 1, "state %d: the replay supports channel capacity 1 only", i)
		require.LessOrEqual(t, len(s.PartChan), 1, "state %d: the replay supports channel capacity 1 only", i)
	}
	d := newMBTDriver(t, m, tr, mode)
	d.replay()
	st := d.stats
	if mode == mbtConformance {
		t.Logf("%s: %d steps (%d env), %d compared strictly (%d settled), %d takes, %d reorders, %d parts skipped, %d requests recorded, diverged at %d %s",
			tr.Name, st.steps, st.envSteps, st.strictSteps, st.settledChecks, st.takes, st.reorders, st.skippedParts, st.requestedParts, st.divergedAt, st.divergeReason)
	}
	return st
}

func globTraces(t *testing.T, dir string, patterns ...string) []string {
	files := make([]string, 0, len(patterns))
	for _, p := range patterns {
		fs, err := filepath.Glob(filepath.Join(dir, p))
		require.NoError(t, err)
		files = append(files, fs...)
	}
	sort.Strings(files)
	return files
}

// TestMBTReplayFixedTraces replays every fixed_* trace (ab6a3df applied, the
// code on main) against the reactor and compares after each step. Every
// committed trace must replay without schedule divergence, so that it is
// compared to its last step.
//
// Besides the traces `make traces` writes, traces/ holds fixed_witness_*
// traces of rare scenarios the random runs did not reach. Each is the
// shortest run quint found that reaches a goal, written as a violation of
// its negation, for example (from spec/consensus/propagation):
//
//	quint run instances.qnt --main fixedTraced --seed 0x7 --max-samples 50000 --max-steps 40 \
//	  --invariant 'not(lastAction.kind == "consApply" and not(lastAction.accepted))' \
//	  --out-itf traces/fixed_witness_cachedConflict.itf.json
//
// The goals: cachedConflict (above); secondForward
// 'not(hist.fwd.filter(x => x._1 == 0).size() >= 2)'; decodeByParity
// 'not(lastAction.kind == "part" and lastAction.part == "parity" and
// lastAction.accepted and recv.pc == "sendParts")'; corruptOnStub
// 'not(lastAction.kind == "part" and lastAction.corrupt and lastAction.proof
// and prop.entries.get(lastAction.round).kind == "stub")', all with seed 0x7;
// xViaStub 'not(lastAction.kind == "recvSendProp" and lastAction.block ==
// "X")' with seed 0x3. cachedReplaced (the picked peer replaces its cached
// compact block before consApply) is trace 742 of an unguided run:
//
//	quint run instances.qnt --main fixedTraced --seed 0x31 --max-samples 1000 \
//	  --n-traces 1000 --max-steps 50 --out-itf "$TMP/t_{seq}.itf.json" &&
//	  cp "$TMP/t_742.itf.json" traces/fixed_witness_cachedReplaced.itf.json
//
// `make traces` deletes the directory first, so rerun these after it.
func TestMBTReplayFixedTraces(t *testing.T) {
	m := loadMBTFixtures(t)
	files := globTraces(t, mbtTraceDir, "fixed_*.itf.json")
	require.NotEmpty(t, files)
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".itf.json"), func(t *testing.T) {
			st := replayTraceFile(t, m, path, mbtConformance)
			require.Zero(t, st.divergedAt, "committed trace must replay without schedule divergence: %s", st.divergeReason)
		})
	}
}

// TestMBTRevertedTraces replays the environment steps of the traces of the
// model with ab6a3df reverted (fully or one half) against the fixed reactor.
// This is a regression check, not conformance: those traces end in states
// that violate S1 (a wait-for cycle) or S2 (a duplicate forward), and the
// fixed reactor must not reproduce them. It must forward at most one proposal
// per (height, round), return from every consensus call in bounded time, and
// hand every proposal and part to consensus once the consumer drains.
func TestMBTRevertedTraces(t *testing.T) {
	m := loadMBTFixtures(t)
	files := globTraces(t, mbtTraceDir, "prefix_*.itf.json", "forwardOnceOnly_*.itf.json", "asyncOnly_*.itf.json")
	require.NotEmpty(t, files)
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".itf.json"), func(t *testing.T) {
			replayTraceFile(t, m, path, mbtRegression)
		})
	}
}

// TestMBTReplayTraceDir replays every ITF trace in $MBT_TRACE_DIR as
// TestMBTReplayFixedTraces does. Generate the traces with, for example:
//
//	quint run spec/consensus/propagation/instances.qnt --main fixedTraced \
//	  --max-samples 200 --n-traces 200 --max-steps 50 \
//	  --out-itf "$MBT_TRACE_DIR/t_{seq}.itf.json"
//
// then run
//
//	MBT_TRACE_DIR=... go test -run TestMBTReplayTraceDir -v ./consensus/propagation
func TestMBTReplayTraceDir(t *testing.T) {
	dir := os.Getenv("MBT_TRACE_DIR")
	if dir == "" {
		t.Skip("MBT_TRACE_DIR is not set")
	}
	m := loadMBTFixtures(t)
	files := globTraces(t, dir, "*.itf.json")
	require.NotEmpty(t, files, "no *.itf.json in %s", dir)
	var total mbtStats
	diverged := 0
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".itf.json"), func(t *testing.T) {
			st := replayTraceFile(t, m, path, mbtConformance)
			total.steps += st.steps
			total.strictSteps += st.strictSteps
			total.settledChecks += st.settledChecks
			if st.divergedAt != 0 {
				diverged++
			}
		})
	}
	t.Logf("%d traces, %d steps, %d compared strictly (%d settled), %d traces diverged from Go's schedule",
		len(files), total.steps, total.strictSteps, total.settledChecks, diverged)
}
