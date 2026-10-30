package consensus

import (
	"bytes"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	abcicli "github.com/cometbft/cometbft/abci/client"
	"github.com/cometbft/cometbft/abci/example/kvstore"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/p2p"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtprotobits "github.com/cometbft/cometbft/proto/tendermint/libs/bits"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

// Shared harness for the consensus state and reactor fuzz targets. Every
// iteration builds a fresh, unstarted State over in-memory stores with four
// equal-power validators whose keys are derived deterministically. Messages
// are processed synchronously by calling handleMsg (as receiveRoutine does)
// and timeouts are fired explicitly, so there is no timing dependence.

const (
	fuzzNumVals   = 4
	fuzzMaxInput  = 8 << 10
	fuzzMaxOps    = 48
	fuzzMaxRounds = 16 // proposer lookups loop once per round
)

var fuzzGenesisTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// fuzzBytes is a cursor that turns fuzz input into structured fields; it
// returns zeros once the input is exhausted.
type fuzzBytes struct{ b []byte }

func (r *fuzzBytes) u8() byte {
	v := r.bytes(1)
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

func (r *fuzzBytes) u16() uint16 { return uint16(r.u8())<<8 | uint16(r.u8()) }

func (r *fuzzBytes) bytes(n int) []byte {
	n = min(n, len(r.b))
	v := append([]byte(nil), r.b[:n]...)
	r.b = r.b[n:]
	return v
}

// height returns a height near cur, occasionally an extreme value.
func (r *fuzzBytes) height(cur int64) int64 {
	return [...]int64{cur - 1, cur + 1, 0, math.MaxInt64, -1, cur, cur, cur, cur, cur}[r.u8()%10]
}

// round returns a round near cur, occasionally an extreme value.
func (r *fuzzBytes) round(cur int32) int32 {
	skip := cur + int32(r.u8()%fuzzMaxRounds)
	return [...]int32{cur + 1, skip, -1, math.MaxInt32, 0, cur, cur, cur, cur, cur}[r.u8()%10]
}

func (r *fuzzBytes) voteType() cmtproto.SignedMsgType {
	odd := cmtproto.SignedMsgType(r.u8())
	return [...]cmtproto.SignedMsgType{
		cmtproto.PrevoteType, cmtproto.PrevoteType, cmtproto.PrecommitType, cmtproto.PrecommitType, cmtproto.ProposalType, odd,
	}[r.u8()%6]
}

// bitArray returns a wire bit array whose Bits and Elems are fuzzed
// independently, so they are often of odd or mismatched sizes.
func (r *fuzzBytes) bitArray() cmtprotobits.BitArray {
	n := int64(r.u8() % 70)
	if r.u8()%8 == 0 {
		n = int64(r.u16())
	}
	elems := make([]uint64, (n+63)/64)
	if r.u8()%8 == 0 {
		elems = make([]uint64, r.u8()%4)
	}
	for i := range elems {
		elems[i] = uint64(r.u16())<<48 | uint64(r.u16())
	}
	return cmtprotobits.BitArray{Bits: n, Elems: elems}
}

func (r *fuzzBytes) hash() []byte {
	switch r.u8() % 4 {
	case 0:
		return nil
	case 1:
		return r.bytes(int(r.u8() % 40))
	}
	return append(r.bytes(32), make([]byte, 32)...)[:32]
}

func (r *fuzzBytes) partSetHeader() cmttypes.PartSetHeader {
	return cmttypes.PartSetHeader{Total: uint32(r.u16()), Hash: r.hash()}
}

type fuzzBlock struct {
	parts *cmttypes.PartSet
	id    cmttypes.BlockID
}

type fuzzNode struct {
	t       testing.TB
	cs      *State
	pvs     []cmttypes.PrivValidator // indexed like the validator set
	byz     int32                    // may sign anything; holds < 1/3 power
	ours    int32                    // the node under test
	honest  map[int32]bool           // validators that only sign harness blocks and never equivocate
	forge   bool                     // only byz signs; other signatures are forged
	ticker  *fuzzTicker
	blocks  map[string]*fuzzBlock // "height/variant"
	fuzzPS  *cmttypes.PartSet     // part set over fuzzed bytes
	signed  map[string]cmttypes.BlockID
	chainID string
}

// fuzzTicker records the latest timeout with the real ticker's ordering rule;
// the harness fires it explicitly.
type fuzzTicker struct {
	last    timeoutInfo
	pending bool
}

func (*fuzzTicker) Start() error             { return nil }
func (*fuzzTicker) Stop() error              { return nil }
func (*fuzzTicker) Chan() <-chan timeoutInfo { return nil }
func (*fuzzTicker) SetLogger(log.Logger)     {}

func (tk *fuzzTicker) ScheduleTimeout(ti timeoutInfo) {
	l := tk.last
	if ti.Height < l.Height || (ti.Height == l.Height &&
		(ti.Round < l.Round || (ti.Round == l.Round && l.Step > 0 && ti.Step <= l.Step))) {
		return
	}
	tk.last, tk.pending = ti, true
}

func fuzzGenesis(tb testing.TB) (*cmttypes.GenesisDoc, map[string]cmttypes.PrivValidator) {
	pvs := make(map[string]cmttypes.PrivValidator, fuzzNumVals)
	vals := make([]cmttypes.GenesisValidator, fuzzNumVals)
	for i := range vals {
		pk := ed25519.GenPrivKeyFromSecret([]byte{'f', 'u', 'z', 'z', byte(i)})
		pvs[string(pk.PubKey().Address())] = cmttypes.NewMockPVWithParams(pk, false, false)
		vals[i] = cmttypes.GenesisValidator{PubKey: pk.PubKey(), Power: 10}
	}
	genDoc := &cmttypes.GenesisDoc{
		GenesisTime: fuzzGenesisTime, InitialHeight: 1, ChainID: test.DefaultTestChainID,
		Validators: vals, ConsensusParams: cmttypes.DefaultConsensusParams(),
	}
	require.NoError(tb, genDoc.ValidateAndComplete())
	return genDoc, pvs
}

// newFuzzNode builds an unstarted State at height 1, round 0. The genesis
// proposer is the byzantine validator; the node under test is another one.
func newFuzzNode(t testing.TB, genDoc *cmttypes.GenesisDoc, pvByAddr map[string]cmttypes.PrivValidator) *fuzzNode {
	state, err := sm.MakeGenesisState(genDoc)
	require.NoError(t, err)
	db := dbm.NewMemDB()
	stateStore := sm.NewStore(db, sm.StoreOptions{})
	require.NoError(t, stateStore.Save(state))
	blockStore := store.NewBlockStore(db)
	proxyApp := proxy.NewAppConnConsensus(
		abcicli.NewLocalClient(new(cmtsync.Mutex), kvstore.NewInMemoryApplication()), proxy.NopMetrics())
	logger := log.NewNopLogger()
	blockExec := sm.NewBlockExecutor(stateStore, logger, proxyApp, emptyMempool{}, sm.EmptyEvidencePool{}, blockStore)

	n := &fuzzNode{
		t: t, pvs: make([]cmttypes.PrivValidator, fuzzNumVals), honest: map[int32]bool{},
		ticker: &fuzzTicker{}, blocks: map[string]*fuzzBlock{}, signed: map[string]cmttypes.BlockID{},
		chainID: state.ChainID,
	}
	for i, v := range state.Validators.Validators {
		n.pvs[i] = pvByAddr[string(v.Address)]
	}
	n.byz, _ = state.Validators.GetByAddress(state.Validators.GetProposer().Address)
	n.ours = (n.byz + 1) % fuzzNumVals

	consCfg := cmtcfg.TestConsensusConfig()
	cs := NewState(consCfg, state, blockExec, blockStore, propagation.NewNoOpPropagator(), emptyMempool{}, sm.EmptyEvidencePool{})
	cs.SetLogger(logger)
	cs.SetPrivValidator(n.pvs[n.ours])
	cs.SetTimeoutTicker(n.ticker)
	eventBus := cmttypes.NewEventBus()
	eventBus.SetLogger(logger)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })
	cs.SetEventBus(eventBus)
	n.cs = cs

	cs.lockAll()
	cs.enterNewRound(1, 0)
	cs.unlockAll()
	n.drain()
	return n
}

// drain processes queued messages in a fixed order: our own messages first,
// then peer messages, like a receiveRoutine that prefers internal input.
func (n *fuzzNode) drain() {
	for i := 0; i < 10_000; i++ {
		select {
		case mi := <-n.cs.internalMsgQueue:
			n.cs.handleMsg(mi)
			continue
		default:
		}
		select {
		case mi := <-n.cs.peerMsgQueue:
			n.cs.handleMsg(mi)
		default:
			return
		}
	}
	n.t.Fatal("consensus message queues did not drain")
}

func (n *fuzzNode) fireTimeout() {
	if !n.ticker.pending {
		return
	}
	n.ticker.pending = false
	n.cs.handleTimeout(n.ticker.last, *n.cs.GetRoundState())
	n.drain()
}

// block returns a harness proposal block for the current height: variants 0
// and 1 are valid (different txs), variant 2 decodes but fails validation.
func (n *fuzzNode) block(variant byte) *fuzzBlock {
	rs, state := n.cs.GetRoundState(), n.cs.GetState()
	key := fmt.Sprintf("%d/%d", rs.Height, variant)
	if b, ok := n.blocks[key]; ok {
		return b
	}
	commit := &cmttypes.Commit{}
	if rs.Height > state.InitialHeight {
		commit = rs.LastCommit.MakeExtendedCommit(state.ConsensusParams.ABCI).ToCommit()
	}
	var txs []cmttypes.Tx
	if variant == 1 {
		txs = []cmttypes.Tx{cmttypes.Tx("fuzz=1")}
	}
	proposer := state.Validators.GetProposer().Address
	if variant == 2 {
		proposer = bytes.Repeat([]byte{0xab}, 20)
	}
	blk, parts, err := state.MakeBlock(rs.Height, cmttypes.MakeData(txs), commit, nil, proposer)
	require.NoError(n.t, err)
	b := &fuzzBlock{parts: parts, id: cmttypes.BlockID{Hash: blk.Hash(), PartSetHeader: parts.Header()}}
	n.blocks[key] = b
	return b
}

// fuzzParts returns a part set over fuzzed bytes, built once per iteration.
func (n *fuzzNode) fuzzParts(r *fuzzBytes) *cmttypes.PartSet {
	if n.fuzzPS == nil {
		size := []int{1, 100, int(cmttypes.BlockPartSizeBytes) + 1, 2*int(cmttypes.BlockPartSizeBytes) + 7}[r.u8()%4]
		seed := append(r.bytes(int(r.u8())), 0)
		ps, err := cmttypes.NewPartSetFromData(bytes.Repeat(seed, size/len(seed)+1)[:size], cmttypes.BlockPartSizeBytes)
		require.NoError(n.t, err)
		n.fuzzPS = ps
	}
	return n.fuzzPS
}

// blockID picks a block ID; honest signers only ever reference valid
// harness blocks (or nil).
func (n *fuzzNode) blockID(r *fuzzBytes, honest bool) cmttypes.BlockID {
	c := r.u8() % 7
	if honest {
		c %= 3
	}
	switch c {
	case 0:
		return cmttypes.BlockID{}
	case 1, 2, 3:
		return n.block(c - 1).id
	case 4:
		ps := n.fuzzParts(r)
		return cmttypes.BlockID{Hash: r.hash(), PartSetHeader: ps.Header()}
	case 5:
		if meta := n.cs.blockStore.LoadBlockMeta(n.cs.GetRoundState().Height - 1); meta != nil {
			return meta.BlockID
		}
		return cmttypes.BlockID{}
	default:
		return cmttypes.BlockID{Hash: r.hash(), PartSetHeader: r.partSetHeader()}
	}
}

func (n *fuzzNode) proposerFor(round int32) int32 {
	if round < 0 || round > fuzzMaxRounds {
		return n.byz
	}
	vals := n.cs.GetState().Validators.Copy()
	if round > 0 {
		vals.IncrementProposerPriority(round)
	}
	idx, _ := vals.GetByAddress(vals.GetProposer().Address)
	return idx
}

// once enforces that honest validators never sign two different block IDs
// for the same height/round/type; it returns the ID they signed first.
func (n *fuzzNode) once(signer int32, h int64, rnd int32, typ cmtproto.SignedMsgType, id cmttypes.BlockID) cmttypes.BlockID {
	key := fmt.Sprintf("%d/%d/%d/%d", signer, h, rnd, typ)
	if prev, ok := n.signed[key]; ok {
		return prev
	}
	n.signed[key] = id
	return id
}

// proposal returns a proposal signed by a validator, possibly mutated after
// signing so the signature no longer verifies.
func (n *fuzzNode) proposal(r *fuzzBytes) *cmttypes.Proposal {
	rs := n.cs.GetRoundState()
	h, rnd := r.height(rs.Height), r.round(rs.Round)
	pol := []int32{-1, -1, rnd - 1, rnd, -2, int32(r.u8())}[r.u8()%6]
	signer := n.proposerFor(rnd)
	if r.u8()%4 == 0 {
		signer = int32(r.u8() % fuzzNumVals)
	}
	if signer == n.ours {
		signer = n.byz // a peer cannot sign with our key
	}
	honest := n.honest[signer]
	id := n.blockID(r, honest)
	if honest {
		id = n.once(signer, h, rnd, cmtproto.ProposalType, id)
	}
	prop := cmttypes.NewProposal(h, rnd, pol, id)
	prop.Timestamp = fuzzGenesisTime.Add(time.Duration(rnd) * time.Second)
	prop.Signature = make([]byte, 64) // sign bytes require a well-formed block ID
	if id.ValidateBasic() == nil {
		p := prop.ToProto()
		require.NoError(n.t, n.pvs[signer].SignProposal(n.chainID, p))
		prop.Signature = p.Signature
	}
	if n.forge && signer != n.byz {
		prop.Signature = append(r.bytes(64), make([]byte, 64)...)[:64]
	}
	switch r.u8() % 8 {
	case 0:
		prop.Signature = append([]byte(nil), prop.Signature...)
		prop.Signature[r.u8()%64] ^= 1 << (r.u8() % 8)
	case 1:
		prop.POLRound = int32(r.u8()) - 2
	case 2:
		prop.BlockID.PartSetHeader.Total = uint32(r.u16())
	case 3:
		prop.Signature = r.bytes(int(r.u8() % 80))
	}
	return prop
}

// part returns a block part from a harness or fuzzed part set, optionally
// with mutated bytes, index or proof.
func (n *fuzzNode) part(r *fuzzBytes) *BlockPartMessage {
	rs := n.cs.GetRoundState()
	var ps *cmttypes.PartSet
	if c := r.u8() % 5; c < 3 {
		ps = n.block(c).parts
	} else {
		ps = n.fuzzParts(r)
	}
	src := ps.GetPart(int(r.u8()) % int(ps.Total()))
	part := &cmttypes.Part{
		Index: src.Index, Bytes: append([]byte(nil), src.Bytes...),
		Proof: src.Proof,
	}
	part.Proof.Aunts = append([][]byte(nil), src.Proof.Aunts...)
	switch r.u8() % 10 {
	case 0:
		if len(part.Bytes) > 0 {
			part.Bytes[int(r.u16())%len(part.Bytes)] ^= 1
		}
	case 1:
		part.Index = uint32(r.u16())
	case 2:
		if len(part.Proof.Aunts) > 0 {
			part.Proof.Aunts = part.Proof.Aunts[:len(part.Proof.Aunts)-1]
		}
	case 3:
		part.Proof.Index = int64(r.u8())
	case 4:
		part.Proof.Total = int64(r.u16())
	case 5:
		part.Bytes = r.bytes(int(r.u8()))
	case 6:
		part.Proof.LeafHash = r.hash()
	}
	return &BlockPartMessage{Height: r.height(rs.Height), Round: r.round(rs.Round), Part: part}
}

// vote returns a vote signed by a validator, possibly mutated after signing.
func (n *fuzzNode) vote(r *fuzzBytes) *cmttypes.Vote {
	rs := n.cs.GetRoundState()
	signer := int32(r.u8() % fuzzNumVals)
	if signer == n.ours {
		signer = n.byz
	}
	typ, h, rnd := r.voteType(), r.height(rs.Height), r.round(rs.Round)
	honest := n.honest[signer]
	id := n.blockID(r, honest)
	if honest {
		// +2/3 at a round makes the node skip to it, and the proposer
		// rotation loops once per round, so honest validators stay in
		// low rounds.
		rnd = min(rnd, fuzzMaxRounds)
		if h != rs.Height {
			id = cmttypes.BlockID{}
			if h == rs.Height-1 && typ == cmtproto.PrecommitType {
				if meta := n.cs.blockStore.LoadBlockMeta(h); meta != nil {
					id = meta.BlockID
				}
			}
		}
		id = n.once(signer, h, rnd, typ, id)
	}
	addr := n.cs.GetState().Validators.Validators[signer].Address
	v := &cmttypes.Vote{
		Type: typ, Height: h, Round: rnd, BlockID: id, ValidatorAddress: addr, ValidatorIndex: signer,
		Timestamp: fuzzGenesisTime.Add(time.Duration(max(h, 0)%1000) * time.Second),
	}
	v.Signature = make([]byte, 64) // sign bytes require a well-formed block ID
	if id.ValidateBasic() == nil {
		p := v.ToProto()
		require.NoError(n.t, n.pvs[signer].SignVote(n.chainID, p))
		v.Signature = p.Signature
	}
	if n.forge && signer != n.byz {
		v.Signature = append(r.bytes(64), make([]byte, 64)...)[:64]
	}
	switch r.u8() % 12 {
	case 0:
		v.Signature = append([]byte(nil), v.Signature...)
		v.Signature[r.u8()%64] ^= 1 << (r.u8() % 8)
	case 1:
		v.ValidatorIndex = int32(int8(r.u8()))
	case 2:
		v.ValidatorAddress = n.cs.GetState().Validators.Validators[r.u8()%fuzzNumVals].Address
	case 3:
		v.Round = r.round(rs.Round)
	case 4:
		v.Extension, v.ExtensionSignature = r.bytes(int(r.u8())), r.bytes(int(r.u8()%80))
	case 5:
		v.Timestamp = v.Timestamp.Add(time.Nanosecond)
	case 6:
		v.Signature = r.bytes(int(r.u8() % 80))
	}
	return v
}

// wire round-trips msg through the peer wire format, as the p2p layer does.
// It returns the decoded inner proto, or nil if msg cannot be encoded.
func wire(msg Message) proto.Message {
	pb, err := MsgToProto(msg)
	if err != nil {
		return nil
	}
	return wireProto(pb)
}

func wireProto(pb proto.Message) proto.Message {
	raw, err := proto.Marshal(pb.(p2p.Wrapper).Wrap())
	var env cmtcons.Message
	if err != nil || proto.Unmarshal(raw, &env) != nil {
		return nil
	}
	inner, _ := env.Unwrap()
	return inner
}

// checkRoundState asserts invariants that must hold whatever peers send.
func (n *fuzzNode) checkRoundState(t *testing.T) {
	rs := n.cs.GetRoundState()
	if rs.Proposal != nil {
		// Verify against the live proposer of the current round, not the
		// harness's proposerFor, which gives up beyond fuzzMaxRounds even
		// though the state can legitimately get there via timeouts.
		pub := rs.Validators.GetProposer().PubKey
		require.True(t, pub.VerifySignature(cmttypes.ProposalSignBytes(n.chainID, rs.Proposal.ToProto()), rs.Proposal.Signature),
			"accepted proposal with invalid signature")
		require.Equal(t, rs.Height, rs.Proposal.Height)
		require.Equal(t, rs.Round, rs.Proposal.Round)
	}
	for round := int32(0); round <= rs.Round; round++ {
		for _, vs := range []*cmttypes.VoteSet{rs.Votes.Prevotes(round), rs.Votes.Precommits(round)} {
			checkVoteSet(t, n.chainID, vs, rs.Validators)
		}
	}
	if rs.LastCommit != nil {
		checkVoteSet(t, n.chainID, rs.LastCommit, rs.LastValidators)
	}
}

func checkVoteSet(t *testing.T, chainID string, vs *cmttypes.VoteSet, vals *cmttypes.ValidatorSet) {
	for i := int32(0); vs != nil && i < int32(vals.Size()); i++ {
		v := vs.GetByIndex(i)
		if v == nil {
			continue
		}
		_, val := vals.GetByIndex(i)
		require.NoError(t, v.Verify(chainID, val.PubKey), "vote set holds a vote that does not verify")
		require.Equal(t, vs.GetHeight(), v.Height)
		require.Equal(t, vs.GetRound(), v.Round)
	}
}

// fuzzOps encodes harness operations in the byte format the fuzz targets
// read, so seeds stay in sync with the fuzzBytes reader. Each method mirrors
// the reads of the corresponding fuzzNode builder.
type fuzzOps []byte

// fuzzRound selects a round relative to the current one, as fuzzBytes.round
// reads it: a skip byte followed by an index byte.
type fuzzRound [2]byte

var fuzzRoundCur = fuzzRound{0, 5}

// fuzzRoundSkip selects the current round plus k (k < fuzzMaxRounds).
func fuzzRoundSkip(k byte) fuzzRound { return fuzzRound{k, 1} }

const (
	fuzzHeightCur   = 5    // fuzzBytes.height index of the current height
	fuzzBlockNil    = 0xff // fuzzOps block argument for a nil block ID
	fuzzSignerFixed = -1   // fuzzOps proposal signer: keep the harness choice
)

// blockID encodes fuzzNode.blockID choosing harness block variant b (or nil).
func (o fuzzOps) blockID(b byte) fuzzOps {
	if b == fuzzBlockNil {
		return append(o, 0)
	}
	return append(o, b+1)
}

// proposal encodes a proposal at the current height for harness block b with
// POL round -1 and no mutation. signer overrides the harness's proposer
// choice unless it is fuzzSignerFixed.
func (o fuzzOps) proposal(rnd fuzzRound, signer int, b byte) fuzzOps {
	o = append(o, 0, fuzzHeightCur, rnd[0], rnd[1], 0, 0) // kind, height, round, POL (-1)
	if signer == fuzzSignerFixed {
		o = append(o, 1)
	} else {
		o = append(o, 0, byte(signer))
	}
	return append(o.blockID(b), 4) // no mutation
}

// part encodes block part idx of harness block b at the current height and
// round, unmutated.
func (o fuzzOps) part(b, idx byte) fuzzOps {
	return append(o, 1, b, idx, 7, fuzzHeightCur, fuzzRoundCur[0], fuzzRoundCur[1])
}

// vote encodes an unmutated vote of type typ at the current height.
func (o fuzzOps) vote(signer byte, typ cmtproto.SignedMsgType, rnd fuzzRound, b byte) fuzzOps {
	sel := map[cmtproto.SignedMsgType]byte{cmtproto.PrevoteType: 0, cmtproto.PrecommitType: 2}[typ]
	o = append(o, 2, signer, 0, sel, fuzzHeightCur, rnd[0], rnd[1])
	return append(o.blockID(b), 7) // no mutation
}

// votes encodes a vote from every validator index; the node's own index is
// remapped to the byzantine validator by the harness.
func (o fuzzOps) votes(typ cmtproto.SignedMsgType, rnd fuzzRound, b byte) fuzzOps {
	for s := byte(0); s < fuzzNumVals; s++ {
		o = o.vote(s, typ, rnd, b)
	}
	return o
}

func (o fuzzOps) timeout() fuzzOps { return append(o, 3) }

// fuzzSeedHappy commits harness block 0 at height 1: the genesis proposer's
// proposal and its only part, prevotes and precommits from every validator,
// then the commit timeout that starts height 2.
func fuzzSeedHappy() []byte {
	return fuzzOps{}.
		proposal(fuzzRoundCur, fuzzSignerFixed, 0).
		part(0, 0).
		votes(cmtproto.PrevoteType, fuzzRoundCur, 0).
		votes(cmtproto.PrecommitType, fuzzRoundCur, 0).
		timeout()
}

// fuzzSeedRound17 reaches round 17 of height 1, beyond fuzzMaxRounds, via
// nil precommit quorums and precommit timeouts, then offers a proposal for
// harness block 0 signed by each validator so the live proposer's is
// accepted (or the node proposes itself).
func fuzzSeedRound17() []byte {
	o := fuzzOps{}.
		votes(cmtproto.PrecommitType, fuzzRoundSkip(15), fuzzBlockNil).timeout(). // round 15 -> 16
		votes(cmtproto.PrecommitType, fuzzRoundCur, fuzzBlockNil).timeout()       // round 16 -> 17
	for s := 0; s < fuzzNumVals; s++ {
		o = o.proposal(fuzzRoundCur, s, 0)
	}
	return o.part(0, 0)
}

func fuzzSeeds(f *testing.F) {
	f.Add([]byte{})
	f.Add(fuzzSeedHappy())
	f.Add(fuzzSeedRound17())
	f.Add(bytes.Repeat([]byte{1, 3, 7, 0, 1, 5, 5}, 6))
}

// FuzzConsensusStateHandleMsg drives proposals, block parts and votes through
// State.handleMsg (defaultSetProposal, addProposalBlockPart, tryAddVote). Two
// controlled validators behave honestly, one is byzantine, and any message
// may be mutated after signing. The node must only move forward, keep only
// verifiable votes and proposals, and commit blocks backed by +2/3 precommits.
func FuzzConsensusStateHandleMsg(f *testing.F) {
	genDoc, pvs := fuzzGenesis(f)
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInput {
			return
		}
		runStateHandleMsg(t, genDoc, pvs, data)
	})
}

// runStateHandleMsg is the body of FuzzConsensusStateHandleMsg; it returns
// the node so deterministic tests can inspect where an input leads.
func runStateHandleMsg(t *testing.T, genDoc *cmttypes.GenesisDoc, pvs map[string]cmttypes.PrivValidator, data []byte) *fuzzNode {
	n := newFuzzNode(t, genDoc, pvs)
	for i := int32(0); i < fuzzNumVals; i++ {
		n.honest[i] = i != n.byz && i != n.ours
	}
	r := &fuzzBytes{b: data}
	prev := n.cs.GetRoundState()
	vals := n.cs.GetState().Validators
	for op := 0; op < fuzzMaxOps && len(r.b) > 0; op++ {
		var msg Message
		switch r.u8() % 4 {
		case 0:
			msg = &ProposalMessage{Proposal: n.proposal(r)}
		case 1:
			msg = n.part(r)
		case 2:
			msg = &VoteMessage{Vote: n.vote(r)}
		default:
			n.fireTimeout()
		}
		if msg != nil {
			// Only messages that survive the reactor's decode and
			// ValidateBasic reach the state machine.
			if pb := wire(msg); pb != nil {
				if decoded, err := MsgFromProto(pb); err == nil && decoded.ValidateBasic() == nil {
					n.cs.handleMsg(msgInfo{decoded, "peer"})
					n.drain()
				}
			}
		}
		for len(n.cs.statsMsgQueue) > 0 {
			<-n.cs.statsMsgQueue
		}
		n.checkRoundState(t)

		rs := n.cs.GetRoundState()
		require.GreaterOrEqual(t, CompareHRS(rs.Height, rs.Round, rs.Step, prev.Height, prev.Round, prev.Step), 0,
			"round state moved backwards from %d/%d/%v to %d/%d/%v", prev.Height, prev.Round, prev.Step, rs.Height, rs.Round, rs.Step)
		require.LessOrEqual(t, rs.Height, prev.Height+1)
		if rs.Height > prev.Height {
			meta := n.cs.blockStore.LoadBlockMeta(prev.Height)
			require.NotNil(t, meta, "advanced height without storing block %d", prev.Height)
			seen := n.cs.blockStore.LoadSeenCommit(prev.Height)
			require.NotNil(t, seen)
			require.NoError(t, vals.VerifyCommit(n.chainID, meta.BlockID, prev.Height, seen),
				"committed block without +2/3 precommits")
		}
		prev = rs
	}
	return n
}

// TestFuzzSeedHappyPathCommits guards the happy-path seed against drifting
// out of sync with the fuzzBytes reader: replaying it must commit height 1.
func TestFuzzSeedHappyPathCommits(t *testing.T) {
	genDoc, pvs := fuzzGenesis(t)
	n := runStateHandleMsg(t, genDoc, pvs, fuzzSeedHappy())
	meta := n.cs.blockStore.LoadBlockMeta(1)
	require.NotNil(t, meta, "happy-path seed did not commit height 1")
	blk := n.blocks["1/0"]
	require.NotNil(t, blk, "seed never referenced harness block 0 at height 1")
	require.Equal(t, blk.id, meta.BlockID, "committed an unexpected block")
	require.Equal(t, int64(2), n.cs.GetRoundState().Height)
}

// TestFuzzSeedRound17AcceptsProposal is a regression test: past
// fuzzMaxRounds the accepted proposal must be checked against the live
// proposer, not the harness's fallback signer.
func TestFuzzSeedRound17AcceptsProposal(t *testing.T) {
	genDoc, pvs := fuzzGenesis(t)
	n := runStateHandleMsg(t, genDoc, pvs, fuzzSeedRound17())
	rs := n.cs.GetRoundState()
	require.Equal(t, int64(1), rs.Height)
	require.Equal(t, int32(fuzzMaxRounds+1), rs.Round)
	require.NotNil(t, rs.Proposal, "no proposal accepted at round %d", rs.Round)
	proposer, _ := rs.Validators.GetByAddress(rs.Validators.GetProposer().Address)
	require.NotEqual(t, n.proposerFor(rs.Round), proposer,
		"seed no longer exercises a live proposer that differs from the harness fallback")
}

// FuzzConsensusReactorReceive drives Reactor.receive (the body of Receive,
// minus StopPeerForError) on all four channels for
// one peer, with every consensus message type built from fuzzed fields (or
// raw bytes). Only one validator (< 1/3 power) is byzantine and the others
// stay silent, so the node may at most prevote at height 1, round 0. Peer
// state bit arrays are then consumed the way the gossip routines do.
func FuzzConsensusReactorReceive(f *testing.F) {
	genDoc, pvs := fuzzGenesis(f)
	fuzzSeeds(f)
	f.Add([]byte{4, 0, 5, 5, 1, 5, 4, 1, 5, 5, 4, 7, 5, 5, 3, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInput {
			return
		}
		n := newFuzzNode(t, genDoc, pvs)
		n.forge = true
		conR := NewReactor(n.cs, propagation.NewNoOpPropagator(), true)
		conR.SetLogger(log.NewNopLogger())
		conR.SetSwitch(p2p.NewSwitch(cmtcfg.DefaultP2PConfig(), nil))
		require.NoError(t, conR.Start())
		conR.mtx.Lock()
		conR.waitSync = false // accept data and votes without starting the State
		conR.mtx.Unlock()
		t.Cleanup(func() {
			conR.mtx.Lock()
			conR.waitSync = true // the State was never started, so do not wait on it
			conR.mtx.Unlock()
			require.NoError(t, conR.Stop())
		})
		peer := p2pmock.NewPeer(nil)
		_, err := conR.InitPeer(peer)
		require.NoError(t, err)
		ps := peer.Get(cmttypes.PeerStateKey).(*PeerState)

		r := &fuzzBytes{b: data}
		for op := 0; op < fuzzMaxOps && len(r.b) > 0; op++ {
			rs := n.cs.GetRoundState()
			var msg Message
			var pb proto.Message
			kind := r.u8() % 11
			switch kind {
			case 0:
				msg = &ProposalMessage{Proposal: n.proposal(r)}
			case 1:
				msg = n.part(r)
			case 2:
				msg = &VoteMessage{Vote: n.vote(r)}
			case 4:
				msg = &NewRoundStepMessage{
					Height: r.height(rs.Height), Round: r.round(rs.Round),
					Step: cstypes.RoundStepType(r.u8() % 10), SecondsSinceStartTime: int64(r.u16()), LastCommitRound: r.round(-1),
				}
			case 5:
				ba := r.bitArray()
				psh := r.partSetHeader()
				if r.u8()%2 == 0 {
					psh.Total = uint32(ba.Bits)
				}
				pb = &cmtcons.NewValidBlock{
					Height: r.height(rs.Height), Round: r.round(rs.Round),
					BlockPartSetHeader: psh.ToProto(), BlockParts: &ba, IsCommit: r.u8()%2 == 0,
				}
			case 6:
				msg = &HasVoteMessage{Height: r.height(rs.Height), Round: r.round(rs.Round), Type: r.voteType(), Index: int32(int8(r.u8()))}
			case 7:
				msg = &VoteSetMaj23Message{Height: r.height(rs.Height), Round: r.round(rs.Round), Type: r.voteType(), BlockID: n.blockID(r, false)}
			case 8:
				h, rnd, typ, id := r.height(rs.Height), r.round(rs.Round), r.voteType(), n.blockID(r, false)
				pb = &cmtcons.VoteSetBits{Height: h, Round: rnd, Type: typ, BlockID: id.ToProto(), Votes: r.bitArray()}
			case 9:
				pb = &cmtcons.ProposalPOL{Height: r.height(rs.Height), ProposalPolRound: r.round(rs.Round), ProposalPol: r.bitArray()}
			} // kinds 3 and 10: raw envelope bytes
			if kind == 10 || kind == 3 {
				var env cmtcons.Message
				raw := r.bytes(int(r.u8()))
				if proto.Unmarshal(raw, &env) == nil {
					pb, _ = env.Unwrap()
				}
			} else if msg != nil {
				pb = wire(msg)
			} else {
				pb = wireProto(pb)
			}
			if pb == nil {
				continue
			}
			chID := map[byte]byte{0: StateChannel, 1: DataChannel, 2: VoteChannel, 3: VoteSetBitsChannel}[r.u8()%5]
			if r.u8()%4 != 0 {
				chID = channelFor(pb)
			}
			decoded, decErr := MsgFromProto(pb)
			err := conR.receive(p2p.Envelope{Src: peer, ChannelID: chID, Message: pb})
			if decErr != nil || decoded.ValidateBasic() != nil {
				require.Error(t, err, "invalid message was not rejected")
			}
			n.drain()
			n.checkRoundState(t)
			checkPeerState(t, ps, n.cs.GetRoundState())

			rs = n.cs.GetRoundState()
			require.Equal(t, int64(1), rs.Height)
			require.Equal(t, int32(0), rs.Round)
			require.Contains(t, []cstypes.RoundStepType{cstypes.RoundStepPropose, cstypes.RoundStepPrevote}, rs.Step)
			if rs.Step == cstypes.RoundStepPrevote {
				require.NotNil(t, rs.ProposalBlock, "prevote step without a complete proposal")
			}
		}
	})
}

func channelFor(pb proto.Message) byte {
	switch pb.(type) {
	case *cmtcons.Proposal, *cmtcons.ProposalPOL, *cmtcons.BlockPart:
		return DataChannel
	case *cmtcons.Vote:
		return VoteChannel
	case *cmtcons.VoteSetBits:
		return VoteSetBitsChannel
	default:
		return StateChannel
	}
}

// checkPeerState asserts peer-state consistency and exercises the bit array
// operations the gossip routines run against it.
func checkPeerState(t *testing.T, ps *PeerState, rs *cstypes.RoundState) {
	prs := ps.GetRoundState()
	if prs.ProposalBlockParts != nil {
		require.Equal(t, int(prs.ProposalBlockPartSetHeader.Total), prs.ProposalBlockParts.Size())
		if rs.ProposalBlockParts != nil && rs.ProposalBlockParts.HasHeader(prs.ProposalBlockPartSetHeader) {
			if idx, ok := rs.ProposalBlockParts.BitArray().Sub(prs.ProposalBlockParts.Copy()).PickRandom(); ok {
				require.NotNil(t, rs.ProposalBlockParts.GetPart(idx))
			}
		}
	}
	for _, vs := range []cmttypes.VoteSetReader{rs.Votes.Prevotes(rs.Round), rs.Votes.Precommits(rs.Round)} {
		if vote, ok := ps.PickVoteToSend(vs); ok {
			require.NotNil(t, vote)
		}
	}
	if pol := rs.Votes.Prevotes(prs.ProposalPOLRound); pol != nil {
		if vote, ok := ps.PickVoteToSend(pol); ok {
			require.NotNil(t, vote)
		}
	}
	_, err := ps.MarshalJSON()
	require.NoError(t, err)
}
