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
	"github.com/cometbft/cometbft/internal/fuzzinput"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/light"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	cmttypes "github.com/cometbft/cometbft/types"
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
	privVals   []cmttypes.PrivValidator // ordered like valSet.Validators
	phantom    cmttypes.PrivValidator
	valSet     *cmttypes.ValidatorSet
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
		fx := &evFixture{phantom: cmttypes.NewMockPVWithParams(ed25519.GenPrivKeyFromSecret([]byte("phantom")), false, false)}
		byAddr := map[string]cmttypes.PrivValidator{}
		vals := make([]*cmttypes.Validator, 4)
		for i := range vals {
			pv := cmttypes.NewMockPVWithParams(ed25519.GenPrivKeyFromSecret([]byte{byte(i)}), false, false)
			vals[i] = cmttypes.NewValidator(pv.PrivKey.PubKey(), int64(10*(i+1)))
			byAddr[string(vals[i].Address)] = pv
		}
		fx.valSet = cmttypes.NewValidatorSet(vals)
		for _, v := range fx.valSet.Validators {
			fx.privVals = append(fx.privVals, byAddr[string(v.Address)])
		}
		params := *cmttypes.DefaultConsensusParams()
		params.Evidence = cmttypes.EvidenceParams{MaxAgeNumBlocks: fzMaxAge, MaxAgeDuration: fzMaxAge * time.Minute, MaxBytes: 1 << 20}
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
		fx.blockStore = &fzBlockStore{metas: map[int64]*cmttypes.BlockMeta{}, commits: map[int64]*cmttypes.Commit{}}
		lastCommit, lastID := &cmttypes.Commit{}, cmttypes.BlockID{}
		for h := int64(1); h <= fzHeight; h++ {
			block := cmttypes.MakeBlock(h, cmttypes.MakeData(test.MakeNTxs(h, 1)), lastCommit, nil)
			block.Version = cmtversion.Consensus{Block: version.BlockProtocol, App: 1}
			block.ChainID, block.Time, block.LastBlockID = fzChainID, fzBlockTime(h), lastID
			block.ValidatorsHash, block.NextValidatorsHash = fx.valSet.Hash(), fx.valSet.Hash()
			block.ConsensusHash, block.AppHash = params.Hash(), fzHash("app", h)
			block.LastResultsHash, block.ProposerAddress = fzHash("results", h), fx.valSet.Proposer.Address
			parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
			require.NoError(tb, err)
			lastID = cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
			lastCommit, err = test.MakeCommit(lastID, h, 0, fx.valSet, fx.privVals, fzChainID, fzBlockTime(h))
			require.NoError(tb, err)
			fx.blockStore.metas[h] = cmttypes.NewBlockMeta(block, parts)
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
	metas   map[int64]*cmttypes.BlockMeta
	commits map[int64]*cmttypes.Commit
}

func (s *fzBlockStore) LoadBlockMeta(h int64) *cmttypes.BlockMeta { return s.metas[h] }
func (s *fzBlockStore) Height() int64                             { return fzHeight }
func (s *fzBlockStore) LoadBlockCommit(h int64) *cmttypes.Commit {
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
	ev      cmttypes.Evidence
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
	ids := []cmttypes.BlockID{
		{Hash: r.hash("a", h), PartSetHeader: cmttypes.PartSetHeader{Total: 1 + uint32(r.byte()), Hash: fzHash("pa", h)}},
		{Hash: r.hash("b", h), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: fzHash("pb", h)}},
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
	votes := make([]*cmttypes.Vote, 2)
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
		v := &cmttypes.Vote{
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
	ev := &cmttypes.DuplicateVoteEvidence{
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
			require.NoError(r.t, confVals.UpdateWithChangeSet([]*cmttypes.Validator{cmttypes.NewValidator(pk, 1+int64(r.byte()))}))
			confPrivs = append(append([]cmttypes.PrivValidator{}, fx.privVals...), fx.phantom)
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
	var signers []cmttypes.PrivValidator
	var confPower, commonPower int64
	var byz []*cmttypes.Validator
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
	sort.Sort(cmttypes.ValidatorsByVotingPower(byz))
	mut := r.byte()
	if mut&0x80 == 0 {
		mut = 0
	}
	chainID := fzChainID
	if mut&0x01 != 0 {
		chainID, valid = "other-chain", false
	}
	blockID := cmttypes.BlockID{Hash: header.Hash(), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: fzHash("cp", height)}}
	commit, err := test.MakeCommit(blockID, height, round, confVals, signers, chainID, header.Time)
	require.NoError(r.t, err)
	if mut&0x02 != 0 && len(signers) > 0 {
		for i := range commit.Signatures {
			if commit.Signatures[i].BlockIDFlag == cmttypes.BlockIDFlagCommit {
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
	ev := &cmttypes.LightClientAttackEvidence{
		ConflictingBlock: &cmttypes.LightBlock{
			SignedHeader: &cmttypes.SignedHeader{Header: &header, Commit: commit},
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

func indexOf(vals *cmttypes.ValidatorSet, v *cmttypes.Validator) int {
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
		pb, err := cmttypes.EvidenceToProto(c.ev)
		require.NoError(t, err)
		bz, err := pb.Marshal()
		require.NoError(t, err)
		var decoded cmtproto.Evidence
		require.NoError(t, decoded.Unmarshal(bz))
		if ev, err := cmttypes.EvidenceFromProto(&decoded); err == nil {
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
func (fx *evFixture) requireSignedByValidators(t *testing.T, ev cmttypes.Evidence) {
	switch ev := ev.(type) {
	case *cmttypes.DuplicateVoteEvidence:
		_, val := fx.valSet.GetByAddress(ev.VoteA.ValidatorAddress)
		require.NotNil(t, val)
		require.False(t, ev.VoteA.BlockID.Equals(ev.VoteB.BlockID))
		for _, v := range []*cmttypes.Vote{ev.VoteA, ev.VoteB} {
			require.Equal(t, val.Address, v.ValidatorAddress)
			require.True(t, val.PubKey.VerifySignature(cmttypes.VoteSignBytes(fzChainID, v.ToProto()), v.Signature))
		}
	case *cmttypes.LightClientAttackEvidence:
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

func keyOf(ev cmttypes.Evidence) evKey { return evKey{ev.Height(), string(ev.Hash())} }

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
	all := make(cmttypes.EvidenceList, len(cases))
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
	evs := make(cmttypes.EvidenceList, len(cases))
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
			var invalid *cmttypes.ErrInvalidEvidence
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
		require.Error(t, pool.CheckEvidence(cmttypes.EvidenceList{ev}))
		require.NoError(t, pool.AddEvidence(ev))
	}
	require.Zero(t, pool.Size())
}

// maxFuzzEdits bounds the field edits applied to one structured message.
const maxFuzzEdits = 16

// evidenceEditSeeds are structured inputs for FuzzEvidenceReactorReceive: a
// length-prefixed fzReader input for generate, an edit count, then per edit a
// field selector and its value.
var evidenceEditSeeds = [][]byte{
	{0, 0},                                  // one valid duplicate vote, unedited
	{2, 0, 1, 0},                            // valid equivocation attack, unedited
	{8, 0, 1, 0, 1, 0, 0, 0, 1, 0},          // lunatic attack with a phantom validator, unedited
	{0, 1, 0, 0, 0, 0, 0, 0, 2},             // duplicate vote: vote B moved to height 2
	{0, 1, 0, 0, 2, 0, 0, 0, 7},             // duplicate vote: vote B moved to height 7, re-signed
	{0, 1, 0, 0, 3, 0, 3, 0},                // duplicate vote: vote B timestamp moved, re-signed
	{0, 1, 0, 0, 7},                         // duplicate vote: votes swapped
	{0, 1, 0, 0, 1, 0, 0},                   // duplicate vote: validator power changed
	{2, 0, 1, 1, 0, 0, 0, 0, 7},             // equivocation: common height 7
	{2, 0, 1, 2, 0, 0, 6, 0, 0, 5, 0, 0, 7}, // equivocation: app hash flipped, rehashed and re-signed
	{2, 0, 1, 1, 0, 0, 3, 1},                // equivocation: one byzantine validator dropped
	{2, 0, 1, 1, 0, 0, 8, 0, 0},             // equivocation: one commit signature cleared
	{0, 2, 1, 0, 2},                         // duplicate vote repeated, then moved first
	{0, 1, 3},                               // duplicate vote followed by empty evidence
}

// evReceiveResult is what FuzzEvidenceReactorReceive observed for one list.
type evReceiveResult struct {
	decodeErr error
	stopped   bool
	pending   int
}

// checkEvidenceReceive delivers list to a fresh reactor and replays the
// decoded evidence on an identical pool to decide whether the peer must be
// punished and what must end up pending.
func (fx *evFixture) checkEvidenceReceive(t *testing.T, list *cmtproto.EvidenceList) evReceiveResult {
	pool := fx.newPool(t)
	reactor, peer := newFuzzReactor(t, pool)
	reactor.Receive(p2p.Envelope{Src: peer, ChannelID: EvidenceChannel, Message: list})
	decoded, err := evidenceListFromProto(list)
	if err != nil {
		require.False(t, peer.IsRunning(), "peer sent undecodable evidence")
		require.Zero(t, pool.Size())
		return evReceiveResult{decodeErr: err, stopped: true}
	}
	// Replay the decoded list on an identical pool to decide, per item,
	// whether the reactor must punish the peer: the first evidence the
	// pool rejects as invalid stops the peer and ends processing, while
	// other errors (duplicates, committed evidence) are skipped.
	model := fx.newPool(t)
	wantStopped := false
	for _, ev := range decoded {
		if _, invalid := model.AddEvidence(ev).(*cmttypes.ErrInvalidEvidence); invalid {
			wantStopped = true
			break
		}
	}
	require.Equal(t, wantStopped, !peer.IsRunning(), "peer punishment disagrees with evidence verification")
	pending, _ := pool.PendingEvidence(-1)
	wantPending, _ := model.PendingEvidence(-1)
	require.Equal(t, len(wantPending), len(pending))
	require.EqualValues(t, len(pending), pool.Size())
	for _, ev := range pending {
		require.True(t, cmttypes.EvidenceList(wantPending).Has(ev))
		fx.requireSignedByValidators(t, ev)
	}
	return evReceiveResult{stopped: wantStopped, pending: len(pending)}
}

// FuzzEvidenceReactorReceive feeds raw EvidenceList bytes to the reactor.
// Undecodable evidence must stop the peer; anything that reaches the pending
// pool must be signed by the implicated validators.
//
// With empty edits, raw is decoded as wire bytes. Otherwise edits drives
// generate to build signed evidence, applies field-level edits to its proto
// form, optionally re-signing it with the fixture keys, and sends the
// re-encoded list through the same check, so that verification past
// signature and hash checks is reached far more often.
func FuzzEvidenceReactorReceive(f *testing.F) {
	fx := fuzzEvidenceFixture(f)
	for _, seed := range [][]byte{{}, {0, 1}, {0, 1, 0, 1, 0, 0, 0, 1}, {0, 0, 0, 0x84}, {3, 0, 0, 0x81, 0, 3, 0}} {
		var evs cmttypes.EvidenceList
		for _, c := range fx.generate(f, seed) {
			evs = append(evs, c.ev)
		}
		list, err := evidenceListToProto(evs)
		require.NoError(f, err)
		bz, err := proto.Marshal(list)
		require.NoError(f, err)
		f.Add(bz, []byte(nil))
	}
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{0x0a, 0x00}, []byte(nil))
	for _, edits := range evidenceEditSeeds {
		f.Add([]byte(nil), edits)
	}
	signers := map[string]cmttypes.PrivValidator{}
	for _, pv := range append(append([]cmttypes.PrivValidator{}, fx.privVals...), fx.phantom) {
		pk, err := pv.GetPubKey()
		require.NoError(f, err)
		signers[string(pk.Address())] = pv
	}
	f.Fuzz(func(t *testing.T, raw []byte, edits []byte) {
		if len(edits) == 0 {
			if len(raw) > maxMsgSize {
				return
			}
			var list cmtproto.EvidenceList
			if err := proto.Unmarshal(raw, &list); err != nil {
				return // the p2p layer drops undecodable envelopes
			}
			fx.checkEvidenceReceive(t, &list)
			return
		}
		r := fuzzinput.New(edits)
		cases := fx.generate(t, r.Bytes(64))
		evs := make(cmttypes.EvidenceList, len(cases))
		for i, c := range cases {
			evs[i] = c.ev
		}
		list, err := evidenceListToProto(evs)
		require.NoError(t, err)
		n := r.Intn(maxFuzzEdits + 1)
		for i := 0; i < n; i++ {
			editEvidenceList(t, r, list, signers)
		}
		// Re-encode the list as a peer would send it.
		bz, err := proto.Marshal(list)
		require.NoError(t, err)
		var wire cmtproto.EvidenceList
		require.NoError(t, proto.Unmarshal(bz, &wire))
		got := fx.checkEvidenceReceive(t, &wire)
		if n > 0 {
			return
		}
		// Unedited evidence the generator built as valid must be accepted
		// without punishing the peer, each distinct piece once.
		distinct, allValid := map[evKey]bool{}, true
		for _, c := range cases {
			allValid = allValid && c.valid && c.decoded
			distinct[keyOf(c.ev)] = true
		}
		if allValid {
			require.NoError(t, got.decodeErr)
			require.False(t, got.stopped, "peer punished for valid evidence")
			require.Equal(t, len(distinct), got.pending)
		}
	})
}

// editEvidenceList applies one edit to the list or to one piece of evidence
// in it. signers maps validator addresses to the fixture keys for re-signing.
func editEvidenceList(t *testing.T, r *fuzzinput.Reader, list *cmtproto.EvidenceList, signers map[string]cmttypes.PrivValidator) {
	evs := list.Evidence
	switch op := r.Intn(4); {
	case op == 1 && len(evs) > 0 && len(evs) < 2*fzMaxEvs: // repeat a piece
		list.Evidence = append(evs, evs[r.Intn(len(evs))])
		return
	case op == 2 && len(evs) > 1: // move the last piece first
		list.Evidence = append([]cmtproto.Evidence{evs[len(evs)-1]}, evs[:len(evs)-1]...)
		return
	case op == 3 && len(evs) < 2*fzMaxEvs: // evidence with no type set
		list.Evidence = append(evs, cmtproto.Evidence{})
		return
	case len(evs) == 0:
		return
	}
	// Copy the chosen piece, so repeated pieces can diverge.
	i := r.Intn(len(evs))
	bz, err := evs[i].Marshal()
	require.NoError(t, err)
	var ev cmtproto.Evidence
	require.NoError(t, ev.Unmarshal(bz))
	switch e := ev.Sum.(type) {
	case *cmtproto.Evidence_DuplicateVoteEvidence:
		editDuplicateVote(t, r, e.DuplicateVoteEvidence, signers)
	case *cmtproto.Evidence_LightClientAttackEvidence:
		editLightClientAttack(t, r, e.LightClientAttackEvidence, signers)
	}
	evs[i] = ev
}

func editDuplicateVote(t *testing.T, r *fuzzinput.Reader, ev *cmtproto.DuplicateVoteEvidence, signers map[string]cmttypes.PrivValidator) {
	switch op := r.Intn(8); op {
	case 0, 2, 3: // a vote field; 2 and 3 also re-sign the vote
		vote := ev.VoteB
		if r.Bool() {
			vote = ev.VoteA
		}
		if vote == nil {
			return
		}
		editVote(r, vote)
		if op != 0 {
			signVote(t, vote, signers)
		}
	case 1:
		switch r.Intn(3) {
		case 0:
			ev.ValidatorPower += fzDelta(r)
		case 1:
			ev.TotalVotingPower += fzDelta(r)
		case 2:
			ev.Timestamp = ev.Timestamp.Add(time.Duration(fzDelta(r)))
		}
	case 4:
		if r.Bool() {
			ev.VoteA = nil
		} else {
			ev.VoteB = nil
		}
	case 5: // vote B copies vote A, so the block IDs agree
		if ev.VoteA != nil {
			v := *ev.VoteA
			ev.VoteB = &v
		}
	case 6: // claim the other validator's address, then re-sign
		if ev.VoteA != nil && ev.VoteB != nil {
			pk, err := signerList(signers)[r.Intn(len(signers))].GetPubKey()
			require.NoError(t, err)
			ev.VoteA.ValidatorAddress, ev.VoteB.ValidatorAddress = pk.Address(), pk.Address()
			signVote(t, ev.VoteA, signers)
			signVote(t, ev.VoteB, signers)
		}
	case 7:
		ev.VoteA, ev.VoteB = ev.VoteB, ev.VoteA
	}
}

func editVote(r *fuzzinput.Reader, v *cmtproto.Vote) {
	switch r.Intn(7) {
	case 0:
		v.Height = fzEditHeight(r, v.Height)
	case 1:
		v.Round += int32(fzDelta(r))
	case 2:
		v.Type = cmtproto.PrevoteType + cmtproto.PrecommitType - v.Type
	case 3:
		v.Timestamp = v.Timestamp.Add(time.Duration(fzDelta(r)))
	case 4:
		if len(v.BlockID.Hash) > 0 {
			v.BlockID.Hash[r.Intn(len(v.BlockID.Hash))] ^= 1 | r.Byte()
		}
	case 5:
		v.ValidatorIndex += int32(fzDelta(r))
	case 6:
		if len(v.Signature) > 0 {
			v.Signature[r.Intn(len(v.Signature))] ^= 1 | r.Byte()
		}
	}
}

func editLightClientAttack(t *testing.T, r *fuzzinput.Reader, ev *cmtproto.LightClientAttackEvidence, signers map[string]cmttypes.PrivValidator) {
	op := r.Intn(11)
	switch op {
	case 0:
		ev.CommonHeight = fzEditHeight(r, ev.CommonHeight)
		return
	case 1:
		if r.Bool() {
			ev.TotalVotingPower += fzDelta(r)
		} else {
			ev.Timestamp = ev.Timestamp.Add(time.Duration(fzDelta(r)) * time.Second)
		}
		return
	case 2:
		ev.ConflictingBlock = nil
		return
	case 3:
		byz := ev.ByzantineValidators
		switch {
		case r.Bool() && len(byz) > 0:
			ev.ByzantineValidators = byz[:len(byz)-1]
		case len(byz) > 0 && len(byz) < 2*len(signers):
			ev.ByzantineValidators = append(byz, byz[r.Intn(len(byz))])
		}
		return
	}
	cb := ev.ConflictingBlock
	if cb == nil || cb.SignedHeader == nil || cb.SignedHeader.Header == nil || cb.SignedHeader.Commit == nil {
		return
	}
	h, c := cb.SignedHeader.Header, cb.SignedHeader.Commit
	switch op {
	case 4:
		h.Height = fzEditHeight(r, h.Height)
	case 5:
		h.Time = h.Time.Add(time.Duration(fzDelta(r)) * time.Second)
	case 6:
		hashes := []*[]byte{&h.AppHash, &h.DataHash, &h.ValidatorsHash, &h.NextValidatorsHash, &h.LastResultsHash}
		p := hashes[r.Intn(len(hashes))]
		if len(*p) > 0 {
			(*p)[r.Intn(len(*p))] ^= 1 | r.Byte()
		}
	case 7: // rehash the header into the commit and re-sign it
		if header, err := cmttypes.HeaderFromProto(h); err == nil {
			c.BlockID.Hash = header.Hash()
		}
		signCommit(t, c, signers)
	case 8:
		if len(c.Signatures) > 0 {
			s := &c.Signatures[r.Intn(len(c.Signatures))]
			switch r.Intn(3) {
			case 0:
				*s = cmtproto.CommitSig{BlockIdFlag: cmtproto.BlockIDFlagAbsent}
			case 1:
				if len(s.Signature) > 0 {
					s.Signature[r.Intn(len(s.Signature))] ^= 1 | r.Byte()
				}
			case 2:
				s.Timestamp = s.Timestamp.Add(time.Duration(fzDelta(r)))
			}
		}
	case 9:
		if r.Bool() {
			c.Round += int32(fzDelta(r))
		} else {
			c.Height = fzEditHeight(r, c.Height)
		}
	case 10:
		if vs := cb.ValidatorSet; vs != nil && len(vs.Validators) > 0 {
			if v := vs.Validators[r.Intn(len(vs.Validators))]; v != nil {
				v.VotingPower += fzDelta(r)
			}
		}
	}
}

// fzDelta returns a small signed adjustment, usually -1 or 1.
func fzDelta(r *fuzzinput.Reader) int64 {
	switch b := r.Byte(); b % 4 {
	case 0:
		return 1
	case 1:
		return -1
	default:
		return int64(int8(b))
	}
}

// fzEditHeight moves h by a small step, or to a height around the fixture's
// evidence window.
func fzEditHeight(r *fuzzinput.Reader, h int64) int64 {
	if r.Bool() {
		return h + fzDelta(r)
	}
	return int64(r.Intn(fzHeight + 3))
}

// signerList returns the fixture keys in a fixed order.
func signerList(signers map[string]cmttypes.PrivValidator) []cmttypes.PrivValidator {
	addrs := make([]string, 0, len(signers))
	for a := range signers {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	out := make([]cmttypes.PrivValidator, len(addrs))
	for i, a := range addrs {
		out[i] = signers[a]
	}
	return out
}

// signVote re-signs v on the fixture chain with the key for its validator
// address, if there is one.
func signVote(t *testing.T, v *cmtproto.Vote, signers map[string]cmttypes.PrivValidator) {
	pv, ok := signers[string(v.ValidatorAddress)]
	if !ok {
		return
	}
	require.NoError(t, pv.SignVote(fzChainID, v))
}

// signCommit re-signs every commit signature with the key for its validator
// address, if the commit still converts.
func signCommit(t *testing.T, c *cmtproto.Commit, signers map[string]cmttypes.PrivValidator) {
	commit, err := cmttypes.CommitFromProto(c)
	if err != nil {
		return
	}
	for i := range c.Signatures {
		s := &c.Signatures[i]
		if s.BlockIdFlag != cmtproto.BlockIDFlagCommit {
			continue
		}
		pv, ok := signers[string(s.ValidatorAddress)]
		if !ok {
			continue
		}
		vote := commit.GetVote(int32(i)).ToProto()
		require.NoError(t, pv.SignVote(fzChainID, vote))
		s.Signature = vote.Signature
	}
}
