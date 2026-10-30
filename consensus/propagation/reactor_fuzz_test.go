package propagation

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	cmtcfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

// Heights used by the reactor fuzzer. Height fuzzStoredHeight is committed in
// the block store, the fixture proposals live at the two heights above it.
const (
	fuzzStoredHeight = int64(4)
	fuzzMaxHeight    = int64(7)
	fuzzMaxRound     = int32(3)
	fuzzMaxOps       = 48
)

type fuzzBlock struct {
	cb       *proptypes.CompactBlock // signed by the proposer; only sent as wire bytes
	original *types.PartSet
	parity   *types.PartSet
	txs      types.Txs
	keys     []types.TxKey // keys of txs, hashed once rather than per iteration
}

// part returns the bytes and proof of combined index i.
func (b *fuzzBlock) part(i uint32) (*types.Part, bool) {
	if i < b.original.Total() {
		return b.original.GetPart(int(i)), true
	}
	if i < 2*b.original.Total() {
		return b.parity.GetPart(int(i - b.original.Total())), true
	}
	return nil, false
}

type fuzzFixtures struct {
	pv       types.MockPV
	pub      crypto.PubKey
	otherPub crypto.PubKey
	store    *store.BlockStore
	stored   *types.PartSet
	blocks   []*fuzzBlock
}

var (
	fuzzFixturesOnce sync.Once
	fuzzFixturesVal  *fuzzFixtures
)

// deterministicTxs returns txs of the given sizes from a fixed seed so fuzz
// inputs replay the same blocks in every process.
func deterministicTxs(seed int64, sizes ...int) types.Txs {
	r := rand.New(rand.NewSource(seed)) //nolint:gosec
	txs := make(types.Txs, len(sizes))
	for i, size := range sizes {
		txs[i] = make([]byte, size)
		_, _ = r.Read(txs[i])
	}
	return txs
}

func loadFuzzFixtures(tb testing.TB) *fuzzFixtures {
	fuzzFixturesOnce.Do(func() {
		pv := types.NewMockPVWithParams(ed25519.GenPrivKeyFromSecret([]byte("propagation-fuzz-proposer")), false, false)
		fx := &fuzzFixtures{
			pv:       pv,
			pub:      pv.PrivKey.PubKey(),
			otherPub: ed25519.GenPrivKeyFromSecret([]byte("propagation-fuzz-other")).PubKey(),
			store:    store.NewBlockStore(dbm.NewMemDB()),
		}
		ts := time.Unix(1_700_000_000, 0).UTC()

		stored := types.MakeBlock(fuzzStoredHeight, types.Data{Txs: deterministicTxs(4, 80_000)}, &types.Commit{Height: fuzzStoredHeight - 1}, nil)
		stored.ProposerAddress = fx.pub.Address()
		storedParts, err := stored.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(tb, err)
		fx.store.SaveBlock(stored, storedParts, &types.Commit{Height: fuzzStoredHeight})
		fx.stored = storedParts

		makeBlock := func(height int64, round int32, txs types.Txs) *fuzzBlock {
			block := types.MakeBlock(height, types.Data{Txs: txs}, nil, nil)
			ps, err := block.MakePartSet(types.BlockPartSizeBytes)
			require.NoError(tb, err)
			parity, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
			require.NoError(tb, err)
			blobs := make([]proptypes.TxMetaData, len(ps.TxPos))
			for i, pos := range ps.TxPos {
				blobs[i] = proptypes.TxMetaData{Start: pos.Start, End: pos.End, Hash: block.Txs[i].Hash()}
			}
			cb := &proptypes.CompactBlock{
				Proposal: types.Proposal{
					Type: cmtproto.ProposalType, Height: height, Round: round, POLRound: -1, Timestamp: ts,
					BlockID: types.BlockID{Hash: tmhash.Sum(ps.Hash()), PartSetHeader: ps.Header()},
				},
				LastLen:     uint32(lastLen),
				BpHash:      parity.Hash(),
				Blobs:       blobs,
				PartsHashes: extractHashes(ps, parity),
			}
			fx.sign(tb, cb, true)
			keys := make([]types.TxKey, len(txs))
			for i, tx := range txs {
				keys[i] = tx.Key()
			}
			return &fuzzBlock{cb: cb, original: ps, parity: parity, txs: txs, keys: keys}
		}
		a := deterministicTxs(5, 30_000, 50_000, 70_000, 40_000, 60_000)
		fx.blocks = []*fuzzBlock{
			makeBlock(5, 0, a), // four parts
			makeBlock(5, 0, deterministicTxs(6, 20_000, 90_000)), // conflicting proposal, two parts
			makeBlock(5, 1, a),
			makeBlock(6, 0, deterministicTxs(7, 100, 200, 300)), // one part
		}
		fuzzFixturesVal = fx
	})
	return fuzzFixturesVal
}

// sign signs the compact block (and optionally its proposal) with the
// proposer key, as a byzantine proposer could for arbitrary contents.
func (fx *fuzzFixtures) sign(tb testing.TB, cb *proptypes.CompactBlock, proposal bool) {
	if proposal {
		sig, err := fx.pv.PrivKey.Sign(types.ProposalSignBytes(TestChainID, cb.Proposal.ToProto()))
		require.NoError(tb, err)
		cb.Proposal.Signature = sig
	}
	sbz, err := cb.SignBytes()
	require.NoError(tb, err)
	cb.Signature, err = fx.pv.SignRawBytes(TestChainID, CompactBlockUID, sbz)
	require.NoError(tb, err)
}

// fuzzPeer checks every recovery part the reactor sends at send time, so
// parts relayed by goroutines that outlive the iteration's steps (clearWants)
// are checked too.
type fuzzPeer struct {
	*mock.Peer
	check func(*propproto.RecoveryPart)
}

func (p *fuzzPeer) TrySend(e p2p.Envelope) bool { return p.Send(e) }

func (p *fuzzPeer) Send(e p2p.Envelope) bool {
	if rp, ok := e.Message.(*propproto.RecoveryPart); ok {
		p.check(rp)
	}
	return true
}

type fuzzPeerStateEditor struct {
	mtx    sync.Mutex
	height int64
}

func (*fuzzPeerStateEditor) SetHasProposal(*types.Proposal)            {}
func (*fuzzPeerStateEditor) SetHasProposalBlockPart(int64, int32, int) {}
func (e *fuzzPeerStateEditor) GetHeight() int64 {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	return e.height
}

// panicWatchLogger records peers disconnected by BaseReactor.ProtectPanic, so
// a recovered panic in a peer routine still fails the fuzz target.
type panicWatchLogger struct {
	log.Logger
	mtx    *sync.Mutex
	panics *[]string
}

func (l panicWatchLogger) Error(msg string, keyvals ...interface{}) {
	for _, kv := range keyvals {
		if s, ok := kv.(string); ok && strings.HasPrefix(s, "panic in reactor") {
			l.mtx.Lock()
			*l.panics = append(*l.panics, s)
			l.mtx.Unlock()
		}
	}
}

func (l panicWatchLogger) With(...interface{}) log.Logger { return l }

type fuzzReader struct{ data []byte }

func (r *fuzzReader) byte() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[0]
	r.data = r.data[1:]
	return b
}

func (r *fuzzReader) u32() uint32 {
	return uint32(r.byte()) | uint32(r.byte())<<8 | uint32(r.byte())<<16 | uint32(r.byte())<<24
}

func (r *fuzzReader) bytes(n int) []byte {
	n = min(n, len(r.data))
	b := r.data[:n]
	r.data = r.data[n:]
	return b
}

func (r *fuzzReader) hash() []byte {
	h := make([]byte, 32)
	copy(h, r.bytes(32))
	return h
}

// signedProposal is a part set header the reactor may legitimately hold at a
// height: one the proposer signed (a fixture or a re-signed byzantine compact
// block) or one consensus reported as committed.
type signedProposal struct {
	height int64
	psh    types.PartSetHeader
	blk    *fuzzBlock // fixture with that part set, nil if none
}

type reactorFuzzer struct {
	t      *testing.T
	fx     *fuzzFixtures
	r      *Reactor
	peers  []*fuzzPeer
	editor []*fuzzPeerStateEditor
	mtx    sync.Mutex
	panics []string
	// signed and badSends are guarded by mtx; badSends is written by reactor
	// goroutines when they send a part that no signed proposal commits to.
	signed   []signedProposal
	badSends []string
}

func newReactorFuzzer(t *testing.T, fx *fuzzFixtures, rd *fuzzReader) *reactorFuzzer {
	f := &reactorFuzzer{t: t, fx: fx}
	for _, b := range fx.blocks {
		f.addSigned(b.cb.Proposal.Height, b.original.Header())
	}
	// Registered first so it runs last, after the reactor has stopped.
	t.Cleanup(func() { require.Empty(t, f.badSendsCopy(), "reactor sent unverified data") })
	p2pCfg := cmtcfg.DefaultP2PConfig()
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKeyFromSecret([]byte("propagation-fuzz-node"))}
	transport := p2p.NewMultiplexTransport(p2p.DefaultNodeInfo{}, nodeKey, p2p.MConnConfig(p2pCfg), trace.NoOpTracer())
	sw := p2p.NewSwitch(p2pCfg, transport)
	sw.SetLogger(panicWatchLogger{Logger: log.NewNopLogger(), mtx: &f.mtx, panics: &f.panics})

	mp := &mockMempool{txs: make(map[types.TxKey]*types.CachedTx)}
	mask := rd.u32()
	n := 0
	for _, b := range fx.blocks {
		for i, tx := range b.txs {
			if mask&(1<<(n%32)) != 0 {
				mp.txs[b.keys[i]] = &types.CachedTx{Tx: tx}
			}
			n++
		}
	}

	r := NewReactor(nodeKey.ID(), Config{
		Store: fx.store, Mempool: mp, Privval: fx.pv, ChainID: TestChainID, BlockMaxBytes: int64(types.MaxBlockSizeBytes),
	})
	r.ticker.Stop() // catchup retries are driven explicitly by the fuzzer
	r.SetLogger(log.NewNopLogger())
	sw.AddReactor("BlockProp", r)
	require.NoError(t, r.Start())
	r.started.Store(true)
	f.r = r

	// Stand in for the consensus reactor, which always drains these channels.
	// PROTOCO-3430 covers the case where consensus stops draining proposalChan.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-r.proposalChan:
			case <-r.partChan:
			}
		}
	}()
	t.Cleanup(func() {
		_ = r.Stop()
		// Reactor.OnStop does not cancel the base reactor's processor
		// goroutine, which keeps the whole reactor reachable; without this
		// every iteration leaks a goroutine and several MB of heap.
		r.BaseReactor.OnStop()
		<-drained
	})

	for i, n := 0, 1+int(rd.byte()%3); i < n; i++ {
		peer := &fuzzPeer{Peer: mock.NewPeer(nil), check: f.checkSent}
		editor := &fuzzPeerStateEditor{height: fuzzStoredHeight + int64(rd.byte()%4)}
		peer.Set(types.PeerStateKey, editor)
		r.AddPeer(peer)
		f.peers = append(f.peers, peer)
		f.editor = append(f.editor, editor)
	}
	r.SetConsensusState(5, 0, fx.pub)
	return f
}

// send delivers msg from peer through the wire decoding path and checks that
// a message failing decoding or ValidateBasic disconnects the peer.
func (f *reactorFuzzer) send(peer *fuzzPeer, ch byte, msg *propproto.Message) {
	if !peer.IsRunning() {
		return // the switch never delivers messages from a stopped peer
	}
	raw, err := proto.Marshal(msg)
	require.NoError(f.t, err)
	f.sendRaw(peer, ch, raw)
}

func (f *reactorFuzzer) sendRaw(peer *fuzzPeer, ch byte, raw []byte) {
	if !peer.IsRunning() {
		return
	}
	var pb propproto.Message
	if proto.Unmarshal(raw, &pb) != nil {
		return
	}
	decoded, decErr := proptypes.MsgFromProto(&pb)
	if decErr == nil {
		decErr = decoded.ValidateBasic()
	}
	if w, ok := decoded.(*proptypes.WantParts); ok && decErr == nil && (w.Round < 0 || w.Round > fuzzMaxRound) {
		// Excluded pending PROTOCO-3516; remove with the fix.
		return
	}
	f.r.Receive(p2p.Envelope{Src: peer, ChannelID: ch, Message: &pb})
	if decErr != nil {
		require.Nil(f.t, f.r.getPeer(peer.ID()), "peer sending an invalid message must be removed: %v", decErr)
		require.False(f.t, peer.IsRunning(), "peer sending an invalid message must be stopped")
	}
}

func (f *reactorFuzzer) height() (int64, int32) {
	f.r.pmtx.Lock()
	defer f.r.pmtx.Unlock()
	return f.r.height, f.r.round
}

func (f *reactorFuzzer) step(rd *fuzzReader) {
	fx := f.fx
	peer := f.peers[int(rd.byte())%len(f.peers)]
	blk := fx.blocks[int(rd.byte())%len(fx.blocks)]
	total := blk.original.Total()
	data := func(m propproto.Message) *propproto.Message { return &m }

	switch op := rd.byte() % 13; op {
	case 0: // valid compact block from the proposer
		f.send(peer, DataChannel, data(propproto.Message{Sum: &propproto.Message_CompactBlock{CompactBlock: blk.cb.ToProto()}}))
	case 1, 2: // compact block with fuzzed fields; op 1 is re-signed by a byzantine proposer
		cb := cloneCompactBlock(f.t, blk.cb)
		switch rd.byte() % 7 {
		case 0: // attacker-chosen tx metadata offsets
			cb.Blobs = cb.Blobs[:int(rd.byte())%(len(cb.Blobs)+1)]
			limit := total*types.BlockPartSizeBytes + 64
			for i := range cb.Blobs {
				cb.Blobs[i].Start = rd.u32() % limit
				cb.Blobs[i].End = rd.u32() % limit
				if rd.byte()%4 == 0 {
					cb.Blobs[i].Hash = fx.blocks[int(rd.byte())%len(fx.blocks)].txs[0].Hash()
				}
			}
		case 1:
			cb.LastLen = rd.u32() % (types.BlockPartSizeBytes + 2)
		case 2:
			i, j := int(rd.byte())%len(cb.PartsHashes), int(rd.byte())%len(cb.PartsHashes)
			cb.PartsHashes[i], cb.PartsHashes[j] = cb.PartsHashes[j], cb.PartsHashes[i]
			if rd.byte()%2 == 0 {
				cb.PartsHashes[i] = rd.hash()
			}
		case 3:
			cb.BpHash = rd.hash()
		case 4:
			cb.Proposal.BlockID.Hash = rd.hash()
			cb.Proposal.POLRound = int32(int8(rd.byte()))
		case 5:
			cb.Proposal.Height = fuzzStoredHeight + int64(rd.byte()%5)
			cb.Proposal.Round = int32(rd.byte() % 4)
		case 6:
			cb.Proposal.BlockID.PartSetHeader = fx.blocks[int(rd.byte())%len(fx.blocks)].original.Header()
		}
		if op == 1 {
			fx.sign(f.t, cb, true)
			f.addSigned(cb.Proposal.Height, cb.Proposal.BlockID.PartSetHeader)
		} else if rd.byte()%2 == 0 {
			cb.Signature = append([]byte{}, rd.bytes(64)...)
		}
		f.send(peer, DataChannel, data(propproto.Message{Sum: &propproto.Message_CompactBlock{CompactBlock: cb.ToProto()}}))
	case 3: // haves
		h, r := f.target(rd, blk)
		have := &propproto.HaveParts{Height: h, Round: r}
		corrupt := rd.byte()%8 == 0 // one entry with a fuzzed index and hash
		for i, n := 0, 1+int(rd.byte()%8); i < n; i++ {
			idx := uint32(rd.byte()) % (2 * total)
			p, _ := blk.part(idx)
			hash := p.Proof.LeafHash
			if corrupt && i == 0 {
				idx = uint32(rd.byte()) % (2*total + 2)
				hash = rd.hash()
			}
			have.Parts = append(have.Parts, &propproto.PartMetaData{Index: idx, Hash: hash})
		}
		f.send(peer, DataChannel, data(propproto.Message{Sum: &propproto.Message_HaveParts{HaveParts: have}}))
	case 4: // wants
		h, r := f.target(rd, blk)
		size := int(2 * total)
		if rd.byte()%4 == 0 {
			size = int(rd.byte()) % (size + 3)
		}
		ba := bits.NewBitArray(size)
		mask := rd.u32()
		for i := 0; i < size; i++ {
			ba.SetIndex(i, mask&(1<<(i%32)) != 0)
		}
		want := &propproto.WantParts{Height: h, Round: r, Prove: rd.byte()%2 == 0, MissingPartsCount: 1 + int32(rd.byte())%int32(max(size, 1))}
		if ba != nil {
			want.Parts = *ba.ToProto()
		}
		ch := WantChannel
		if rd.byte()%8 == 0 {
			ch = DataChannel
		}
		f.send(peer, ch, data(propproto.Message{Sum: &propproto.Message_WantParts{WantParts: want}}))
	case 5: // single recovery part with fuzzed index, data and proof
		h, r := f.target(rd, blk)
		idx := uint32(rd.byte()) % (2*total + 1)
		rp := &propproto.RecoveryPart{Height: h, Round: r, Index: idx, Data: []byte{rd.byte()}}
		if p, ok := blk.part(idx); ok {
			rp.Data = append([]byte{}, p.Bytes...)
			switch rd.byte() % 4 {
			case 1:
				rp.Data[int(rd.u32())%len(rp.Data)] ^= 1 | rd.byte()
			case 2:
				rp.Data = rp.Data[:int(rd.u32())%len(rp.Data)+1]
			}
			switch rd.byte() % 4 {
			case 1:
				rp.Proof = *p.Proof.ToProto()
			case 2:
				other, _ := blk.part(uint32(rd.byte()) % (2 * total))
				rp.Proof = *other.Proof.ToProto()
			case 3:
				rp.Proof = *p.Proof.ToProto()
				rp.Proof.Index = int64(int8(rd.byte()))
				rp.Proof.Total = int64(int8(rd.byte()))
			}
		}
		f.send(peer, DataChannel, data(propproto.Message{Sum: &propproto.Message_RecoveryPart{RecoveryPart: rp}}))
	case 6: // genuine recovery parts for a subset of indexes, drives decoding
		mask, prove := rd.u32(), rd.byte()%2 == 0
		for idx := uint32(0); idx < 2*total; idx++ {
			if mask&(1<<idx) == 0 {
				continue
			}
			p, _ := blk.part(idx)
			rp := &propproto.RecoveryPart{Height: blk.cb.Proposal.Height, Round: blk.cb.Proposal.Round, Index: idx, Data: p.Bytes}
			if prove {
				rp.Proof = *p.Proof.ToProto()
			}
			f.send(peer, DataChannel, data(propproto.Message{Sum: &propproto.Message_RecoveryPart{RecoveryPart: rp}}))
		}
	case 7: // raw wire bytes
		ch := DataChannel
		if rd.byte()%2 == 0 {
			ch = WantChannel
		}
		f.sendRaw(peer, ch, rd.bytes(int(rd.byte())))
	case 8: // consensus moves to a new round, possibly with a different proposer
		h, r := f.height()
		pub := fx.pub
		if rd.byte()%8 == 0 {
			pub = fx.otherPub
		}
		f.r.SetConsensusState(h, min(r+int32(rd.byte()%2), fuzzMaxRound), pub)
	case 9: // consensus commits the current height and moves to the next one
		h, _ := f.height()
		if h < fuzzMaxHeight {
			f.r.Prune(h)
			f.r.SetConsensusState(h+1, 0, fx.pub)
		}
	case 10: // consensus learns a committed part set header it lacks
		h, _ := f.height()
		if blk.cb.Proposal.Height >= h {
			psh := blk.original.Header()
			f.addSigned(blk.cb.Proposal.Height, psh)
			f.r.AddCommitment(blk.cb.Proposal.Height, blk.cb.Proposal.Round+int32(rd.byte()%2), &psh)
		}
	case 11:
		f.r.retryWants()
	case 12:
		e := f.editor[int(rd.byte())%len(f.editor)]
		e.mtx.Lock()
		e.height = fuzzStoredHeight + int64(rd.byte()%4)
		e.mtx.Unlock()
	}
	f.r.ticker.Stop()
}

// target returns the height and round a have, want or part refers to: mostly
// the fixture's own, sometimes nearby or arbitrary values.
func (f *reactorFuzzer) target(rd *fuzzReader, blk *fuzzBlock) (int64, int32) {
	h, r := blk.cb.Proposal.Height, blk.cb.Proposal.Round
	switch rd.byte() % 8 {
	case 0:
		h = fuzzStoredHeight + int64(rd.byte()%4)
	case 1:
		r = int32(rd.byte() % 4)
	case 2:
		h, r = int64(int8(rd.byte())), int32(int8(rd.byte()))
	}
	return h, r
}

func cloneCompactBlock(t *testing.T, cb *proptypes.CompactBlock) *proptypes.CompactBlock {
	raw, err := proto.Marshal(cb.ToProto())
	require.NoError(t, err)
	var pb propproto.CompactBlock
	require.NoError(t, proto.Unmarshal(raw, &pb))
	out, err := proptypes.CompactBlockFromProto(&pb)
	require.NoError(t, err)
	return out
}

// fixtureFor returns the fixture whose original part set header is psh.
func (f *reactorFuzzer) fixtureFor(psh types.PartSetHeader) *fuzzBlock {
	for _, b := range f.fx.blocks {
		if b.original.Header().Equals(psh) {
			return b
		}
	}
	return nil
}

// addSigned records that the reactor may hold part set psh at height.
func (f *reactorFuzzer) addSigned(height int64, psh types.PartSetHeader) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	for _, sp := range f.signed {
		if sp.height == height && sp.psh.Equals(psh) {
			return
		}
	}
	f.signed = append(f.signed, signedProposal{height: height, psh: psh, blk: f.fixtureFor(psh)})
}

// signedAt returns the fixture holding part set psh if a signed proposal or
// commitment for psh exists at height.
func (f *reactorFuzzer) signedAt(height int64, psh types.PartSetHeader) (*fuzzBlock, bool) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	for _, sp := range f.signed {
		if sp.height == height && sp.psh.Equals(psh) {
			return sp.blk, true
		}
	}
	return nil, false
}

// checkSent runs on reactor goroutines for every recovery part sent to a peer.
func (f *reactorFuzzer) checkSent(rp *propproto.RecoveryPart) {
	if f.knownPart(rp) {
		return
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if len(f.badSends) < 16 {
		f.badSends = append(f.badSends, fmt.Sprintf("part %d of %d/%d", rp.Index, rp.Height, rp.Round))
	}
}

func (f *reactorFuzzer) checkInvariants() {
	t := f.t
	// Assertions never run while holding a lock: a failing one would leave it
	// held and deadlock the cleanup.
	f.mtx.Lock()
	panics := append([]string(nil), f.panics...)
	f.mtx.Unlock()
	require.Empty(t, panics, "reactor recovered a panic")

	f.r.pmtx.Lock()
	type entry struct {
		height int64
		round  int32
		data   *proposalData
	}
	var entries []entry
	for h, rounds := range f.r.proposals {
		for r, pd := range rounds {
			entries = append(entries, entry{h, r, pd})
		}
	}
	f.r.pmtx.Unlock()

	for _, e := range entries {
		orig := e.data.block.Original()
		blk, ok := f.signedAt(e.height, orig.Header())
		require.True(t, ok, "proposal %d/%d has a part set header no proposer signed at that height", e.height, e.round)
		require.NotNil(t, blk, "proposal %d/%d has parts but its part set header matches no fixture", e.height, e.round)
		for i := 0; i < int(orig.Total()); i++ {
			if p := orig.GetPart(i); p != nil {
				require.Equal(t, blk.original.GetPart(i).Bytes, p.Bytes, "original part %d of %d/%d differs from the signed block", i, e.height, e.round)
			}
		}
		if parity := e.data.block.Parity(); parity.Total() > 0 && bytes.Equal(parity.Hash(), blk.parity.Hash()) {
			for i := 0; i < int(parity.Total()); i++ {
				if p := parity.GetPart(i); p != nil {
					require.Equal(t, blk.parity.GetPart(i).Bytes, p.Bytes, "parity part %d of %d/%d differs", i, e.height, e.round)
				}
			}
		}
		for _, i := range e.data.block.BitArray().GetTrueIndices() {
			_, ok := e.data.block.GetPart(uint32(i))
			require.True(t, ok, "part %d of %d/%d is marked present but missing", i, e.height, e.round)
		}
	}

	type key struct {
		height int64
		round  int32
	}
	for _, ps := range f.r.getPeers() {
		var stateKeys, requestKeys []key
		ps.mtx.RLock()
		unverified := len(ps.unverifiedProposals)
		for h, rounds := range ps.state {
			for r := range rounds {
				stateKeys = append(stateKeys, key{h, r})
			}
		}
		for h, rounds := range ps.remainingRequests {
			for r := range rounds {
				requestKeys = append(requestKeys, key{h, r})
			}
		}
		ps.mtx.RUnlock()
		inRange := func(k key) bool {
			return k.height >= fuzzStoredHeight && k.height <= fuzzMaxHeight && k.round >= 0 && k.round <= fuzzMaxRound+1
		}
		require.LessOrEqual(t, unverified, MaxUnverifiedProposals)
		for _, k := range stateKeys {
			require.True(t, inRange(k), "peer state grew an entry for %d/%d", k.height, k.round)
		}
		for _, k := range requestKeys {
			require.True(t, inRange(k), "peer request counter grew an entry for %d/%d", k.height, k.round)
		}
	}

	require.Empty(t, f.badSendsCopy(), "reactor sent unverified data")
}

func (f *reactorFuzzer) badSendsCopy() []string {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return append([]string(nil), f.badSends...)
}

// knownPart reports whether a sent part carries the data of a block signed by
// the proposer (or committed in the store) at that height.
func (f *reactorFuzzer) knownPart(rp *propproto.RecoveryPart) bool {
	if rp.Height == fuzzStoredHeight && rp.Index < f.fx.stored.Total() {
		return bytes.Equal(f.fx.stored.GetPart(int(rp.Index)).Bytes, rp.Data)
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	for _, sp := range f.signed {
		if sp.height != rp.Height || sp.blk == nil {
			continue
		}
		if p, ok := sp.blk.part(rp.Index); ok && bytes.Equal(p.Bytes, rp.Data) {
			return true
		}
	}
	return false
}

// FuzzPropagationReactor drives a live propagation reactor with sequences of
// peer messages (compact blocks, haves, wants, recovery parts) and consensus
// transitions, then checks that only data committed to by a signed proposal
// is stored or relayed and that per-peer state stays bounded.
func FuzzPropagationReactor(f *testing.F) {
	if fl := flag.Lookup("test.fuzzworker"); fl != nil && fl.Value.String() == "true" {
		// Each iteration hashes and erasure-codes 64 KiB parts on several
		// goroutines. Uncapped, two workers saturate every CPU of a CI runner
		// and starve the fuzzing coordinator process. When -fuzztime expires,
		// the coordinator compares its deadline error against a child context
		// that context.cancel has not reached yet, and a starved coordinator
		// sometimes reports that as a bare "context deadline exceeded" failure.
		// One P per worker leaves CPU for the coordinator.
		runtime.GOMAXPROCS(1)
	}
	fx := loadFuzzFixtures(f)
	// Each step is (peer, fixture, op, op-specific bytes); see step.
	seed := func(steps ...[]byte) []byte {
		n := 7
		for _, s := range steps {
			n += len(s)
		}
		in := make([]byte, 0, n)
		in = append(in, 0xff, 0xff, 0xff, 0xff, 1, 1, 2) // all txs in the mempool, two peers
		for _, s := range steps {
			in = append(in, s...)
		}
		return in
	}
	f.Add(seed( // compact block, haves, wants, then parity parts that decode the block
		[]byte{0, 0, 0},
		[]byte{1, 0, 3, 7, 1, 7, 0, 1, 2, 3, 4, 5, 6, 7},
		[]byte{1, 0, 4, 7, 1, 0x0f, 0, 0, 0, 1, 3, 1},
		[]byte{1, 0, 6, 0xf0, 0, 0, 0, 1},
		[]byte{0, 0, 11},
	))
	f.Add(seed( // catchup: commitment for the next height, proven parts, stored-block wants
		[]byte{0, 3, 10, 0},
		[]byte{0, 3, 11},
		[]byte{1, 3, 6, 0x01, 0, 0, 0, 0},
		[]byte{0, 3, 4, 0, 0, 1, 3, 0, 0, 0, 0, 0, 1},
		[]byte{0, 0, 9},
	))
	f.Add(seed( // byzantine proposer metadata, then conflicting and next-round proposals
		[]byte{0, 0, 1, 0, 5, 1, 0, 0, 0, 0xff, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 2, 0, 0, 1, 0, 0, 0, 3, 0, 0, 0, 1, 0, 0, 0, 4, 0, 0, 0, 1},
		[]byte{1, 1, 0},
		[]byte{0, 0, 8, 0, 1},
		[]byte{1, 2, 0},
		[]byte{0, 2, 6, 0xff, 0, 0, 0, 1},
	))
	f.Add(seed( // unsigned compact block cached for a future height, then applied
		[]byte{1, 3, 0},
		[]byte{0, 0, 9},
		[]byte{0, 3, 6, 0x03, 0, 0, 0, 1},
	))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1<<14 {
			return
		}
		watchdog := time.AfterFunc(30*time.Second, func() {
			buf := make([]byte, 1<<22)
			buf = buf[:runtime.Stack(buf, true)]
			panic(fmt.Sprintf("propagation reactor fuzz iteration deadlocked:\n%s", buf))
		})
		defer watchdog.Stop()

		runReactorFuzz(t, fx, input)
	})
}

func runReactorFuzz(t *testing.T, fx *fuzzFixtures, input []byte) *reactorFuzzer {
	rd := &fuzzReader{data: input}
	fz := newReactorFuzzer(t, fx, rd)
	for i := 0; i < fuzzMaxOps && len(rd.data) > 0; i++ {
		fz.step(rd)
	}
	fz.checkInvariants()
	return fz
}

// TestFuzzPropagationReactorResignedHeight replays a sequence where a
// byzantine proposer re-signs the height-6 fixture's part set for the current
// height 5. The reactor rightly accepts it, so the harness must check stored
// proposals against what was signed rather than the fixture's original height.
func TestFuzzPropagationReactorResignedHeight(t *testing.T) {
	fx := loadFuzzFixtures(t)
	fz := runReactorFuzz(t, fx, []byte{
		0, 0, 0, 0, 2, 0, 0, 0, // empty mempool, three peers at the stored height
		0, 3, 1, 5, 1, 0, // peer 0, fixture 3, re-signed compact block moved to height 5 round 0
	})
	fz.r.pmtx.Lock()
	pd := fz.r.proposals[5][0]
	fz.r.pmtx.Unlock()
	require.NotNil(t, pd, "re-signed compact block was not accepted")
	require.True(t, pd.block.Original().Header().Equals(fx.blocks[3].original.Header()))
}
