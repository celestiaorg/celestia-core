package blocksync

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	mpmocks "github.com/cometbft/cometbft/mempool/mocks"
	"github.com/cometbft/cometbft/p2p"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	fzChainHeight = 7 // canonical blocks 1..7; a receiver can sync up to 6
	fzExtHeight   = 4 // vote extensions are enabled from this height
	fzNumPeers    = 3
	fzMaxReqs     = 4 // requesters the harness keeps open, like defaultMaxRequesters
	fzMaxOps      = 48

	// Excluded pending FINDING-blocksync-2; remove with the fix.
	fzCheckExtSigs = false
)

var fzGenesisTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// fzChain is a deterministic canonical chain signed by four validators.
type fzChain struct {
	genDoc     *cmttypes.GenesisDoc
	blocks     map[int64]*cmttypes.Block
	extCommits map[int64]*cmttypes.ExtendedCommit
}

var (
	fzChainOnce sync.Once
	fzChainVal  *fzChain
)

func newFzBlockExec(tb testing.TB, genDoc *cmttypes.GenesisDoc) (sm.State, *sm.BlockExecutor, *store.BlockStore, proxy.AppConns) {
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(abci.NewBaseApplication()), proxy.NopMetrics())
	require.NoError(tb, proxyApp.Start())
	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{})
	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(tb, err)
	require.NoError(tb, stateStore.Save(state))
	mp := &mpmocks.Mempool{}
	mp.On("Lock").Return()
	mp.On("Unlock").Return()
	mp.On("FlushAppConn", mock.Anything).Return(nil)
	mp.On("Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockExec := sm.NewBlockExecutor(stateStore, log.NewNopLogger(), proxyApp.Consensus(), mp, sm.EmptyEvidencePool{}, blockStore)
	return state, blockExec, blockStore, proxyApp
}

func fuzzChain(tb testing.TB) *fzChain {
	fzChainOnce.Do(func() {
		privVals := map[string]cmttypes.PrivValidator{}
		genDoc := &cmttypes.GenesisDoc{GenesisTime: fzGenesisTime, ChainID: "blocksync-fuzz", ConsensusParams: cmttypes.DefaultConsensusParams()}
		genDoc.ConsensusParams.ABCI.VoteExtensionsEnableHeight = fzExtHeight
		for i := 0; i < 4; i++ {
			pv := cmttypes.NewMockPVWithParams(ed25519.GenPrivKeyFromSecret([]byte{byte(i)}), false, false)
			privVals[pv.PrivKey.PubKey().Address().String()] = pv
			genDoc.Validators = append(genDoc.Validators, cmttypes.GenesisValidator{PubKey: pv.PrivKey.PubKey(), Power: 10})
		}
		require.NoError(tb, genDoc.ValidateAndComplete())
		state, blockExec, _, app := newFzBlockExec(tb, genDoc)
		defer func() { _ = app.Stop() }()
		c := &fzChain{genDoc: genDoc, blocks: map[int64]*cmttypes.Block{}, extCommits: map[int64]*cmttypes.ExtendedCommit{}}
		lastCommit := &cmttypes.Commit{}
		for h := int64(1); h <= fzChainHeight; h++ {
			block, parts, err := state.MakeBlock(h, cmttypes.MakeData(cmttypes.Txs{cmttypes.Tx(fmt.Sprintf("tx%d", h))}), lastCommit, nil, state.Validators.GetProposer().Address)
			require.NoError(tb, err)
			blockID := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
			ec := &cmttypes.ExtendedCommit{Height: h, BlockID: blockID}
			for i, val := range state.Validators.Validators {
				vote, err := cmttypes.MakeVote(privVals[val.Address.String()], genDoc.ChainID, int32(i), h, 0,
					cmtproto.PrecommitType, blockID, fzGenesisTime.Add(time.Duration(h)*time.Second))
				require.NoError(tb, err)
				ec.ExtendedSignatures = append(ec.ExtendedSignatures, vote.ExtendedCommitSig())
			}
			state, err = blockExec.ApplyBlock(state, blockID, block, lastCommit)
			require.NoError(tb, err)
			c.blocks[h], c.extCommits[h], lastCommit = block, ec, ec.ToCommit()
		}
		fzChainVal = c
	})
	return fzChainVal
}

// fzPeer records what the reactor sends to it.
type fzPeer struct {
	*p2pmock.Peer
	id   p2p.ID
	sent []proto.Message
}

func (p *fzPeer) ID() p2p.ID { return p.id }
func (p *fzPeer) TrySend(e p2p.Envelope) bool {
	p.sent = append(p.sent, e.Message)
	return true
}
func (p *fzPeer) Send(e p2p.Envelope) bool { return p.TrySend(e) }

// nopTransport lets the real Switch stop peers without a network.
type nopTransport struct{ p2p.Transport }

func (nopTransport) Cleanup(p2p.Peer) {}

type bsHarness struct {
	t         *testing.T
	chain     *fzChain
	r         *Reactor
	sw        *p2p.Switch
	state     sm.State
	peers     [fzNumPeers]*fzPeer
	synced    uint64
	stoppedBy map[p2p.ID]bool
}

// newBSHarness builds a fresh receiving node at genesis. Its pool is marked
// running without its background routines; the harness performs their work
// (creating requesters, assigning peers, handling errors, verifying and
// applying blocks) deterministically.
func newBSHarness(t *testing.T, chain *fzChain) *bsHarness {
	state, blockExec, blockStore, app := newFzBlockExec(t, chain.genDoc)
	r := NewReactor(state.Copy(), blockExec, blockStore, false, NopMetrics(), 0)
	r.SetLogger(log.NewNopLogger())
	r.pool.BaseService = *service.NewBaseService(nil, "BlockPool", service.NewBaseService(nil, "nop", nil))
	require.NoError(t, r.pool.Start())
	sw := p2p.NewSwitch(cmtcfg.DefaultP2PConfig(), nopTransport{})
	sw.SetLogger(log.NewNopLogger())
	sw.AddReactor("BLOCKSYNC", r)
	h := &bsHarness{t: t, chain: chain, r: r, sw: sw, state: state, stoppedBy: map[p2p.ID]bool{}}
	for i := range h.peers {
		h.connect(i)
	}
	t.Cleanup(func() {
		for _, p := range h.peers {
			r.pool.RemovePeer(p.id) // stops per-peer timeout timers
			_ = p.Stop()
		}
		_ = r.pool.Stop()
		r.BaseReactor.OnStop()
		_ = app.Stop()
	})
	return h
}

func (h *bsHarness) connect(i int) {
	h.peers[i] = &fzPeer{Peer: p2pmock.NewPeer(nil), id: p2p.ID(fmt.Sprintf("%040x", i+1))}
	h.r.AddPeer(h.peers[i])
}

func (h *bsHarness) receive(p *fzPeer, msg proto.Message) {
	h.r.Receive(p2p.Envelope{Src: p, ChannelID: BlocksyncChannel, Message: msg})
}

// drainErrors does what poolRoutine does with pool errors.
func (h *bsHarness) drainErrors() map[p2p.ID]bool {
	got := map[p2p.ID]bool{}
	for {
		select {
		case err := <-h.r.errorsCh:
			got[err.peerID] = true
			for _, p := range h.peers {
				if p.id == err.peerID && p.IsRunning() {
					h.sw.StopPeerForError(p, err, h.r.String())
				}
			}
		default:
			return got
		}
	}
}

// schedule mirrors makeRequestersRoutine and bpRequester.requestRoutine:
// open requesters up to the max peer height and (re)assign peers to them.
func (h *bsHarness) schedule() {
	pool := h.r.pool
	pool.mtx.Lock()
	for len(pool.requesters) < fzMaxReqs {
		next := pool.height + int64(len(pool.requesters))
		if next > pool.maxPeerHeight {
			break
		}
		pool.requesters[next] = newBPRequester(pool, next)
		atomic.AddInt32(&pool.numPending, 1)
	}
	heights := make([]int64, 0, len(pool.requesters))
	for height := range pool.requesters {
		heights = append(heights, height)
	}
	pool.mtx.Unlock()
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	for _, height := range heights {
		pool.mtx.Lock()
		bpr := pool.requesters[height]
		var prev p2p.ID
		if pr := pool.requesters[height-1]; pr != nil {
			prev = pr.gotBlockFromPeerID()
		}
		pool.mtx.Unlock()
		var ignore p2p.ID
		select {
		case peerID := <-bpr.redoCh:
			bpr.reset(peerID)
			ignore = peerID
		default:
		}
		bpr.mtx.Lock()
		idle := bpr.peerID == "" && bpr.block == nil
		bpr.mtx.Unlock()
		if !idle {
			continue
		}
		if peer := pool.pickIncrAvailablePeer(height, ignore, prev); peer != nil {
			bpr.mtx.Lock()
			bpr.peerID = peer.id
			bpr.requestedFromPeers[peer.id] = struct{}{}
			bpr.mtx.Unlock()
		}
	}
}

// syncStep performs one iteration of poolRoutine's block processing.
func (h *bsHarness) syncStep() {
	t, r := h.t, h.r
	first, second, extCommit := r.pool.PeekTwoBlocks()
	if first == nil || second == nil {
		return
	}
	require.Equal(t, h.state.LastBlockHeight+1, first.Height)
	require.Equal(t, first.Height+1, second.Height)
	firstParts, err := first.MakePartSet(cmttypes.BlockPartSizeBytes)
	require.NoError(t, err)
	firstID := cmttypes.BlockID{Hash: first.Hash(), PartSetHeader: firstParts.Header()}
	chainID := h.state.ChainID
	err = h.state.Validators.VerifyCommit(chainID, firstID, first.Height, second.LastCommit)
	if err == nil {
		if h.synced == 0 {
			err = r.blockExec.ValidateBlock(h.state, first)
		} else {
			err = r.blockExec.ValidateBlockSkipLastCommit(h.state, first)
		}
	}
	if err == nil && r.verifyData {
		var ok bool
		if ok, err = r.blockExec.ProcessProposal(first, h.state.InitialHeight); !ok && err == nil {
			err = fmt.Errorf("app rejected block")
		}
	}
	extEnabled := h.state.ConsensusParams.ABCI.VoteExtensionsEnabled(first.Height)
	if (extCommit != nil) != extEnabled {
		err = fmt.Errorf("extended commit presence mismatch")
	}
	if err == nil && extEnabled {
		err = extCommit.EnsureExtensions(true)
	}
	if err == nil && extEnabled {
		err = h.state.Validators.VerifyCommit(chainID, firstID, first.Height, extCommit.ToCommit())
	}
	if err != nil {
		for _, height := range []int64{first.Height, second.Height} {
			// The harness peers are not in the switch's peer set, so stop
			// them directly.
			peerID := r.pool.RemovePeerAndRedoAllPeerRequests(height)
			for _, p := range h.peers {
				if p.id == peerID && p.IsRunning() {
					h.sw.StopPeerForError(p, ErrReactorValidation{Err: err}, r.String())
				}
			}
		}
		return
	}
	// Commit verification passed, so this must be the canonical block.
	canonical := h.chain.blocks[first.Height]
	require.Equal(t, canonical.Hash(), first.Hash(), "blocksync accepted a non-canonical block at %d", first.Height)
	r.pool.PopRequest()
	if extEnabled {
		require.Equal(t, h.chain.extCommits[first.Height].ToCommit().Hash(), extCommit.ToCommit().Hash())
		// ToCommit drops vote extensions, so check the persisted extension
		// signatures separately: consensus re-verifies them on restart.
		for i, sig := range extCommit.ExtendedSignatures {
			if !fzCheckExtSigs || sig.BlockIDFlag != cmttypes.BlockIDFlagCommit {
				continue
			}
			_, val := h.state.Validators.GetByIndex(int32(i))
			require.NoError(t, extCommit.GetExtendedVote(int32(i)).VerifyExtension(chainID, val.PubKey),
				"blocksync would persist an invalid vote extension at %d", first.Height)
		}
		r.store.SaveBlockWithExtendedCommit(first, firstParts, extCommit)
	} else {
		r.store.SaveBlock(first, firstParts, second.LastCommit)
	}
	h.state, err = r.blockExec.ApplyVerifiedBlock(h.state, firstID, first, second.LastCommit)
	require.NoError(t, err)
	h.synced++
}

// checkPool asserts structural invariants of the pool after every message.
func (h *bsHarness) checkPool() {
	pool := h.r.pool
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	require.LessOrEqual(h.t, len(pool.peers), fzNumPeers)
	require.Len(h.t, pool.sortedPeers, len(pool.peers))
	require.LessOrEqual(h.t, len(pool.requesters), fzMaxReqs)
	require.Equal(h.t, h.state.LastBlockHeight+1, pool.height)
	var maxHeight int64
	for id, p := range pool.peers {
		require.LessOrEqual(h.t, int64(0), p.base, "peer %s", id)
		require.LessOrEqual(h.t, p.base, p.height, "peer %s", id)
		if p.base <= pool.height && p.height > maxHeight {
			maxHeight = p.height
		}
	}
	require.Equal(h.t, maxHeight, pool.maxPeerHeight)
	for height := range pool.requesters {
		require.GreaterOrEqual(h.t, height, pool.height)
		require.Less(h.t, height, pool.height+fzMaxReqs)
	}
	require.Equal(h.t, h.state.LastBlockHeight, h.r.store.Height())
}

type fzReader struct{ b []byte }

func (r *fzReader) byte() byte {
	if len(r.b) == 0 {
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *fzReader) uint64() uint64 {
	var buf [8]byte
	for i := range buf {
		buf[i] = r.byte()
	}
	return binary.BigEndian.Uint64(buf[:])
}

// height picks a height near the chain, or an extreme value.
func (r *fzReader) height(near int64) int64 {
	switch c := r.byte(); c % 8 {
	case 5:
		return math.MaxInt64 - int64(c>>3)
	case 6:
		return -1 - int64(c>>3)
	case 7:
		return int64(r.uint64())
	default:
		return near + int64(c>>3)%(fzChainHeight+2) - 2
	}
}

// blockResponse builds a response for the canonical block at height, possibly
// mutated as a byzantine peer would.
func (h *bsHarness) blockResponse(r *fzReader, height int64) (msg *bcproto.BlockResponse, mutated bool) {
	if height < 1 || height > fzChainHeight {
		height = 1 + (height%fzChainHeight+fzChainHeight)%fzChainHeight
	}
	block := h.chain.blocks[height]
	ec := h.chain.extCommits[height].Clone()
	if !h.chain.genDoc.ConsensusParams.ABCI.VoteExtensionsEnabled(height) {
		ec = nil
	}
	if m := r.byte(); m&0x80 != 0 {
		mutated = true
		// Deep copy through the wire encoding: ToProto shares byte slices
		// with the canonical block.
		bpb, err := block.ToProto()
		require.NoError(h.t, err)
		bz, err := bpb.Marshal()
		require.NoError(h.t, err)
		var copied cmtproto.Block
		require.NoError(h.t, copied.Unmarshal(bz))
		block, err = cmttypes.BlockFromProto(&copied)
		require.NoError(h.t, err)
		rebuild := true
		switch m % 8 {
		case 0: // corrupt a LastCommit signature, keep the header consistent
			if len(block.LastCommit.Signatures) > 0 {
				block.LastCommit.Signatures[int(r.byte())%len(block.LastCommit.Signatures)].Signature[0] ^= 1 + r.byte()%255
			}
		case 1:
			block.Txs = append(block.Txs, cmttypes.Tx{r.byte()})
		case 2:
			block.AppHash = []byte{r.byte()}
		case 3:
			block.Height = r.height(height)
			ec = nil
		case 4: // drop or add the extended commit
			rebuild = false
			if ec != nil {
				ec = nil
			} else {
				ec = h.chain.extCommits[height].Clone()
			}
		case 5:
			rebuild = false
			if ec != nil && len(ec.ExtendedSignatures) > 0 {
				i := int(r.byte()) % len(ec.ExtendedSignatures)
				if r.byte()&1 == 0 {
					ec.ExtendedSignatures[i].ExtensionSignature = []byte{r.byte()}
				} else {
					sig := append([]byte{}, ec.ExtendedSignatures[i].Signature...)
					sig[0] ^= 1
					ec.ExtendedSignatures[i].Signature = sig
				}
			}
		case 6:
			rebuild = false
			if ec != nil {
				ec.Height++
			}
		case 7:
			rebuild = false
			if ec != nil && len(ec.ExtendedSignatures) > 0 {
				ec.ExtendedSignatures[int(r.byte())%len(ec.ExtendedSignatures)].BlockIDFlag = cmttypes.BlockIDFlagAbsent
			}
		}
		if rebuild {
			block.LastCommitHash, block.DataHash = block.LastCommit.Hash(), block.Data.Hash()
		}
	}
	bpb, err := block.ToProto()
	if err != nil {
		bpb = &cmtproto.Block{}
	}
	return &bcproto.BlockResponse{Block: bpb, ExtCommit: ec.ToProto()}, mutated
}

// FuzzBlockSyncReactor drives a receiving node's blocksync reactor with a
// sequence of peer messages: status ranges (including extreme values),
// canonical or mutated block responses, block/status requests, no-block
// responses and disconnects, interleaved with the pool's scheduling and the
// poolRoutine verification step. Only canonical blocks may ever be applied.
func FuzzBlockSyncReactor(f *testing.F) {
	chain := fuzzChain(f)
	// Every peer reports [1, 7], then answer each peer's request and sync.
	// After round 0 only peer 2 still has a request outstanding, so round i
	// syncs height i+1 from peer 2 with the blockResponse mutation bytes in
	// mutations[i] (canonical if absent).
	syncSeed := func(mutations map[int][]byte) []byte {
		seed := []byte{0, 0, 0x10, 0x10, 0, 1, 0x10, 0x10, 0, 2, 0x10, 0x10}
		for i := 0; i < 8; i++ {
			mut, ok := mutations[i]
			if !ok {
				mut = []byte{0}
			}
			seed = append(seed, 6, 0, 1, 0, 0, 0, 1, 1, 0, 0, 1, 2, 0)
			seed = append(seed, mut...)
			seed = append(seed, 6, 0)
		}
		return seed
	}
	f.Add(syncSeed(nil))
	// The same with a byzantine response: a corrupted LastCommit signature
	// at height 3, a corrupted extended commit signature at height 5, and a
	// dropped extended commit at height 5.
	f.Add(syncSeed(map[int][]byte{2: {0x80, 0, 0}}))
	f.Add(syncSeed(map[int][]byte{4: {0x85, 0, 1}}))
	f.Add(syncSeed(map[int][]byte{4: {0x84}}))
	// Extreme and invalid status ranges, unsolicited blocks, requests.
	f.Add([]byte{0, 0, 5, 0, 0, 1, 0x10, 6, 2, 0, 0, 0, 3, 1, 0, 4, 0, 0x08, 5, 1, 7, 2, 7, 2})
	f.Add([]byte{0, 0, 0x10, 0x10, 6, 0, 2, 1, 1, 0, 1, 0, 0, 0x83, 0x18, 6, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		h := newBSHarness(t, chain)
		r := &fzReader{b: data}
		for op := 0; op < fzMaxOps && len(r.b) > 0; op++ {
			kind, p := r.byte()%8, h.peers[int(r.byte())%fzNumPeers]
			if kind == 7 { // disconnect a peer, or reconnect a stopped one
				if p.IsRunning() {
					h.sw.StopPeerForError(p, "fuzz disconnect", h.r.String())
				} else {
					for i := range h.peers {
						if h.peers[i] == p {
							h.connect(i)
						}
					}
				}
				h.checkPool()
				continue
			}
			if !p.IsRunning() && kind != 6 {
				continue // a disconnected peer cannot send anything
			}
			pool := h.r.pool
			switch kind {
			case 0:
				// Pick heights relative to the chain tip, then base <= height
				// most of the time.
				height := r.height(fzChainHeight)
				base := r.height(1)
				h.receive(p, &bcproto.StatusResponse{Base: base, Height: height})
				if base < 0 || height < 0 || base > height {
					require.False(t, p.IsRunning(), "invalid status range accepted")
				}
			case 1, 2:
				height := pool.Height() + int64(r.byte()%(fzMaxReqs+2)) - 1
				if kind == 1 { // answer the lowest request made to this peer, if any
					pool.mtx.Lock()
					// Visit heights in order: map iteration order would make
					// replays of the same input pick different heights.
					heights := make([]int64, 0, len(pool.requesters))
					for hh := range pool.requesters {
						heights = append(heights, hh)
					}
					sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
					for _, hh := range heights {
						if bpr := pool.requesters[hh]; bpr.didRequestFrom(p.id) && bpr.getBlock() == nil {
							height = hh
							break
						}
					}
					pool.mtx.Unlock()
				}
				msg, _ := h.blockResponse(r, height)
				block, decodeErr := cmttypes.BlockFromProto(msg.Block)
				pool.mtx.Lock()
				expectErr, skip := false, false
				if decodeErr == nil {
					bpr, banned := pool.requesters[block.Height], pool.isPeerBanned(p.id)
					// Excluded pending PROTOCO-3517; remove with the fix.
					if bp := pool.peers[p.id]; bp != nil && bp.recvMonitor == nil &&
						((bpr == nil && block.Height >= pool.startHeight && block.Height <= pool.height) ||
							(bpr != nil && bpr.didRequestFrom(p.id) && bpr.getBlock() == nil)) {
						skip = true
					}
					expectErr = (msg.ExtCommit != nil && msg.ExtCommit.Height != block.Height) ||
						(bpr == nil && (block.Height > pool.height || block.Height < pool.startHeight)) ||
						(bpr != nil && !bpr.didRequestFrom(p.id) && !banned)
				}
				pool.mtx.Unlock()
				if skip {
					break
				}
				h.drainErrors()
				h.receive(p, msg)
				if decodeErr != nil {
					require.False(t, p.IsRunning(), "undecodable block accepted")
				} else if errs := h.drainErrors(); expectErr {
					require.True(t, errs[p.id] || !p.IsRunning(), "unexpected block not reported")
				}
			case 3:
				h.receive(p, &bcproto.NoBlockResponse{Height: r.height(pool.Height())})
			case 4:
				height := r.height(h.r.store.Height())
				p.sent = nil
				h.receive(p, &bcproto.BlockRequest{Height: height})
				if height < 0 {
					require.False(t, p.IsRunning())
					break
				}
				require.Len(t, p.sent, 1)
				if height >= 1 && height <= h.r.store.Height() {
					resp, ok := p.sent[0].(*bcproto.BlockResponse)
					require.True(t, ok, "expected block response for %d", height)
					got, err := cmttypes.BlockFromProto(resp.Block)
					require.NoError(t, err)
					require.Equal(t, h.chain.blocks[height].Hash(), got.Hash())
				} else {
					require.Equal(t, &bcproto.NoBlockResponse{Height: height}, p.sent[0])
				}
			case 5:
				p.sent = nil
				h.receive(p, &bcproto.StatusRequest{})
				require.Equal(t, []proto.Message{&bcproto.StatusResponse{Height: h.r.store.Height(), Base: h.r.store.Base()}}, p.sent)
			case 6:
				h.drainErrors()
				h.schedule()
				h.syncStep()
				h.drainErrors()
			}
			h.checkPool()
		}
	})
}
