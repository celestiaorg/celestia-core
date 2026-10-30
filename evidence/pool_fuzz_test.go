package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/light"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

// The fuzz chain has blocks 1..fzHeight signed by four validators with
// distinct powers. Evidence older than fzMinHeight is expired, and the latest
// block has no stored commit, so light client attacks need height < fzHeight.
const (
	fzChainID   = "evidence-fuzz"
	fzHeight    = 10
	fzMaxAge    = 4
	fzMinHeight = fzHeight - fzMaxAge
	fzMaxEvs    = 4
)

var fzBaseTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func fzBlockTime(h int64) time.Time { return fzBaseTime.Add(time.Duration(h) * time.Minute) }

type evFixture struct {
	privVals   []types.PrivValidator // ordered like valSet.Validators
	phantom    types.PrivValidator
	valSet     *types.ValidatorSet
	stateStore sm.Store
	blockStore *fzBlockStore
}

var (
	evFixtureOnce sync.Once
	evFixtureVal  *evFixture
)

// fuzzEvidenceFixture builds deterministic, read-only state and block stores
// shared by all iterations. Pools never write to them.
func fuzzEvidenceFixture(tb testing.TB) *evFixture {
	evFixtureOnce.Do(func() {
		fx := &evFixture{phantom: types.NewMockPVWithParams(ed25519.GenPrivKeyFromSecret([]byte("phantom")), false, false)}
		byAddr := map[string]types.PrivValidator{}
		vals := make([]*types.Validator, 4)
		for i := range vals {
			pv := types.NewMockPVWithParams(ed25519.GenPrivKeyFromSecret([]byte{byte(i)}), false, false)
			vals[i] = types.NewValidator(pv.PrivKey.PubKey(), int64(10*(i+1)))
			byAddr[string(vals[i].Address)] = pv
		}
		fx.valSet = types.NewValidatorSet(vals)
		for _, v := range fx.valSet.Validators {
			fx.privVals = append(fx.privVals, byAddr[string(v.Address)])
		}
		params := *types.DefaultConsensusParams()
		params.Evidence = types.EvidenceParams{MaxAgeNumBlocks: fzMaxAge, MaxAgeDuration: fzMaxAge * time.Minute, MaxBytes: 1 << 20}
		state := sm.State{
			ChainID: fzChainID, InitialHeight: 1, LastBlockTime: fzBlockTime(fzHeight),
			Validators: fx.valSet, NextValidators: fx.valSet, LastValidators: fx.valSet,
			LastHeightValidatorsChanged: 1, ConsensusParams: params,
		}
		fx.stateStore = sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{})
		for i := int64(0); i <= fzHeight; i++ {
			state.LastBlockHeight = i
			require.NoError(tb, fx.stateStore.Save(state))
		}
		fx.blockStore = &fzBlockStore{metas: map[int64]*types.BlockMeta{}, commits: map[int64]*types.Commit{}}
		lastCommit, lastID := &types.Commit{}, types.BlockID{}
		for h := int64(1); h <= fzHeight; h++ {
			block := types.MakeBlock(h, types.MakeData(test.MakeNTxs(h, 1)), lastCommit, nil)
			block.Version = cmtversion.Consensus{Block: version.BlockProtocol, App: 1}
			block.ChainID, block.Time, block.LastBlockID = fzChainID, fzBlockTime(h), lastID
			block.ValidatorsHash, block.NextValidatorsHash = fx.valSet.Hash(), fx.valSet.Hash()
			block.ConsensusHash, block.AppHash = params.Hash(), fzHash("app", h)
			block.LastResultsHash, block.ProposerAddress = fzHash("results", h), fx.valSet.Proposer.Address
			parts, err := block.MakePartSet(types.BlockPartSizeBytes)
			require.NoError(tb, err)
			lastID = types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
			lastCommit, err = test.MakeCommit(lastID, h, 0, fx.valSet, fx.privVals, fzChainID, fzBlockTime(h))
			require.NoError(tb, err)
			fx.blockStore.metas[h] = types.NewBlockMeta(block, parts)
			if h > 1 { // like store.BlockStore, a commit comes from the next block
				fx.blockStore.commits[h-1] = block.LastCommit
			}
		}
		evFixtureVal = fx
	})
	return evFixtureVal
}

// fzBlockStore is an in-memory BlockStore (store.BlockStore would be an
// import cycle). It only has commits up to fzHeight-1, like a real store.
type fzBlockStore struct {
	metas   map[int64]*types.BlockMeta
	commits map[int64]*types.Commit
}

func (s *fzBlockStore) LoadBlockMeta(h int64) *types.BlockMeta { return s.metas[h] }
func (s *fzBlockStore) Height() int64                          { return fzHeight }
func (s *fzBlockStore) LoadBlockCommit(h int64) *types.Commit {
	if c, ok := s.commits[h]; ok {
		return c.Clone()
	}
	return nil
}

func fzHash(tag string, h int64) []byte {
	s := sha256.Sum256(binary.BigEndian.AppendUint64([]byte(tag), uint64(h)))
	return s[:]
}

func (fx *evFixture) newPool(tb testing.TB) *Pool {
	pool, err := NewPool(dbm.NewMemDB(), fx.stateStore, fx.blockStore)
	require.NoError(tb, err)
	return pool
}

// fzReader hands out fuzz bytes; reading past the end yields zeros, so an
// empty input decodes to well-formed, valid evidence.
type fzReader struct {
	t testing.TB
	b []byte
}

func (r *fzReader) byte() byte {
	if len(r.b) == 0 {
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *fzReader) intn(n int) int { return int(r.byte()) % n }

func (r *fzReader) uint64() uint64 {
	var buf [8]byte
	for i := range buf {
		buf[i] = r.byte()
	}
	return binary.BigEndian.Uint64(buf[:])
}

// hash returns 32 bytes: a default derived from tag when the fuzz byte is
// zero, otherwise bytes taken from the input.
func (r *fzReader) hash(tag string, h int64) []byte {
	if r.byte() == 0 {
		return fzHash(tag, h)
	}
	out := make([]byte, 32)
	for i := range out {
		out[i] = r.byte()
	}
	return out
}

// fzCase is a piece of generated evidence and whether the oracle expects the
// pool to accept it.
type fzCase struct {
	ev      types.Evidence
	valid   bool
	decoded bool // false if EvidenceFromProto (incl. ValidateBasic) rejects it
}

func (fx *evFixture) dupVoteEvidence(r *fzReader) fzCase {
	idx := r.intn(len(fx.privVals))
	val := fx.valSet.Validators[idx]
	mut := r.byte()
	if mut&0x80 == 0 { // most inputs stay structurally valid
		mut = 0
	}
	h := int64(fzMinHeight + r.intn(fzHeight-fzMinHeight+1))
	valid := true
	switch r.intn(8) {
	case 1: // expired
		h, valid = int64(1+r.intn(fzMinHeight-1)), false
	case 2: // no such block
		h, valid = fzHeight+1+int64(r.uint64()>>2), false
	}
	round := int32(r.intn(4))
	typ := cmtproto.PrevoteType
	if r.byte()&1 == 1 {
		typ = cmtproto.PrecommitType
	}
	ids := []types.BlockID{
		{Hash: r.hash("a", h), PartSetHeader: types.PartSetHeader{Total: 1 + uint32(r.byte()), Hash: fzHash("pa", h)}},
		{Hash: r.hash("b", h), PartSetHeader: types.PartSetHeader{Total: 1, Hash: fzHash("pb", h)}},
	}
	if mut&0x01 != 0 || ids[0].Equals(ids[1]) {
		ids[1], valid = ids[0], false
	}
	if ids[0].Key() > ids[1].Key() {
		ids[0], ids[1] = ids[1], ids[0]
	}
	if mut&0x02 != 0 { // unsorted votes fail ValidateBasic
		ids[0], ids[1], valid = ids[1], ids[0], false
	}
	votes := make([]*types.Vote, 2)
	for i := range votes {
		signer, chainID, vh, vr, vt := fx.privVals[idx], fzChainID, h, round, typ
		if i == 1 {
			switch {
			case mut&0x04 != 0: // other validator's key
				signer, valid = fx.privVals[(idx+1)%len(fx.privVals)], false
			case mut&0x08 != 0:
				chainID, valid = "other-chain", false
			case mut&0x10 != 0:
				vr, valid = vr+1, false
			case mut&0x20 != 0:
				vt, valid = cmtproto.PrevoteType+cmtproto.PrecommitType-vt, false
			}
		}
		v := &types.Vote{
			Type: vt, Height: vh, Round: vr, BlockID: ids[i], Timestamp: fzBaseTime.Add(time.Duration(r.byte())),
			ValidatorAddress: val.Address, ValidatorIndex: int32(idx),
		}
		pb := v.ToProto()
		require.NoError(r.t, signer.SignVote(chainID, pb))
		v.Signature = pb.Signature
		if i == 1 && mut&0x40 != 0 {
			v.Signature[r.intn(len(v.Signature))] ^= 1 + r.byte()%255
			valid = false
		}
		votes[i] = v
	}
	ev := &types.DuplicateVoteEvidence{
		VoteA: votes[0], VoteB: votes[1], Timestamp: fzBlockTime(h),
		ValidatorPower: val.VotingPower, TotalVotingPower: fx.valSet.TotalVotingPower(),
	}
	switch r.intn(16) {
	case 1:
		ev.Timestamp, valid = ev.Timestamp.Add(time.Duration(r.byte())+1), false
	case 2:
		ev.ValidatorPower, valid = ev.ValidatorPower+1+int64(r.byte()), false
	case 3:
		ev.TotalVotingPower, valid = ev.TotalVotingPower-1-int64(r.byte()%50), false
	}
	return fzCase{ev: ev, valid: valid}
}

func (fx *evFixture) lightAttackEvidence(r *fzReader) fzCase {
	valid := true
	common := int64(fzMinHeight + r.intn(fzHeight-fzMinHeight))
	height := common
	lunatic := r.byte()&1 == 1
	if lunatic {
		if common == fzHeight-1 {
			common--
		}
		height = common + 1 + int64(r.intn(int(fzHeight-1-common)))
		if r.intn(8) == 1 { // forward lunatic: latest block has no commit
			height, valid = fzHeight+1+int64(r.byte()), false
		}
	}
	trustedH := height
	if trustedH > fzHeight {
		trustedH = fzHeight
	}
	trusted := fx.blockStore.LoadBlockMeta(trustedH).Header
	header := trusted
	header.Height = height
	confVals, confPrivs := fx.valSet, fx.privVals
	if lunatic {
		header.AppHash = r.hash("evil", height)
		if bytes.Equal(header.AppHash, trusted.AppHash) {
			header.AppHash[0] ^= 1
		}
		if r.byte()&1 == 1 { // add a validator unknown to the chain
			confVals = fx.valSet.Copy()
			pk, _ := fx.phantom.GetPubKey()
			require.NoError(r.t, confVals.UpdateWithChangeSet([]*types.Validator{types.NewValidator(pk, 1+int64(r.byte()))}))
			confPrivs = append(append([]types.PrivValidator{}, fx.privVals...), fx.phantom)
		}
		header.ValidatorsHash = confVals.Hash()
	} else {
		header.DataHash = r.hash("data", height)
		if bytes.Equal(header.DataHash, trusted.DataHash) {
			valid = false // identical to the trusted header
		}
	}
	round := int32(0)
	if !lunatic && r.intn(8) == 1 {
		// Amnesia: no byzantine validators can be attributed; the decoded
		// empty list is not nil, so such evidence is always rejected.
		round, valid = 1+int32(r.byte()%3), false
	}
	signMask := r.byte()
	var signers []types.PrivValidator
	var confPower, commonPower int64
	var byz []*types.Validator
	for _, pv := range confPrivs {
		if signMask&1 == 0 { // default: everyone signs
			pk, _ := pv.GetPubKey()
			_, cv := confVals.GetByAddress(pk.Address())
			signers, confPower = append(signers, pv), confPower+cv.VotingPower
			if _, v := fx.valSet.GetByAddress(pk.Address()); v != nil {
				commonPower += v.VotingPower
				byz = append(byz, v.Copy())
			}
		}
		signMask >>= 1
	}
	if confPower <= confVals.TotalVotingPower()*2/3 {
		valid = false
	}
	if lunatic && commonPower <= fx.valSet.TotalVotingPower()/3 {
		valid = false
	}
	sort.Sort(types.ValidatorsByVotingPower(byz))
	mut := r.byte()
	if mut&0x80 == 0 {
		mut = 0
	}
	chainID := fzChainID
	if mut&0x01 != 0 {
		chainID, valid = "other-chain", false
	}
	blockID := types.BlockID{Hash: header.Hash(), PartSetHeader: types.PartSetHeader{Total: 1, Hash: fzHash("cp", height)}}
	commit, err := test.MakeCommit(blockID, height, round, confVals, signers, chainID, header.Time)
	require.NoError(r.t, err)
	if mut&0x02 != 0 && len(signers) > 0 {
		for i := range commit.Signatures {
			if commit.Signatures[i].BlockIDFlag == types.BlockIDFlagCommit {
				commit.Signatures[i].Signature[0] ^= 1
				break
			}
		}
		valid = false
	}
	if round != 0 {
		byz = nil
	}
	switch {
	case mut&0x04 != 0: // claim an extra byzantine validator
		byz, valid = append(byz, fx.valSet.Validators[r.intn(fx.valSet.Size())].Copy()), false
	case mut&0x08 != 0 && len(byz) > 0: // pubkey does not match address
		byz[0].PubKey, valid = fx.valSet.Validators[(r.intn(3)+1+indexOf(fx.valSet, byz[0]))%4].PubKey, false
	case mut&0x10 != 0 && len(byz) > 0:
		byz[len(byz)-1].VotingPower, valid = byz[len(byz)-1].VotingPower+1, false
	}
	ev := &types.LightClientAttackEvidence{
		ConflictingBlock: &types.LightBlock{
			SignedHeader: &types.SignedHeader{Header: &header, Commit: commit},
			ValidatorSet: confVals,
		},
		CommonHeight: common, ByzantineValidators: byz,
		TotalVotingPower: fx.valSet.TotalVotingPower(), Timestamp: fzBlockTime(common),
	}
	if mut&0x20 != 0 {
		ev.TotalVotingPower, valid = ev.TotalVotingPower+1, false
	}
	if mut&0x40 != 0 {
		ev.Timestamp, valid = ev.Timestamp.Add(time.Second), false
	}
	return fzCase{ev: ev, valid: valid}
}

func indexOf(vals *types.ValidatorSet, v *types.Validator) int {
	idx, _ := vals.GetByAddress(v.Address)
	return int(idx)
}

// generate decodes up to fzMaxEvs pieces of evidence and round-trips each
// through protobuf, as the reactor and block decoder would.
func (fx *evFixture) generate(t testing.TB, data []byte) []fzCase {
	r := &fzReader{t: t, b: data}
	n := 1 + r.intn(fzMaxEvs)
	cases := make([]fzCase, 0, n)
	for i := 0; i < n; i++ {
		var c fzCase
		switch k := r.intn(4); {
		case k == 3 && i > 0: // repeat an earlier piece of evidence
			c = cases[r.intn(i)]
		case k%2 == 0:
			c = fx.dupVoteEvidence(r)
		default:
			c = fx.lightAttackEvidence(r)
		}
		pb, err := types.EvidenceToProto(c.ev)
		require.NoError(t, err)
		bz, err := pb.Marshal()
		require.NoError(t, err)
		var decoded cmtproto.Evidence
		require.NoError(t, decoded.Unmarshal(bz))
		if ev, err := types.EvidenceFromProto(&decoded); err == nil {
			c.ev, c.decoded = ev, true
		} else {
			require.False(t, c.valid, "valid evidence failed to decode: %v", err)
		}
		cases = append(cases, c)
	}
	return cases
}

// requireSignedByValidators independently checks that pending evidence can
// only implicate validators who actually signed conflicting data.
func (fx *evFixture) requireSignedByValidators(t *testing.T, ev types.Evidence) {
	switch ev := ev.(type) {
	case *types.DuplicateVoteEvidence:
		_, val := fx.valSet.GetByAddress(ev.VoteA.ValidatorAddress)
		require.NotNil(t, val)
		require.False(t, ev.VoteA.BlockID.Equals(ev.VoteB.BlockID))
		for _, v := range []*types.Vote{ev.VoteA, ev.VoteB} {
			require.Equal(t, val.Address, v.ValidatorAddress)
			require.True(t, val.PubKey.VerifySignature(types.VoteSignBytes(fzChainID, v.ToProto()), v.Signature))
		}
	case *types.LightClientAttackEvidence:
		cb := ev.ConflictingBlock
		require.NoError(t, fx.valSet.VerifyCommitLightTrustingAllSignatures(fzChainID, cb.Commit,
			light.DefaultTrustLevel))
		require.Equal(t, cb.Hash(), cb.Commit.BlockID.Hash)
		for _, bv := range ev.ByzantineValidators {
			_, val := fx.valSet.GetByAddress(bv.Address)
			require.NotNil(t, val)
			require.True(t, bv.PubKey.Equals(val.PubKey))
		}
	default:
		t.Fatalf("unexpected evidence type %T", ev)
	}
}

type evKey struct {
	height int64
	hash   string
}

func keyOf(ev types.Evidence) evKey { return evKey{ev.Height(), string(ev.Hash())} }

// requirePending checks that the pool holds exactly the expected evidence and
// that each item is independently verifiable.
func (fx *evFixture) requirePending(t *testing.T, pool *Pool, want map[evKey]bool) {
	pending, _ := pool.PendingEvidence(-1)
	require.Len(t, pending, len(want))
	require.EqualValues(t, len(want), pool.Size())
	require.LessOrEqual(t, pool.Size(), uint32(fzMaxEvs))
	for _, ev := range pending {
		require.True(t, want[keyOf(ev)], "unexpected pending evidence %v", ev)
		fx.requireSignedByValidators(t, ev)
	}
}

// nopTransport lets the real Switch stop peers without a network.
type nopTransport struct{ p2p.Transport }

func (nopTransport) Cleanup(p2p.Peer) {}

func newFuzzReactor(t *testing.T, pool *Pool) (*Reactor, *mock.Peer) {
	r := NewReactor(pool)
	r.SetLogger(log.NewNopLogger())
	sw := p2p.NewSwitch(cmtcfg.DefaultP2PConfig(), nopTransport{})
	sw.SetLogger(log.NewNopLogger())
	r.SetSwitch(sw)
	peer := mock.NewPeer(nil)
	t.Cleanup(func() {
		_ = peer.Stop()
		r.OnStop() // cancels the base reactor's processor goroutine
	})
	return r, peer
}

// FuzzEvidencePool generates duplicate-vote and light-client-attack evidence
// signed by the fixture validators (valid, or with a mutation the pool must
// reject) and feeds it to the evidence reactor, AddEvidence and CheckEvidence.
func FuzzEvidencePool(f *testing.F) {
	fx := fuzzEvidenceFixture(f)
	f.Add([]byte{})                             // one valid duplicate vote
	f.Add([]byte{0, 1})                         // valid equivocation attack
	f.Add([]byte{0, 1, 0, 1})                   // valid lunatic attack
	f.Add([]byte{0, 1, 0, 1, 0, 0, 0, 1})       // lunatic attack with a phantom validator
	f.Add([]byte{0, 1, 0, 0, 0, 0, 0x03})       // equivocation with too few signers
	f.Add([]byte{0, 1, 0, 0, 0, 0, 0, 0x84})    // extra byzantine validator claimed
	f.Add([]byte{0, 0, 0, 0x84})                // duplicate vote signed by another key
	f.Add([]byte{3, 0, 0, 0x81, 0, 3, 0})       // undecodable duplicate vote, then a repeat
	f.Add([]byte{2, 0, 1, 0x80, 0, 0, 0, 0, 3}) // valid duplicate votes and a repeat
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		runEvidencePoolFuzz(t, fx, data)
	})
}

// TestFuzzEvidencePoolTimestampOverflow replays an input that shifts a
// duplicate vote evidence timestamp by 1+255 ns. Computed in byte arithmetic
// that wrapped to 0, leaving valid evidence the harness expected to be rejected.
func TestFuzzEvidencePoolTimestampOverflow(t *testing.T) {
	runEvidencePoolFuzz(t, fuzzEvidenceFixture(t), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 255})
}

// runEvidencePoolFuzz runs one FuzzEvidencePool iteration.
func runEvidencePoolFuzz(t *testing.T, fx *evFixture, data []byte) {
	cases := fx.generate(t, data)
	all := make(types.EvidenceList, len(cases))
	for i, c := range cases {
		all[i] = c.ev
	}

	// Reactor: one undecodable piece rejects the whole message.
	// Otherwise evidence is added in order until the first invalid
	// piece, which gets the peer stopped. Evidence already pending is
	// skipped without verification.
	pool := fx.newPool(t)
	reactor, peer := newFuzzReactor(t, pool)
	list, err := evidenceListToProto(all)
	require.NoError(t, err)
	reactor.Receive(p2p.Envelope{Src: peer, ChannelID: EvidenceChannel, Message: list})
	want, punished := map[evKey]bool{}, false
	var decoded []fzCase
	for _, c := range cases {
		if c.decoded {
			decoded = append(decoded, c)
		} else {
			punished = true
		}
	}
	for _, c := range cases {
		if punished {
			break
		}
		if want[keyOf(c.ev)] {
			continue
		}
		if !c.valid {
			punished = true
			break
		}
		want[keyOf(c.ev)] = true
	}
	require.Equal(t, punished, !peer.IsRunning(), "peer stopped for error mismatch")
	fx.requirePending(t, pool, want)

	// AddEvidence and CheckEvidence are only reached with evidence
	// that passed decoding and ValidateBasic.
	cases = decoded
	evs := make(types.EvidenceList, len(cases))
	for i, c := range cases {
		evs[i] = c.ev
	}

	// AddEvidence on a fresh pool reports each item independently.
	pool = fx.newPool(t)
	want = map[evKey]bool{}
	for _, c := range cases {
		err := pool.AddEvidence(c.ev)
		if want[keyOf(c.ev)] {
			require.NoError(t, err)
			continue
		}
		if c.valid {
			require.NoError(t, err)
			want[keyOf(c.ev)] = true
		} else {
			var invalid *types.ErrInvalidEvidence
			require.ErrorAs(t, err, &invalid)
		}
	}
	fx.requirePending(t, pool, want)

	// CheckEvidence treats the list as a proposed block's evidence.
	pool = fx.newPool(t)
	ok, seen := true, map[string]bool{}
	for _, c := range cases {
		ok = ok && c.valid && !seen[string(c.ev.Hash())]
		seen[string(c.ev.Hash())] = true
	}
	err = pool.CheckEvidence(evs)
	if !ok {
		require.Error(t, err)
		return
	}
	require.NoError(t, err)
	// Committing the block removes it from pending, after which the
	// same evidence is rejected in a block and ignored from peers.
	state := pool.State()
	state.LastBlockHeight++
	state.LastBlockTime = state.LastBlockTime.Add(time.Minute)
	pool.Update(state, evs)
	require.Zero(t, pool.Size())
	for _, ev := range evs {
		require.Error(t, pool.CheckEvidence(types.EvidenceList{ev}))
		require.NoError(t, pool.AddEvidence(ev))
	}
	require.Zero(t, pool.Size())
}

// FuzzEvidenceReactorReceive feeds raw EvidenceList bytes to the reactor.
// Undecodable evidence must stop the peer; anything that reaches the pending
// pool must be signed by the implicated validators.
func FuzzEvidenceReactorReceive(f *testing.F) {
	fx := fuzzEvidenceFixture(f)
	for _, seed := range [][]byte{{}, {0, 1}, {0, 1, 0, 1, 0, 0, 0, 1}, {0, 0, 0, 0x84}, {3, 0, 0, 0x81, 0, 3, 0}} {
		var evs types.EvidenceList
		for _, c := range fx.generate(f, seed) {
			evs = append(evs, c.ev)
		}
		list, err := evidenceListToProto(evs)
		require.NoError(f, err)
		bz, err := proto.Marshal(list)
		require.NoError(f, err)
		f.Add(bz)
	}
	f.Add([]byte{})
	f.Add([]byte{0x0a, 0x00})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxMsgSize {
			return
		}
		var list cmtproto.EvidenceList
		if err := proto.Unmarshal(raw, &list); err != nil {
			return // the p2p layer drops undecodable envelopes
		}
		pool := fx.newPool(t)
		reactor, peer := newFuzzReactor(t, pool)
		reactor.Receive(p2p.Envelope{Src: peer, ChannelID: EvidenceChannel, Message: &list})
		decoded, err := evidenceListFromProto(&list)
		if err != nil {
			require.False(t, peer.IsRunning(), "peer sent undecodable evidence")
			require.Zero(t, pool.Size())
			return
		}
		pending, _ := pool.PendingEvidence(-1)
		require.LessOrEqual(t, len(pending), len(decoded))
		require.EqualValues(t, len(pending), pool.Size())
		for _, ev := range pending {
			require.True(t, types.EvidenceList(decoded).Has(ev))
			fx.requireSignedByValidators(t, ev)
		}
	})
}
