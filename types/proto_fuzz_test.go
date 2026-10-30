package types

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/cometbft/cometbft/crypto"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/internal/fuzzinput"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// protoSeedSet holds one valid wire encoding of each block-level type, and
// the keys that signed them.
type protoSeedSet struct {
	block, part, vote, proposal, commit, evidence []byte
	// lightAttack is light client attack evidence on lightBlockSeed.
	lightAttack []byte
	// parts are the parts of a three-part block, for structured part edits.
	parts [][]byte
	// pv signed the vote, the proposal and the duplicate vote evidence; vals
	// signed the commit.
	pv   MockPV
	vals *ValidatorSet
}

// fuzzValidators returns a validator set of the first n pool keys with powers
// 1..n, and their signers in validator set order.
func fuzzValidators(n int) (*ValidatorSet, []PrivValidator) {
	vals := make([]*Validator, n)
	for i := range vals {
		vals[i] = NewValidator(verifyFuzzPool[i].PubKey(), int64(i+1))
	}
	valSet := NewValidatorSet(vals)
	pvs := make([]PrivValidator, n)
	for i, v := range valSet.Validators {
		pvs[i] = NewMockPVWithParams(verifyFuzzPool[keyIndex(v.Address)], false, false)
	}
	return valSet, pvs
}

// protoSeeds builds valid wire bytes for the block-level types that peers
// send: a block with txs and a commit, its first part, a vote, a proposal,
// the commit on its own, and duplicate vote evidence. All keys come from the
// deterministic pool, so the seeds are the same on every run.
func protoSeeds(tb testing.TB) protoSeedSet {
	tb.Helper()
	marshal := func(msg proto.Message) []byte {
		raw, err := proto.Marshal(msg)
		require.NoError(tb, err)
		return raw
	}
	const chainID = verifyFuzzChainID
	now := verifyFuzzTime

	pv := NewMockPVWithParams(verifyFuzzPool[0], false, false)
	pubKey := pv.PrivKey.PubKey()
	valSet, pvs := fuzzValidators(3)

	blockID := BlockID{Hash: make([]byte, 32), PartSetHeader: PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	voteSet := NewVoteSet(chainID, 9, 0, cmtproto.PrecommitType, valSet)
	extCommit, err := MakeExtCommit(blockID, 9, 0, voteSet, pvs, now, false)
	require.NoError(tb, err)
	lastCommit := extCommit.ToCommit()

	blk := MakeBlock(10, Data{Txs: MakeTxs(10, 3)}, lastCommit, nil)
	blk.ValidatorsHash = valSet.Hash()
	blk.NextValidatorsHash = valSet.Hash()
	blk.ProposerAddress = pubKey.Address()
	pbBlock, err := blk.ToProto()
	require.NoError(tb, err)
	partSet, err := blk.MakePartSet(BlockPartSizeBytes)
	require.NoError(tb, err)
	pbPart, err := partSet.GetPart(0).ToProto()
	require.NoError(tb, err)

	data := make([]byte, 2*int(BlockPartSizeBytes)+1000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	bigParts, err := NewPartSetFromData(data, BlockPartSizeBytes)
	require.NoError(tb, err)
	parts := make([][]byte, bigParts.Total())
	for i := range parts {
		p, err := bigParts.GetPart(i).ToProto()
		require.NoError(tb, err)
		parts[i] = marshal(p)
	}

	v, err := MakeVote(pv, chainID, 0, 10, 0, cmtproto.PrecommitType, blockID, now)
	require.NoError(tb, err)

	p := NewProposal(10, 0, -1, blockID)
	p.Timestamp = now
	pbProposal := p.ToProto()
	require.NoError(tb, pv.SignProposal(chainID, pbProposal))

	voteA, err := MakeVote(pv, chainID, 0, 10, 0, cmtproto.PrecommitType, makeBlockID([]byte{1}, 1, []byte{1}), now)
	require.NoError(tb, err)
	voteB, err := MakeVote(pv, chainID, 0, 10, 0, cmtproto.PrecommitType, makeBlockID([]byte{2}, 1, []byte{2}), now)
	require.NoError(tb, err)
	dve, err := NewDuplicateVoteEvidence(voteA, voteB, now, valSet)
	require.NoError(tb, err)
	pbEvidence, err := EvidenceToProto(dve)
	require.NoError(tb, err)

	var pbLight cmtproto.LightBlock
	require.NoError(tb, proto.Unmarshal(lightBlockSeed(tb), &pbLight))
	lightAttack := &cmtproto.Evidence{Sum: &cmtproto.Evidence_LightClientAttackEvidence{
		LightClientAttackEvidence: &cmtproto.LightClientAttackEvidence{
			ConflictingBlock:    &pbLight,
			CommonHeight:        verifyFuzzHeight - 1,
			ByzantineValidators: pbLight.ValidatorSet.Validators[:2],
			TotalVotingPower:    6,
			Timestamp:           now,
		},
	}}

	return protoSeedSet{
		block:       marshal(pbBlock),
		part:        marshal(pbPart),
		vote:        marshal(v.ToProto()),
		proposal:    marshal(pbProposal),
		commit:      marshal(lastCommit.ToProto()),
		evidence:    marshal(pbEvidence),
		lightAttack: marshal(lightAttack),
		parts:       parts,
		pv:          pv,
		vals:        valSet,
	}
}

// proofRoot computes the root a proof commits to. ComputeRootHash is
// documented to panic on a proof whose index, total and aunts disagree, and
// ValidateBasic does not rule that out, so the panic is turned into !ok here.
func proofRoot(proof *merkle.Proof) (root []byte, ok bool) {
	defer func() {
		if recover() != nil {
			root, ok = nil, false
		}
	}()
	return proof.ComputeRootHash(), true
}

// maxFuzzEdits caps the number of field edits one structured input applies.
const maxFuzzEdits = 16

// fuzzEditor applies the field edits of a structured fuzz input to a valid
// fixture. Each edit is a selector byte that picks an edit kind, followed by
// the values that edit reads.
type fuzzEditor struct {
	r *fuzzinput.Reader
	// mutated is set once an edit kind below the neutral range was applied.
	mutated bool
}

func newFuzzEditor(edits []byte) *fuzzEditor {
	return &fuzzEditor{r: fuzzinput.New(edits)}
}

// run reads up to maxFuzzEdits selectors and calls apply with each, reduced
// modulo kinds. Kinds at or above neutral select another valid fixture or
// repair the object (re-sign, recompute hashes): they leave a valid object
// valid, so they do not count as mutations.
func (e *fuzzEditor) run(kinds, neutral int, apply func(kind int)) {
	for i := 0; i < maxFuzzEdits && e.r.Len() > 0; i++ {
		kind := e.r.Intn(kinds)
		if kind < neutral {
			e.mutated = true
		}
		apply(kind)
	}
}

// index reads an index into a slice of length n, or returns -1 if n is 0.
func (e *fuzzEditor) index(n int) int {
	if n == 0 {
		return -1
	}
	return int(e.r.Uint16()) % n
}

// nearInt returns cur moved by a small signed step, an arbitrary value, or
// one of the given edge values.
func nearInt[T ~int32 | ~int64 | ~uint32 | ~uint64](e *fuzzEditor, cur T, edges ...T) T {
	switch e.r.Intn(3) {
	case 0:
		return cur + T(int8(e.r.Byte()))
	case 1:
		return T(e.r.Uint64())
	default:
		if len(edges) == 0 {
			return 0
		}
		return edges[e.r.Intn(len(edges))]
	}
}

// bytes returns an edited copy of cur: a bit flip, a truncation, an append,
// a replacement of up to maxLen bytes, or nil.
func (e *fuzzEditor) bytes(cur []byte, maxLen int) []byte {
	out := append([]byte(nil), cur...)
	switch e.r.Intn(5) {
	case 0:
		if len(out) > 0 {
			out[int(e.r.Uint16())%len(out)] ^= 1 << (e.r.Byte() % 8)
		}
	case 1:
		out = out[:int(e.r.Uint16())%(len(out)+1)]
	case 2:
		out = append(out, e.r.Bytes(maxLen)...)
	case 3:
		out = e.r.Bytes(maxLen)
	default:
		out = nil
	}
	return out
}

// time returns cur moved by seconds or nanoseconds, an arbitrary instant
// (possibly outside the range protobuf can encode), or the zero time.
func (e *fuzzEditor) time(cur time.Time) time.Time {
	switch e.r.Intn(4) {
	case 0:
		return cur.Add(time.Duration(int16(e.r.Uint16())) * time.Second)
	case 1:
		return cur.Add(time.Duration(int8(e.r.Byte())))
	case 2:
		return time.Unix(e.r.Int64(), int64(e.r.Uint32()))
	default:
		return time.Time{}
	}
}

// blockID edits the hash, the part set header, or clears the block ID.
func (e *fuzzEditor) blockID(id *cmtproto.BlockID) {
	switch e.r.Intn(4) {
	case 0:
		id.Hash = e.bytes(id.Hash, 64)
	case 1:
		id.PartSetHeader.Total = nearInt(e, id.PartSetHeader.Total, 1, MaxBlockPartsCount, math.MaxUint32)
	case 2:
		id.PartSetHeader.Hash = e.bytes(id.PartSetHeader.Hash, 64)
	default:
		*id = cmtproto.BlockID{}
	}
}

// signedMsg points at the fields votes and proposals share.
type signedMsg struct {
	typ     *cmtproto.SignedMsgType
	height  *int64
	round   *int32
	blockID *cmtproto.BlockID
	ts      *time.Time
	sig     *[]byte
}

// Edit kinds shared by votes and proposals.
const (
	msgEditType = iota
	msgEditHeight
	msgEditRound
	msgEditBlockID
	msgEditTimestamp
	msgEditSignature
	msgEditKinds
)

func (e *fuzzEditor) signedMsg(m signedMsg, kind int) {
	switch kind {
	case msgEditType:
		*m.typ = nearInt(e, *m.typ, cmtproto.PrevoteType, cmtproto.PrecommitType, cmtproto.ProposalType)
	case msgEditHeight:
		*m.height = nearInt(e, *m.height, 1, -1, math.MaxInt64, math.MinInt64)
	case msgEditRound:
		*m.round = nearInt(e, *m.round, 1, -1, math.MaxInt32, math.MinInt32)
	case msgEditBlockID:
		e.blockID(m.blockID)
	case msgEditTimestamp:
		*m.ts = e.time(*m.ts)
	case msgEditSignature:
		*m.sig = e.bytes(*m.sig, MaxSignatureSize+1)
	}
}

// Edit kinds of a vote, after the shared ones.
const (
	voteEditAddress = msgEditKinds + iota
	voteEditIndex
	voteEditExtension
	voteEditExtensionSig
	voteEditKinds
)

func (e *fuzzEditor) vote(v *cmtproto.Vote, kind int) {
	switch kind {
	case voteEditAddress:
		v.ValidatorAddress = e.bytes(v.ValidatorAddress, 64)
	case voteEditIndex:
		v.ValidatorIndex = nearInt(e, v.ValidatorIndex, -1, math.MaxInt32, math.MinInt32)
	case voteEditExtension:
		v.Extension = e.bytes(v.Extension, 255)
	case voteEditExtensionSig:
		v.ExtensionSignature = e.bytes(v.ExtensionSignature, MaxSignatureSize+1)
	default:
		e.signedMsg(signedMsg{&v.Type, &v.Height, &v.Round, &v.BlockID, &v.Timestamp, &v.Signature}, kind)
	}
}

// commitSigFields points at the fields CommitSig and ExtendedCommitSig share.
type commitSigFields struct {
	flag *cmtproto.BlockIDFlag
	addr *[]byte
	ts   *time.Time
	sig  *[]byte
}

func commitSigOf(cs *cmtproto.CommitSig) commitSigFields {
	return commitSigFields{&cs.BlockIdFlag, &cs.ValidatorAddress, &cs.Timestamp, &cs.Signature}
}

func extCommitSigOf(cs *cmtproto.ExtendedCommitSig) commitSigFields {
	return commitSigFields{&cs.BlockIdFlag, &cs.ValidatorAddress, &cs.Timestamp, &cs.Signature}
}

// Edit kinds of a commit or an extended commit.
const (
	commitEditHeight = iota
	commitEditRound
	commitEditBlockID
	commitEditDropSig
	commitEditDupSig
	commitEditFlag
	commitEditSigAddress
	commitEditSigTimestamp
	commitEditSignature
	commitEditAbsent
	commitEditKinds
)

// maxFuzzSigs bounds how many signatures duplication can grow a commit to.
const maxFuzzSigs = 32

// editCommit applies one commit edit. sigs is the signature list, fields
// gives access to the shared signature fields, and absent is an absent
// signature of the list's element type.
func editCommit[S any](e *fuzzEditor, height *int64, round *int32, blockID *cmtproto.BlockID,
	sigs *[]S, fields func(*S) commitSigFields, absent S, kind int,
) {
	switch kind {
	case commitEditHeight:
		*height = nearInt(e, *height, 1, -1, math.MaxInt64, math.MinInt64)
		return
	case commitEditRound:
		*round = nearInt(e, *round, 1, -1, math.MaxInt32, math.MinInt32)
		return
	case commitEditBlockID:
		e.blockID(blockID)
		return
	}
	i := e.index(len(*sigs))
	if i < 0 {
		return
	}
	f := fields(&(*sigs)[i])
	switch kind {
	case commitEditDropSig:
		*sigs = append((*sigs)[:i:i], (*sigs)[i+1:]...)
	case commitEditDupSig:
		if len(*sigs) < maxFuzzSigs {
			*sigs = append(*sigs, (*sigs)[i])
		}
	case commitEditFlag:
		*f.flag = nearInt(e, *f.flag, cmtproto.BlockIDFlagAbsent, cmtproto.BlockIDFlagCommit, cmtproto.BlockIDFlagNil)
	case commitEditSigAddress:
		*f.addr = e.bytes(*f.addr, 64)
	case commitEditSigTimestamp:
		*f.ts = e.time(*f.ts)
	case commitEditSignature:
		*f.sig = e.bytes(*f.sig, MaxSignatureSize+1)
	case commitEditAbsent:
		(*sigs)[i] = absent
	}
}

func (e *fuzzEditor) commit(c *cmtproto.Commit, kind int) {
	editCommit(e, &c.Height, &c.Round, &c.BlockID, &c.Signatures, commitSigOf,
		cmtproto.CommitSig{BlockIdFlag: cmtproto.BlockIDFlagAbsent}, kind)
}

// header edits one header field, picked by the next byte.
func (e *fuzzEditor) header(h *cmtproto.Header) {
	switch e.r.Intn(7) {
	case 0:
		h.Height = nearInt(e, h.Height, 1, -1, math.MaxInt64)
	case 1:
		h.Time = e.time(h.Time)
	case 2:
		h.ChainID = string(e.bytes([]byte(h.ChainID), MaxChainIDLen+1))
	case 3:
		if e.r.Bool() {
			h.Version.Block = nearInt(e, h.Version.Block, 0, math.MaxUint64)
		} else {
			h.Version.App = nearInt(e, h.Version.App, 0, math.MaxUint64)
		}
	case 4:
		e.blockID(&h.LastBlockId)
	case 5:
		hashes := []*[]byte{
			&h.LastCommitHash, &h.DataHash, &h.ValidatorsHash, &h.NextValidatorsHash,
			&h.ConsensusHash, &h.AppHash, &h.LastResultsHash, &h.EvidenceHash,
		}
		f := hashes[e.r.Intn(len(hashes))]
		*f = e.bytes(*f, 64)
	default:
		h.ProposerAddress = e.bytes(h.ProposerAddress, 64)
	}
}

// poolKey returns the pool key with address addr, or nil.
func poolKey(addr []byte) crypto.PrivKey {
	if i := keyIndex(addr); i >= 0 {
		return verifyFuzzPool[i]
	}
	return nil
}

// precommit is the vote a commit signature with this flag signs.
func precommit(height int64, round int32, blockID cmtproto.BlockID, flag cmtproto.BlockIDFlag, ts time.Time) *cmtproto.Vote {
	v := &cmtproto.Vote{Type: cmtproto.PrecommitType, Height: height, Round: round, Timestamp: ts}
	if flag == cmtproto.BlockIDFlagCommit {
		v.BlockID = blockID
	}
	return v
}

// signable reports whether sign bytes can be built for a message with this
// block ID and timestamp. Building them panics on a block ID that does not
// decode or a timestamp protobuf cannot encode; real signers only sign
// messages that passed validation.
func signable(id *cmtproto.BlockID, ts time.Time) bool {
	_, err := BlockIDFromProto(id)
	return err == nil && timestamppb.New(ts).CheckValid() == nil
}

// resignCommit re-signs every commit and nil signature whose address belongs
// to a pool key, with that key, for the commit's current height, round and
// block ID.
func resignCommit[S any](t *testing.T, height int64, round int32, blockID cmtproto.BlockID,
	sigs []S, fields func(*S) commitSigFields,
) {
	for i := range sigs {
		f := fields(&sigs[i])
		key := poolKey(*f.addr)
		if key == nil || (*f.flag != cmtproto.BlockIDFlagCommit && *f.flag != cmtproto.BlockIDFlagNil) ||
			!signable(&blockID, *f.ts) {
			continue
		}
		sig, err := key.Sign(VoteSignBytes(verifyFuzzChainID, precommit(height, round, blockID, *f.flag, *f.ts)))
		require.NoError(t, err)
		*f.sig = sig
	}
}

// wantCommitLight reports whether VerifyCommitLight must accept a commit
// whose signatures were all re-signed: the sizes match, every commit-flag
// signature sits at its validator's index, and those validators hold more
// than two thirds of the power.
func wantCommitLight(vals *ValidatorSet, c *Commit) bool {
	if vals.Size() != len(c.Signatures) {
		return false
	}
	var tally int64
	for i, cs := range c.Signatures {
		if cs.BlockIDFlag != BlockIDFlagCommit {
			continue
		}
		if !bytes.Equal(cs.ValidatorAddress, vals.Validators[i].Address) {
			return false
		}
		tally += vals.Validators[i].VotingPower
	}
	return tally > vals.TotalVotingPower()*2/3
}

// Edit kinds of a validator list.
const (
	valEditDrop = iota
	valEditDup
	valEditPower
	valEditPriority
	valEditAddress
	valEditPubKey
	valEditAdd
	valEditSwap
	valEditKinds
)

// maxFuzzVals bounds how many validators edits can grow a list to.
const maxFuzzVals = 16

func (e *fuzzEditor) validators(t *testing.T, vals *[]*cmtproto.Validator, kind int) {
	if kind == valEditAdd {
		if len(*vals) < maxFuzzVals {
			pv, err := NewValidator(verifyFuzzPool[e.r.Intn(verifyFuzzKeys)].PubKey(), int64(e.r.Byte())).ToProto()
			require.NoError(t, err)
			*vals = append(*vals, pv)
		}
		return
	}
	i := e.index(len(*vals))
	if i < 0 {
		return
	}
	v := *(*vals)[i]
	switch kind {
	case valEditDrop:
		*vals = append((*vals)[:i:i], (*vals)[i+1:]...)
		return
	case valEditDup:
		if len(*vals) < maxFuzzVals {
			*vals = append(*vals, &v)
		}
		return
	case valEditPower:
		v.VotingPower = nearInt(e, v.VotingPower, 0, -1, MaxTotalVotingPower, MaxTotalVotingPower+1, math.MaxInt64)
	case valEditPriority:
		v.ProposerPriority = nearInt(e, v.ProposerPriority, 0, math.MaxInt64, math.MinInt64)
	case valEditAddress:
		v.Address = e.bytes(v.Address, 64)
	case valEditPubKey:
		pk, err := cryptoenc.PubKeyToProto(verifyFuzzPool[e.r.Intn(verifyFuzzKeys)].PubKey())
		require.NoError(t, err)
		v.PubKey = pk
	case valEditSwap:
		j := e.index(len(*vals))
		(*vals)[i], (*vals)[j] = (*vals)[j], (*vals)[i]
		return
	}
	(*vals)[i] = &v
}

// unmarshalFixture decodes a fixture's wire bytes into a fresh message, so
// edits never touch the shared fixture.
func unmarshalFixture(t *testing.T, raw []byte, msg proto.Message) {
	require.NoError(t, proto.Unmarshal(raw, msg))
}

// checkBlockBytes decodes peer-supplied block bytes the way blocksync and the
// propagation reactor do, then validates and re-encodes the result. It
// returns the first decode or validation error.
func checkBlockBytes(t *testing.T, raw []byte) error {
	var pb cmtproto.Block
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return err
	}
	blk, err := BlockFromProto(&pb)
	if err != nil {
		return err
	}
	if err := blk.ValidateBasic(); err != nil {
		return err
	}
	_ = blk.Hash()
	_, _ = blk.MakePartSet(BlockPartSizeBytes)
	again, err := blk.ToProto()
	require.NoError(t, err)
	back, err := BlockFromProto(again)
	require.NoError(t, err)
	require.Equal(t, blk.Hash(), back.Hash(), "block hash changed across a proto round trip")
	return nil
}

// Edit kinds of a block.
const (
	blockEditHeader = iota
	blockEditAddTx
	blockEditDropTx
	blockEditTx
	blockEditSquareSize
	blockEditDataHash
	blockEditLastCommit
	blockEditNilLastCommit
	blockEditAddEvidence
	blockEditDropEvidence
	blockEditResign // neutral
	blockEditRehash // neutral
	blockEditKinds
)

// maxFuzzTxs and maxFuzzEvidence bound how far edits can grow a block.
const (
	maxFuzzTxs      = 64
	maxFuzzEvidence = 4
)

// encodable reports whether msg marshals. Hashing panics on timestamps that
// protobuf cannot encode, which decoding never produces but edits do.
func encodable(msg proto.Message) bool {
	_, err := proto.Marshal(msg)
	return err == nil
}

// rehashBlock recomputes the header hashes of the last commit, the data and
// the evidence, and the data hash carried in the data itself.
func rehashBlock(pb *cmtproto.Block) {
	if pb.LastCommit != nil && encodable(pb.LastCommit) {
		if c, err := CommitFromProto(pb.LastCommit); err == nil {
			pb.Header.LastCommitHash = c.Hash()
		}
	}
	txs := make(Txs, len(pb.Data.Txs))
	for i, tx := range pb.Data.Txs {
		txs[i] = tx
	}
	pb.Data.Hash = txs.Hash()
	pb.Header.DataHash = pb.Data.Hash
	evl := make(EvidenceList, len(pb.Evidence.Evidence))
	for i := range pb.Evidence.Evidence {
		ev, err := EvidenceFromProto(&pb.Evidence.Evidence[i])
		if err != nil {
			return
		}
		evl[i] = ev
	}
	pb.Header.EvidenceHash = evl.Hash()
}

// FuzzBlockFromProto decodes peer-supplied block bytes the way blocksync and
// the propagation reactor do, then validates and re-encodes the result.
//
// With edits, it starts from a valid block and edits header fields, txs, the
// square size, the last commit and the evidence list, optionally re-signing
// the last commit and recomputing the header hashes, so that edited blocks
// still reach the round trip.
func FuzzBlockFromProto(f *testing.F) {
	fx := protoSeeds(f)
	f.Add(fx.block, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{blockEditRehash})
	f.Add([]byte{}, []byte{blockEditAddTx, 4, 't', 'x', '-', '4', blockEditRehash})
	f.Add([]byte{}, []byte{blockEditAddEvidence, 0, blockEditAddEvidence, 1, blockEditRehash})
	f.Add([]byte{}, []byte{blockEditLastCommit, commitEditDropSig, 0, 0, blockEditRehash})
	f.Add([]byte{}, []byte{blockEditLastCommit, commitEditRound, 0, 1, blockEditResign, blockEditRehash})
	f.Add([]byte{}, []byte{blockEditHeader, 0, 0, 0xFF, blockEditSquareSize, 1, 0, 0, 0, 0, 0, 0, 0, 0x80})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_ = checkBlockBytes(t, raw)
			return
		}
		var pb cmtproto.Block
		unmarshalFixture(t, fx.block, &pb)
		e := newFuzzEditor(edits)
		resign, rehash := false, false
		e.run(blockEditKinds, blockEditResign, func(kind int) {
			switch kind {
			case blockEditHeader:
				e.header(&pb.Header)
			case blockEditAddTx:
				if len(pb.Data.Txs) < maxFuzzTxs {
					pb.Data.Txs = append(pb.Data.Txs, e.r.Bytes(255))
				}
			case blockEditDropTx:
				if i := e.index(len(pb.Data.Txs)); i >= 0 {
					pb.Data.Txs = append(pb.Data.Txs[:i:i], pb.Data.Txs[i+1:]...)
				}
			case blockEditTx:
				if i := e.index(len(pb.Data.Txs)); i >= 0 {
					pb.Data.Txs[i] = e.bytes(pb.Data.Txs[i], 255)
				}
			case blockEditSquareSize:
				pb.Data.SquareSize = nearInt(e, pb.Data.SquareSize, 1, 128, math.MaxUint64)
			case blockEditDataHash:
				pb.Data.Hash = e.bytes(pb.Data.Hash, 64)
			case blockEditLastCommit:
				if pb.LastCommit != nil {
					e.commit(pb.LastCommit, e.r.Intn(commitEditKinds))
				}
			case blockEditNilLastCommit:
				pb.LastCommit = nil
			case blockEditAddEvidence:
				if len(pb.Evidence.Evidence) < maxFuzzEvidence {
					src := fx.evidence
					if e.r.Bool() {
						src = fx.lightAttack
					}
					var ev cmtproto.Evidence
					unmarshalFixture(t, src, &ev)
					pb.Evidence.Evidence = append(pb.Evidence.Evidence, ev)
				}
			case blockEditDropEvidence:
				if i := e.index(len(pb.Evidence.Evidence)); i >= 0 {
					pb.Evidence.Evidence = append(pb.Evidence.Evidence[:i:i], pb.Evidence.Evidence[i+1:]...)
				}
			case blockEditResign:
				resign = true
			case blockEditRehash:
				rehash = true
			}
		})
		if resign && pb.LastCommit != nil {
			c := pb.LastCommit
			resignCommit(t, c.Height, c.Round, c.BlockID, c.Signatures, commitSigOf)
		}
		if rehash {
			rehashBlock(&pb)
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		err = checkBlockBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "re-signed or rehashed valid block rejected")
		}
	})
}

// checkPartBytes decodes a block part and adds it to a part set whose header
// is taken from the part's own proof, which is what a receiving node does for
// parts of a proposal it has only the header for. It reports whether the part
// was decoded and added.
func checkPartBytes(t *testing.T, raw []byte) bool {
	var pb cmtproto.Part
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return false
	}
	p, err := PartFromProto(&pb)
	if err != nil {
		return false
	}
	root, ok := proofRoot(&p.Proof)
	if !ok || p.Proof.Total <= 0 || p.Proof.Total > int64(MaxBlockPartsCount) {
		return false
	}
	ps := NewPartSetFromHeader(PartSetHeader{Total: uint32(p.Proof.Total), Hash: root}, BlockPartSizeBytes)
	added, err := ps.AddPart(p)
	if err == nil && added {
		require.True(t, ps.HasPart(int(p.Index)))
		_ = ps.GetPart(int(p.Index))
		_ = ps.BitArray()
	}
	again, err := p.ToProto()
	require.NoError(t, err)
	_, err = PartFromProto(again)
	require.NoError(t, err)
	return err == nil && added
}

// Edit kinds of a block part.
const (
	partEditIndex = iota
	partEditProofIndex
	partEditProofTotal
	partEditLeafHash
	partEditBytes
	partEditDropAunt
	partEditAddAunt
	partEditAunt
	partEditSwapAunts
	partEditBase   // neutral: start over from a part of the three-part block
	partEditRelink // neutral: recompute the leaf hash from the bytes
	partEditKinds
)

// FuzzPartFromProto decodes a block part and adds it to a part set whose
// header is taken from the part's own proof, which is what a receiving node
// does for parts of a proposal it has only the header for.
//
// With edits, it starts from a valid part (of a one-part or a three-part
// block) and edits the index, the bytes and the proof, optionally recomputing
// the leaf hash so that edited bytes still carry a consistent proof.
func FuzzPartFromProto(f *testing.F) {
	fx := protoSeeds(f)
	f.Add(fx.part, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{partEditBase, 1, partEditRelink})
	f.Add([]byte{}, []byte{partEditBase, 2, partEditBytes, 1, 10, 0, partEditRelink})
	f.Add([]byte{}, []byte{partEditBase, 0, partEditBytes, 0, 5, 0, 3, partEditRelink})
	f.Add([]byte{}, []byte{partEditBase, 1, partEditSwapAunts, 0, 0, 1, 0})
	f.Add([]byte{}, []byte{partEditBase, 2, partEditIndex, 0, 0xFF, partEditProofIndex, 0, 0xFF})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_ = checkPartBytes(t, raw)
			return
		}
		var pb cmtproto.Part
		unmarshalFixture(t, fx.part, &pb)
		e := newFuzzEditor(edits)
		relink := false
		e.run(partEditKinds, partEditBase, func(kind int) {
			aunts := &pb.Proof.Aunts
			switch kind {
			case partEditIndex:
				pb.Index = nearInt(e, pb.Index, 1, MaxBlockPartsCount, math.MaxUint32)
			case partEditProofIndex:
				pb.Proof.Index = nearInt(e, pb.Proof.Index, 1, -1, math.MaxInt64)
			case partEditProofTotal:
				pb.Proof.Total = nearInt(e, pb.Proof.Total, 0, 1, -1, int64(MaxBlockPartsCount)+1, math.MaxInt64)
			case partEditLeafHash:
				pb.Proof.LeafHash = e.bytes(pb.Proof.LeafHash, 64)
			case partEditBytes:
				pb.Bytes = e.bytes(pb.Bytes, 255)
			case partEditDropAunt:
				if i := e.index(len(*aunts)); i >= 0 {
					*aunts = append((*aunts)[:i:i], (*aunts)[i+1:]...)
				}
			case partEditAddAunt:
				if len(*aunts) <= merkle.MaxAunts {
					*aunts = append(*aunts, e.r.Bytes(64))
				}
			case partEditAunt:
				if i := e.index(len(*aunts)); i >= 0 {
					(*aunts)[i] = e.bytes((*aunts)[i], 64)
				}
			case partEditSwapAunts:
				if i, j := e.index(len(*aunts)), e.index(len(*aunts)); i >= 0 {
					(*aunts)[i], (*aunts)[j] = (*aunts)[j], (*aunts)[i]
				}
			case partEditBase:
				pb = cmtproto.Part{}
				unmarshalFixture(t, fx.parts[e.r.Intn(len(fx.parts))], &pb)
				relink = false
			case partEditRelink:
				relink = true
			}
		})
		if relink {
			pb.Proof.LeafHash = merkle.LeafHash(pb.Bytes)
		}
		raw, err := proto.Marshal(&pb)
		require.NoError(t, err)
		added := checkPartBytes(t, raw)
		if !e.mutated {
			require.True(t, added, "valid part rejected")
		}
	})
}

// checkVoteBytes decodes a vote the way the consensus reactor does. It
// returns the decoded vote, or the first decode or validation error.
func checkVoteBytes(t *testing.T, raw []byte) (*Vote, error) {
	var pb cmtproto.Vote
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	v, err := VoteFromProto(&pb)
	if err != nil {
		return nil, err
	}
	if err := v.ValidateBasic(); err != nil {
		return nil, err
	}
	_ = v.String()
	_ = VoteSignBytes(verifyFuzzChainID, v.ToProto())
	back, err := VoteFromProto(v.ToProto())
	require.NoError(t, err)
	require.Equal(t, v.ToProto(), back.ToProto())
	return v, nil
}

// voteEditResign is the neutral edit kind of FuzzVoteFromProto.
const voteEditResign = voteEditKinds

// FuzzVoteFromProto decodes a vote the way the consensus reactor does.
//
// With edits, it starts from a valid precommit and edits its fields,
// optionally re-signing it with the fixture key; a re-signed vote that
// validates must also verify.
func FuzzVoteFromProto(f *testing.F) {
	fx := protoSeeds(f)
	pub := fx.pv.PrivKey.PubKey()
	f.Add(fx.vote, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{voteEditResign})
	f.Add([]byte{}, []byte{msgEditRound, 0, 1, voteEditResign})
	f.Add([]byte{}, []byte{msgEditType, 2, 0, voteEditResign})
	f.Add([]byte{}, []byte{msgEditBlockID, 3, voteEditResign})
	f.Add([]byte{}, []byte{voteEditExtension, 3, 4, 'e', 'x', 't', '!', voteEditResign})
	f.Add([]byte{}, []byte{msgEditTimestamp, 3, voteEditIndex, 0, 0xFF})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkVoteBytes(t, raw)
			return
		}
		var pb cmtproto.Vote
		unmarshalFixture(t, fx.vote, &pb)
		e := newFuzzEditor(edits)
		resign := false
		e.run(voteEditKinds+1, voteEditKinds, func(kind int) {
			if kind == voteEditResign {
				resign = true
				return
			}
			e.vote(&pb, kind)
		})
		resign = resign && signable(&pb.BlockID, pb.Timestamp)
		if resign {
			// An error means an extension on a vote that may not carry one;
			// the vote signature is already replaced and ValidateBasic rejects
			// the extension.
			_ = fx.pv.SignVote(verifyFuzzChainID, &pb)
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		v, err := checkVoteBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "re-signed valid vote rejected")
		}
		if err == nil && resign && bytes.Equal(v.ValidatorAddress, pub.Address()) {
			require.NoError(t, v.Verify(verifyFuzzChainID, pub), "re-signed vote does not verify")
		}
	})
}

// checkProposalBytes decodes a proposal the way the consensus and
// propagation reactors do. It returns the decoded proposal, or the first
// decode or validation error.
func checkProposalBytes(t *testing.T, raw []byte) (*Proposal, error) {
	var pb cmtproto.Proposal
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	p, err := ProposalFromProto(&pb)
	if err != nil {
		return nil, err
	}
	if err := p.ValidateBasic(); err != nil {
		return nil, err
	}
	_ = p.String()
	_ = ProposalSignBytes(verifyFuzzChainID, p.ToProto())
	back, err := ProposalFromProto(p.ToProto())
	require.NoError(t, err)
	require.Equal(t, p.ToProto(), back.ToProto())
	return p, nil
}

// Edit kinds of a proposal, after the shared ones.
const (
	proposalEditPOLRound = msgEditKinds + iota
	proposalEditResign   // neutral
	proposalEditKinds
)

// FuzzProposalFromProto decodes a proposal the way the consensus and
// propagation reactors do.
//
// With edits, it starts from a valid proposal and edits its fields,
// optionally re-signing it with the fixture key; a re-signed proposal that
// validates must also carry a valid signature.
func FuzzProposalFromProto(f *testing.F) {
	fx := protoSeeds(f)
	pub := fx.pv.PrivKey.PubKey()
	f.Add(fx.proposal, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{proposalEditResign})
	f.Add([]byte{}, []byte{msgEditRound, 0, 2, proposalEditPOLRound, 0, 2, proposalEditResign})
	f.Add([]byte{}, []byte{msgEditRound, 0, 2, proposalEditPOLRound, 0, 3, proposalEditResign})
	f.Add([]byte{}, []byte{msgEditHeight, 2, 2, proposalEditResign})
	f.Add([]byte{}, []byte{msgEditTimestamp, 1, 1, proposalEditResign})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkProposalBytes(t, raw)
			return
		}
		var pb cmtproto.Proposal
		unmarshalFixture(t, fx.proposal, &pb)
		e := newFuzzEditor(edits)
		resign := false
		e.run(proposalEditKinds, proposalEditResign, func(kind int) {
			switch kind {
			case proposalEditPOLRound:
				pb.PolRound = nearInt(e, pb.PolRound, -1, -2, math.MaxInt32, math.MinInt32)
			case proposalEditResign:
				resign = true
			default:
				e.signedMsg(signedMsg{&pb.Type, &pb.Height, &pb.Round, &pb.BlockID, &pb.Timestamp, &pb.Signature}, kind)
			}
		})
		resign = resign && signable(&pb.BlockID, pb.Timestamp)
		if resign {
			require.NoError(t, fx.pv.SignProposal(verifyFuzzChainID, &pb))
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		p, err := checkProposalBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "re-signed valid proposal rejected")
		}
		if err == nil && resign {
			require.True(t, pub.VerifySignature(ProposalSignBytes(verifyFuzzChainID, p.ToProto()), p.Signature),
				"re-signed proposal does not verify")
		}
	})
}

// checkCommitBytes decodes a commit the way blocksync and the light client
// do and checks the vote-set helpers on it. It returns the decoded commit,
// or the first decode or validation error.
func checkCommitBytes(t *testing.T, raw []byte) (*Commit, error) {
	var pb cmtproto.Commit
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	c, err := CommitFromProto(&pb)
	if err != nil {
		return nil, err
	}
	if err := c.ValidateBasic(); err != nil {
		return nil, err
	}
	_ = c.Hash()
	_ = c.Size()
	for i := range c.Signatures {
		_ = c.GetVote(int32(i))
	}
	back, err := CommitFromProto(c.ToProto())
	require.NoError(t, err)
	require.Equal(t, c.Hash(), back.Hash(), "commit hash changed across a proto round trip")
	return c, nil
}

// commitEditResign is the neutral edit kind of FuzzCommitFromProto.
const commitEditResign = commitEditKinds

// FuzzCommitFromProto decodes a commit the way blocksync and the light client
// do and checks the vote-set helpers on it.
//
// With edits, it starts from a valid three-validator commit and edits the
// height, round, block ID and individual signatures (drop, duplicate, flag,
// address, timestamp, signature bytes, absent), optionally re-signing every
// signature. A re-signed commit that validates and still carries more than
// two thirds of the power at the right indices must pass VerifyCommitLight.
func FuzzCommitFromProto(f *testing.F) {
	fx := protoSeeds(f)
	f.Add(fx.commit, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{commitEditResign})
	f.Add([]byte{}, []byte{commitEditDropSig, 1, 0})
	f.Add([]byte{}, []byte{commitEditAbsent, 0, 0, commitEditResign})
	f.Add([]byte{}, []byte{commitEditFlag, 2, 0, 2, 2, commitEditResign})
	f.Add([]byte{}, []byte{commitEditRound, 0, 3, commitEditSigTimestamp, 1, 0, 0, 5, commitEditResign})
	f.Add([]byte{}, []byte{commitEditDupSig, 2, 0, commitEditResign})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkCommitBytes(t, raw)
			return
		}
		var pb cmtproto.Commit
		unmarshalFixture(t, fx.commit, &pb)
		e := newFuzzEditor(edits)
		resign := false
		e.run(commitEditKinds+1, commitEditKinds, func(kind int) {
			if kind == commitEditResign {
				resign = true
				return
			}
			e.commit(&pb, kind)
		})
		if resign {
			resignCommit(t, pb.Height, pb.Round, pb.BlockID, pb.Signatures, commitSigOf)
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		c, err := checkCommitBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "re-signed valid commit rejected")
		}
		if err != nil {
			return
		}
		light := fx.vals.VerifyCommitLight(verifyFuzzChainID, c.BlockID, c.Height, c)
		if (!e.mutated || resign) && wantCommitLight(fx.vals, c) {
			require.NoError(t, light, "re-signed commit with enough power rejected")
		}
	})
}

// checkEvidenceBytes decodes evidence the way the evidence reactor does. It
// returns the decoded evidence, or the first decode or validation error.
func checkEvidenceBytes(t *testing.T, raw []byte) (Evidence, error) {
	var pb cmtproto.Evidence
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	ev, err := EvidenceFromProto(&pb)
	if err != nil {
		return nil, err
	}
	if err := ev.ValidateBasic(); err != nil {
		return nil, err
	}
	_ = ev.Hash()
	_ = ev.String()
	again, err := EvidenceToProto(ev)
	require.NoError(t, err)
	back, err := EvidenceFromProto(again)
	require.NoError(t, err)
	require.Equal(t, ev.Hash(), back.Hash(), "evidence hash changed across a proto round trip")
	return ev, nil
}

// Edit kinds of evidence.
const (
	evidenceEditVoteA = iota
	evidenceEditVoteB
	evidenceEditTotalPower
	evidenceEditValidatorPower
	evidenceEditTimestamp
	evidenceEditSwapVotes
	evidenceEditSameVotes
	evidenceEditDropVote
	evidenceEditCommonHeight
	evidenceEditByzantine
	evidenceEditConflicting
	evidenceEditLightAttack // neutral: switch to the light client attack fixture
	evidenceEditResign      // neutral: re-sign both duplicate votes
	evidenceEditKinds
)

// FuzzEvidenceFromProto decodes evidence the way the evidence reactor does.
//
// With edits, it starts from valid duplicate vote evidence or light client
// attack evidence and edits the votes, powers, timestamps, common height,
// byzantine validators and the conflicting block, optionally re-signing the
// duplicate votes; re-signed votes in valid evidence must verify.
func FuzzEvidenceFromProto(f *testing.F) {
	fx := protoSeeds(f)
	pub := fx.pv.PrivKey.PubKey()
	f.Add(fx.evidence, []byte(nil))
	f.Add(fx.lightAttack, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{evidenceEditResign})
	f.Add([]byte{}, []byte{evidenceEditLightAttack})
	f.Add([]byte{}, []byte{evidenceEditVoteA, msgEditRound, 0, 1, evidenceEditVoteB, msgEditRound, 0, 1, evidenceEditResign})
	f.Add([]byte{}, []byte{evidenceEditSwapVotes, evidenceEditResign})
	f.Add([]byte{}, []byte{evidenceEditSameVotes, evidenceEditResign})
	f.Add([]byte{}, []byte{evidenceEditLightAttack, evidenceEditCommonHeight, 0, 1})
	f.Add([]byte{}, []byte{evidenceEditLightAttack, evidenceEditByzantine, valEditDup, 0, 0})
	f.Add([]byte{}, []byte{evidenceEditLightAttack, evidenceEditConflicting, commitEditDropSig, 0, 0})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkEvidenceBytes(t, raw)
			return
		}
		var dvePB, lcaPB cmtproto.Evidence
		unmarshalFixture(t, fx.evidence, &dvePB)
		unmarshalFixture(t, fx.lightAttack, &lcaPB)
		dve := dvePB.GetDuplicateVoteEvidence()
		lca := lcaPB.GetLightClientAttackEvidence()
		e := newFuzzEditor(edits)
		useLCA, resign := false, false
		editVote := func(v **cmtproto.Vote) {
			kind := e.r.Intn(voteEditKinds)
			if *v != nil {
				e.vote(*v, kind)
			}
		}
		e.run(evidenceEditKinds, evidenceEditLightAttack, func(kind int) {
			switch kind {
			case evidenceEditVoteA:
				editVote(&dve.VoteA)
			case evidenceEditVoteB:
				editVote(&dve.VoteB)
			case evidenceEditTotalPower:
				dve.TotalVotingPower = nearInt(e, dve.TotalVotingPower, 0, -1, math.MaxInt64)
				lca.TotalVotingPower = nearInt(e, lca.TotalVotingPower, 0, -1, math.MaxInt64)
			case evidenceEditValidatorPower:
				dve.ValidatorPower = nearInt(e, dve.ValidatorPower, 0, -1, math.MaxInt64)
			case evidenceEditTimestamp:
				dve.Timestamp = e.time(dve.Timestamp)
				lca.Timestamp = dve.Timestamp
			case evidenceEditSwapVotes:
				dve.VoteA, dve.VoteB = dve.VoteB, dve.VoteA
			case evidenceEditSameVotes:
				if dve.VoteA != nil {
					vote := *dve.VoteA
					dve.VoteB = &vote
				}
			case evidenceEditDropVote:
				dve.VoteB = nil
			case evidenceEditCommonHeight:
				lca.CommonHeight = nearInt(e, lca.CommonHeight, 0, -1, verifyFuzzHeight, verifyFuzzHeight+1, math.MaxInt64)
			case evidenceEditByzantine:
				e.validators(t, &lca.ByzantineValidators, e.r.Intn(valEditKinds))
			case evidenceEditConflicting:
				sh := lca.ConflictingBlock.SignedHeader
				if e.r.Bool() {
					e.header(sh.Header)
				} else {
					e.commit(sh.Commit, e.r.Intn(commitEditKinds))
				}
			case evidenceEditLightAttack:
				useLCA = true
			case evidenceEditResign:
				resign = true
			}
		})
		for _, v := range []*cmtproto.Vote{dve.VoteA, dve.VoteB} {
			if resign && v != nil && signable(&v.BlockID, v.Timestamp) {
				_ = fx.pv.SignVote(verifyFuzzChainID, v)
			}
		}
		pb := &dvePB
		if useLCA {
			pb = &lcaPB
		}
		raw, err := proto.Marshal(pb)
		if err != nil {
			return
		}
		ev, err := checkEvidenceBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "re-signed valid evidence rejected")
		}
		if dv, ok := ev.(*DuplicateVoteEvidence); ok && err == nil && resign {
			for _, v := range []*Vote{dv.VoteA, dv.VoteB} {
				if bytes.Equal(v.ValidatorAddress, pub.Address()) {
					require.NoError(t, v.Verify(verifyFuzzChainID, pub), "re-signed duplicate vote does not verify")
				}
			}
		}
	})
}

// TestProtoFuzzSeeds keeps the seed corpus meaningful: every seed decodes and
// validates, so the fuzzer starts from accepted inputs.
func TestProtoFuzzSeeds(t *testing.T) {
	seeds := protoSeeds(t)
	block, part, vote, proposal, commit, evidence := seeds.block, seeds.part, seeds.vote, seeds.proposal, seeds.commit, seeds.evidence

	var pbBlock cmtproto.Block
	require.NoError(t, proto.Unmarshal(block, &pbBlock))
	blk, err := BlockFromProto(&pbBlock)
	require.NoError(t, err)
	require.NoError(t, blk.ValidateBasic())

	var pbPart cmtproto.Part
	require.NoError(t, proto.Unmarshal(part, &pbPart))
	_, err = PartFromProto(&pbPart)
	require.NoError(t, err)

	var pbVote cmtproto.Vote
	require.NoError(t, proto.Unmarshal(vote, &pbVote))
	v, err := VoteFromProto(&pbVote)
	require.NoError(t, err)
	require.NoError(t, v.ValidateBasic())

	var pbProposal cmtproto.Proposal
	require.NoError(t, proto.Unmarshal(proposal, &pbProposal))
	p, err := ProposalFromProto(&pbProposal)
	require.NoError(t, err)
	require.NoError(t, p.ValidateBasic())

	var pbCommit cmtproto.Commit
	require.NoError(t, proto.Unmarshal(commit, &pbCommit))
	c, err := CommitFromProto(&pbCommit)
	require.NoError(t, err)
	require.NoError(t, c.ValidateBasic())

	for _, raw := range [][]byte{evidence, seeds.lightAttack} {
		var pbEvidence cmtproto.Evidence
		require.NoError(t, proto.Unmarshal(raw, &pbEvidence))
		ev, err := EvidenceFromProto(&pbEvidence)
		require.NoError(t, err)
		require.NoError(t, ev.ValidateBasic())
	}

	for _, raw := range seeds.parts {
		require.True(t, checkPartBytes(t, raw), "three-part block part rejected")
	}
}
