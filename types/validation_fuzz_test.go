package types

import (
	"bytes"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/version"
)

const (
	verifyFuzzChainID = "fuzz-chain"
	verifyFuzzHeight  = int64(10)
	// verifyFuzzKeys is the size of the deterministic key pool: up to
	// verifyFuzzMaxVals validators plus outsiders that are never in the set.
	verifyFuzzKeys    = 10
	verifyFuzzMaxVals = 8
)

var (
	verifyFuzzTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	verifyFuzzPool = func() []crypto.PrivKey {
		keys := make([]crypto.PrivKey, verifyFuzzKeys)
		for i := range keys {
			keys[i] = ed25519.GenPrivKeyFromSecret([]byte{'v', byte(i)})
		}
		return keys
	}()
)

// fuzzBytes hands out fuzz input one byte at a time, returning def once the
// input is exhausted so that short inputs still build a full commit.
type fuzzBytes struct{ b []byte }

func (r *fuzzBytes) next(def byte) byte {
	if len(r.b) == 0 {
		return def
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

// buildVerifyValSet builds a validator set from pool keys. A power byte at or
// above 0xF0 selects a power near the per-validator share of
// MaxTotalVotingPower, so totals close to the cap are exercised.
func buildVerifyValSet(r *fuzzBytes, keyIdxs []int) *ValidatorSet {
	vals := make([]*Validator, len(keyIdxs))
	for i, k := range keyIdxs {
		p := r.next(10)
		power := int64(p) + 1
		if p >= 0xF0 {
			power = MaxTotalVotingPower/int64(len(keyIdxs)) - int64(p&0x0F)
		}
		vals[i] = NewValidator(verifyFuzzPool[k].PubKey(), power)
	}
	return NewValidatorSet(vals)
}

func keyIndex(addr []byte) int {
	for i, k := range verifyFuzzPool {
		if bytes.Equal(k.PubKey().Address(), addr) {
			return i
		}
	}
	return -1
}

// buildVerifyCommit builds a commit for blockID whose signature at index i is
// nominally from vals.Validators[i]; each op byte turns that slot into an
// absent, nil, forged, replayed, duplicated or outsider signature.
func buildVerifyCommit(t *testing.T, r *fuzzBytes, vals *ValidatorSet, blockID BlockID, round int32, size int) *Commit {
	commit := &Commit{Height: verifyFuzzHeight, Round: round, BlockID: blockID, Signatures: make([]CommitSig, size)}
	sign := func(idx int, key crypto.PrivKey, flag BlockIDFlag, ts time.Time, height int64) []byte {
		v := &Vote{
			Type: cmtproto.PrecommitType, Height: height, Round: round,
			BlockID: CommitSig{BlockIDFlag: flag}.BlockID(blockID), Timestamp: ts,
			ValidatorAddress: key.PubKey().Address(), ValidatorIndex: int32(idx),
		}
		sig, err := key.Sign(VoteSignBytes(verifyFuzzChainID, v.ToProto()))
		require.NoError(t, err)
		return sig
	}
	for i := 0; i < size; i++ {
		key := verifyFuzzPool[verifyFuzzKeys-1] // outsider for slots past the set
		if i < vals.Size() {
			key = verifyFuzzPool[keyIndex(vals.Validators[i].Address)]
		}
		op := r.next(1)
		ts := verifyFuzzTime.Add(time.Duration(r.next(0)) * time.Second)
		flag := BlockIDFlagCommit
		if op&0x80 != 0 {
			flag = BlockIDFlagNil
		}
		signer, height := key, verifyFuzzHeight
		switch op & 0x0F {
		case 0: // absent
			commit.Signatures[i] = NewCommitSigAbsent()
			continue
		case 2: // another pool key signs and claims its own address
			signer = verifyFuzzPool[int(r.next(0))%verifyFuzzKeys]
		case 3: // replay of a signature for another height
			height++
		}
		cs := CommitSig{BlockIDFlag: flag, ValidatorAddress: signer.PubKey().Address(), Timestamp: ts}
		cs.Signature = sign(i, signer, flag, ts, height)
		switch op & 0x0F {
		case 4: // flip a bit of the signature
			cs.Signature[int(r.next(0))%len(cs.Signature)] ^= 1 << (r.next(0) % 8)
		case 5: // truncate the signature
			cs.Signature = cs.Signature[:1+int(r.next(0))%(len(cs.Signature)-1)]
		case 6: // sign with another key but keep the claimed address
			cs.Signature = sign(i, verifyFuzzPool[int(r.next(0))%verifyFuzzKeys], flag, ts, height)
		case 7: // timestamp changed after signing
			cs.Timestamp = cs.Timestamp.Add(time.Nanosecond)
		case 8: // flag flipped after signing
			if cs.BlockIDFlag == BlockIDFlagCommit {
				cs.BlockIDFlag = BlockIDFlagNil
			} else {
				cs.BlockIDFlag = BlockIDFlagCommit
			}
		case 9: // copy of an earlier slot (duplicate vote)
			if i > 0 {
				cs = commit.Signatures[int(r.next(0))%i]
			}
		}
		commit.Signatures[i] = cs
	}
	return commit
}

// refVerifyCommit is the reference for the verifyCommit* routines: it walks
// the signatures in order, skipping ignored ones, resolves each validator by
// index or by address, checks the signature through Vote.Verify, and accepts
// as soon as the tally exceeds needed (or only at the end with countAll).
// Any malformed or wrongly signed signature reached before acceptance fails.
func refVerifyCommit(vals *ValidatorSet, commit *Commit, needed int64,
	ignore, count func(CommitSig) bool, countAll, byIndex bool,
) bool {
	seen := map[string]bool{}
	var tally int64
	for idx, cs := range commit.Signatures {
		if ignore(cs) {
			continue
		}
		var val *Validator
		if byIndex {
			val = vals.Validators[idx]
			if !bytes.Equal(val.Address, cs.ValidatorAddress) {
				return false
			}
		} else {
			_, val = vals.GetByAddress(cs.ValidatorAddress)
			if val == nil {
				continue
			}
			if seen[string(val.Address)] {
				return false
			}
			seen[string(val.Address)] = true
		}
		if commit.GetVote(int32(idx)).Verify(verifyFuzzChainID, val.PubKey) != nil {
			return false
		}
		if count(cs) {
			tally += val.VotingPower
		}
		if !countAll && tally > needed {
			return true
		}
	}
	return tally > needed
}

// honestTally sums the power of distinct members of vals that produced a
// valid signature for the committed block. Any accepted commit must exceed
// the threshold on this tally alone, whatever the order of signatures.
func honestTally(vals *ValidatorSet, commit *Commit) int64 {
	seen := map[string]bool{}
	var tally int64
	for idx, cs := range commit.Signatures {
		if cs.BlockIDFlag != BlockIDFlagCommit {
			continue
		}
		_, val := vals.GetByAddress(cs.ValidatorAddress)
		if val == nil || seen[string(val.Address)] {
			continue
		}
		if commit.GetVote(int32(idx)).Verify(verifyFuzzChainID, val.PubKey) == nil {
			seen[string(val.Address)] = true
			tally += val.VotingPower
		}
	}
	return tally
}

// FuzzVerifyCommit builds a real validator set and a commit whose signatures
// are individually mutated, then checks every commit verification entry point
// (full, light, light-trusting, with and without counting all signatures)
// against a reference tally, and batch against single verification.
func FuzzVerifyCommit(f *testing.F) {
	f.Add([]byte{3, 10, 10, 10, 0, 0, 0xFF}, []byte{}, uint64(1), uint64(3))
	f.Add([]byte{4, 1, 1, 1, 200, 1, 0, 0x07}, []byte{1, 0, 0x81, 5, 4, 0, 0, 0, 1, 0}, uint64(2), uint64(3))
	f.Add([]byte{8, 0xF0, 0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 2, 7, 0xFF}, []byte{1, 0, 9, 0, 0, 2, 0, 7, 3, 0, 6, 0, 1}, uint64(1<<62), uint64(1))
	f.Add([]byte{2, 5, 5, 0, 6, 0x03}, []byte{1, 0, 1, 0}, uint64(0), uint64(0))
	f.Add([]byte{5, 1, 2, 3, 4, 5, 1, 0, 0x3F}, []byte{8, 0, 0x81, 1, 2, 9, 3, 0, 1, 0, 1, 0}, uint64(1), uint64(1))
	f.Fuzz(func(t *testing.T, setup, ops []byte, trustNum, trustDen uint64) {
		r := &fuzzBytes{b: setup}
		n := 1 + int(r.next(0))%verifyFuzzMaxVals
		keyIdxs := make([]int, n)
		for i := range keyIdxs {
			keyIdxs[i] = i
		}
		vals := buildVerifyValSet(r, keyIdxs)
		round := int32(r.next(0) % 4)
		size := n
		switch r.next(0) % 8 {
		case 6:
			size--
		case 7:
			size++
		}
		// The trusted set used for the trusting checks: any non-empty subset
		// of the pool, including outsiders, with its own powers.
		mask := r.next(0xFF)
		var trusted []int
		for i := 0; i < verifyFuzzKeys; i++ {
			if mask&(1<<(i%8)) != 0 && (i < 8 || mask&0x80 != 0) {
				trusted = append(trusted, i)
			}
		}
		if len(trusted) == 0 {
			trusted = []int{0}
		}
		trustedVals := buildVerifyValSet(r, trusted)

		blockID := makeBlockID(bytes.Repeat([]byte{1}, 32), 1, bytes.Repeat([]byte{2}, 32))
		commit := buildVerifyCommit(t, &fuzzBytes{b: ops}, vals, blockID, round, size)
		// Every production caller passes commits that went through
		// CommitFromProto / ValidateBasic; the verifiers rely on that.
		if commit.ValidateBasic() != nil {
			return
		}
		trust := cmtmath.Fraction{Numerator: trustNum, Denominator: trustDen}

		ignoreAbsent := func(c CommitSig) bool { return c.BlockIDFlag == BlockIDFlagAbsent }
		ignoreNonCommit := func(c CommitSig) bool { return c.BlockIDFlag != BlockIDFlagCommit }
		countCommit := func(c CommitSig) bool { return c.BlockIDFlag == BlockIDFlagCommit }
		countAny := func(CommitSig) bool { return true }
		needed := vals.TotalVotingPower() * 2 / 3
		sizeOK := size == vals.Size()

		check := func(name string, err error, v *ValidatorSet, need int64, ignore, count func(CommitSig) bool, countAll, byIndex, pre bool) {
			want := pre && refVerifyCommit(v, commit, need, ignore, count, countAll, byIndex)
			require.Equal(t, want, err == nil, "%s: reference=%v err=%v", name, want, err)
			if err == nil {
				require.Greater(t, honestTally(v, commit), need, "%s accepted without enough valid signatures", name)
			}
			if pre && len(commit.Signatures) >= batchVerifyThreshold {
				single := verifyCommitSingle(verifyFuzzChainID, v, commit, need, ignore, count, countAll, byIndex)
				batch := verifyCommitBatch(verifyFuzzChainID, v, commit, need, ignore, count, countAll, byIndex)
				require.Equal(t, single == nil, batch == nil, "%s: single=%v batch=%v", name, single, batch)
			}
		}

		check("VerifyCommit", vals.VerifyCommit(verifyFuzzChainID, blockID, verifyFuzzHeight, commit),
			vals, needed, ignoreAbsent, countCommit, true, true, sizeOK)
		check("VerifyCommitLight", vals.VerifyCommitLight(verifyFuzzChainID, blockID, verifyFuzzHeight, commit),
			vals, needed, ignoreNonCommit, countAny, false, true, sizeOK)
		check("VerifyCommitLightAllSignatures", vals.VerifyCommitLightAllSignatures(verifyFuzzChainID, blockID, verifyFuzzHeight, commit),
			vals, needed, ignoreNonCommit, countAny, true, true, sizeOK)
		require.Error(t, vals.VerifyCommit(verifyFuzzChainID, blockID, verifyFuzzHeight+1, commit))
		require.Error(t, vals.VerifyCommitLight(verifyFuzzChainID, BlockID{}, verifyFuzzHeight, commit))

		// The trusting threshold is total*numerator/denominator, rejected
		// if the product does not fit in an int64. The numerator and
		// denominator are reinterpreted as int64, as the production code does.
		num := int64(trustNum)
		product := new(big.Int).Mul(big.NewInt(trustedVals.TotalVotingPower()), big.NewInt(num))
		trustOK := trustDen != 0 && new(big.Int).Abs(product).Cmp(big.NewInt(math.MaxInt64)) <= 0
		var need int64
		if trustOK {
			need = product.Int64() / int64(trustDen)
		}
		check("VerifyCommitLightTrusting", trustedVals.VerifyCommitLightTrusting(verifyFuzzChainID, commit, trust),
			trustedVals, need, ignoreNonCommit, countAny, false, false, trustOK)
		check("VerifyCommitLightTrustingAllSignatures", trustedVals.VerifyCommitLightTrustingAllSignatures(verifyFuzzChainID, commit, trust),
			trustedVals, need, ignoreNonCommit, countAny, true, false, trustOK)
	})
}

// extendedCommitSeed is a valid extended commit with vote extensions, signed
// by a three-validator set.
func extendedCommitSeed(tb testing.TB) []byte {
	tb.Helper()
	valSet, pvs := fuzzValidators(3)
	blockID := BlockID{Hash: make([]byte, 32), PartSetHeader: PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	voteSet := NewExtendedVoteSet(verifyFuzzChainID, 9, 0, cmtproto.PrecommitType, valSet)
	ec, err := MakeExtCommit(blockID, 9, 0, voteSet, pvs, verifyFuzzTime, true)
	require.NoError(tb, err)
	raw, err := proto.Marshal(ec.ToProto())
	require.NoError(tb, err)
	return raw
}

// checkExtendedCommitBytes decodes an extended commit the way blocksync does
// and checks the helpers applied to it before it is stored. It returns the
// decoded extended commit, or the decode error.
func checkExtendedCommitBytes(t *testing.T, raw []byte) (*ExtendedCommit, error) {
	var pb cmtproto.ExtendedCommit
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	ec, err := ExtendedCommitFromProto(&pb)
	if err != nil {
		return nil, err
	}
	c := ec.ToCommit()
	require.NoError(t, c.ValidateBasic(), "valid extended commit converted to an invalid commit")
	require.Equal(t, ec.Size(), c.Size())
	for _, enabled := range []bool{true, false} {
		if ec.EnsureExtensions(enabled) != nil {
			continue
		}
		for _, ecs := range ec.ExtendedSignatures {
			if enabled && ecs.BlockIDFlag == BlockIDFlagCommit {
				require.NotEmpty(t, ecs.ExtensionSignature)
			}
			if !enabled {
				require.Empty(t, ecs.Extension)
				require.Empty(t, ecs.ExtensionSignature)
			}
		}
	}
	require.Equal(t, ec.Size(), ec.BitArray().Size())
	if ec.Height >= 1 {
		for i := range ec.ExtendedSignatures {
			_ = ec.GetExtendedVote(int32(i))
		}
	}
	back, err := ExtendedCommitFromProto(ec.ToProto())
	require.NoError(t, err)
	require.Equal(t, c.Hash(), back.ToCommit().Hash(), "commit hash changed across a proto round trip")
	return ec, nil
}

// Edit kinds of an extended commit, after the commit ones.
const (
	extCommitEditExtension = commitEditKinds + iota
	extCommitEditExtensionSig
	extCommitEditResign // neutral
	extCommitEditKinds
)

// resignExtensions re-signs the vote extension of every commit-flag
// signature whose address belongs to a pool key.
func resignExtensions(t *testing.T, pb *cmtproto.ExtendedCommit) {
	for i := range pb.ExtendedSignatures {
		ecs := &pb.ExtendedSignatures[i]
		key := poolKey(ecs.ValidatorAddress)
		if key == nil || ecs.BlockIdFlag != cmtproto.BlockIDFlagCommit {
			continue
		}
		v := precommit(pb.Height, pb.Round, pb.BlockID, ecs.BlockIdFlag, ecs.Timestamp)
		v.Extension = ecs.Extension
		sig, err := key.Sign(VoteExtensionSignBytes(verifyFuzzChainID, v))
		require.NoError(t, err)
		ecs.ExtensionSignature = sig
	}
}

// FuzzExtendedCommitFromProto decodes an extended commit the way blocksync
// does and checks the helpers applied to it before it is stored.
//
// With edits, it starts from a valid three-validator extended commit and
// edits the commit fields, individual signatures, extensions and extension
// signatures, optionally re-signing votes and extensions. Re-signed
// commit-flag votes must verify with their extensions, and a re-signed
// commit with enough power must pass VerifyCommitLight.
func FuzzExtendedCommitFromProto(f *testing.F) {
	seed := extendedCommitSeed(f)
	vals, _ := fuzzValidators(3)
	f.Add(seed, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{extCommitEditResign})
	f.Add([]byte{}, []byte{extCommitEditExtension, 0, 3, 3, 4, 'v', 'o', 't', 'e', extCommitEditResign})
	f.Add([]byte{}, []byte{commitEditFlag, 1, 0, 2, 2, extCommitEditResign})
	f.Add([]byte{}, []byte{commitEditRound, 0, 1, extCommitEditResign})
	f.Add([]byte{}, []byte{extCommitEditExtensionSig, 2, 0, 4})
	f.Add([]byte{}, []byte{commitEditAbsent, 0, 0, commitEditDupSig, 1, 0, extCommitEditResign})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkExtendedCommitBytes(t, raw)
			return
		}
		var pb cmtproto.ExtendedCommit
		unmarshalFixture(t, seed, &pb)
		e := newFuzzEditor(edits)
		resign := false
		e.run(extCommitEditKinds, extCommitEditResign, func(kind int) {
			switch kind {
			case extCommitEditExtension, extCommitEditExtensionSig:
				if i := e.index(len(pb.ExtendedSignatures)); i >= 0 {
					ecs := &pb.ExtendedSignatures[i]
					if kind == extCommitEditExtension {
						ecs.Extension = e.bytes(ecs.Extension, 255)
					} else {
						ecs.ExtensionSignature = e.bytes(ecs.ExtensionSignature, MaxSignatureSize+1)
					}
				}
			case extCommitEditResign:
				resign = true
			default:
				editCommit(e, &pb.Height, &pb.Round, &pb.BlockID, &pb.ExtendedSignatures, extCommitSigOf,
					cmtproto.ExtendedCommitSig{BlockIdFlag: cmtproto.BlockIDFlagAbsent}, kind)
			}
		})
		if resign {
			resignCommit(t, pb.Height, pb.Round, pb.BlockID, pb.ExtendedSignatures, extCommitSigOf)
			resignExtensions(t, &pb)
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		ec, err := checkExtendedCommitBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "re-signed valid extended commit rejected")
			require.NoError(t, ec.EnsureExtensions(true))
		}
		if err != nil || (!resign && e.mutated) || ec.Height < 1 {
			return
		}
		for i, ecs := range ec.ExtendedSignatures {
			if key := poolKey(ecs.ValidatorAddress); key != nil && ecs.BlockIDFlag == BlockIDFlagCommit {
				require.NoError(t, ec.GetExtendedVote(int32(i)).VerifyVoteAndExtension(verifyFuzzChainID, key.PubKey()),
					"re-signed vote or extension does not verify")
			}
		}
		c := ec.ToCommit()
		if wantCommitLight(vals, c) {
			require.NoError(t, vals.VerifyCommitLight(verifyFuzzChainID, c.BlockID, c.Height, c),
				"re-signed commit with enough power rejected")
		}
	})
}

// lightBlockSeed is a valid light block signed by a three-validator set.
func lightBlockSeed(tb testing.TB) []byte {
	tb.Helper()
	pvs := make([]PrivValidator, 3)
	vals := make([]*Validator, 3)
	for i := range pvs {
		pvs[i] = NewMockPVWithParams(verifyFuzzPool[i], false, false)
		vals[i] = NewValidator(verifyFuzzPool[i].PubKey(), int64(i+1))
	}
	valSet := NewValidatorSet(vals)
	ordered := make([]PrivValidator, len(pvs))
	for i, v := range valSet.Validators {
		ordered[i] = pvs[keyIndex(v.Address)]
	}
	hash := bytes.Repeat([]byte{3}, 32)
	h := Header{
		Version:        cmtversion.Consensus{Block: version.BlockProtocol, App: 1},
		ChainID:        verifyFuzzChainID,
		Height:         verifyFuzzHeight,
		Time:           verifyFuzzTime,
		LastBlockID:    BlockID{Hash: hash, PartSetHeader: PartSetHeader{Total: 1, Hash: hash}},
		LastCommitHash: hash, DataHash: hash, ConsensusHash: hash, AppHash: hash,
		LastResultsHash: hash, EvidenceHash: hash,
		ValidatorsHash:     valSet.Hash(),
		NextValidatorsHash: valSet.Hash(),
		ProposerAddress:    valSet.GetProposer().Address,
	}
	blockID := BlockID{Hash: h.Hash(), PartSetHeader: PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	voteSet := NewVoteSet(verifyFuzzChainID, h.Height, 0, cmtproto.PrecommitType, valSet)
	ec, err := MakeExtCommit(blockID, h.Height, 0, voteSet, ordered, verifyFuzzTime, false)
	require.NoError(tb, err)
	lb := &LightBlock{SignedHeader: &SignedHeader{Header: &h, Commit: ec.ToCommit()}, ValidatorSet: valSet}
	require.NoError(tb, lb.ValidateBasic(verifyFuzzChainID))
	pb, err := lb.ToProto()
	require.NoError(tb, err)
	raw, err := proto.Marshal(pb)
	require.NoError(tb, err)
	return raw
}

// checkLightBlockBytes decodes a light block the way the light client HTTP
// provider and state sync do, validates it, and runs the light-client commit
// check against its own validator set. It returns the decoded light block,
// or the first decode or validation error.
func checkLightBlockBytes(t *testing.T, raw []byte) (*LightBlock, error) {
	var pb cmtproto.LightBlock
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	lb, err := LightBlockFromProto(&pb)
	if err != nil {
		return nil, err
	}
	if lb.SignedHeader != nil {
		_ = lb.SignedHeader.ValidateBasic(verifyFuzzChainID)
		sh, err := SignedHeaderFromProto(lb.SignedHeader.ToProto())
		require.NoError(t, err)
		require.Equal(t, lb.Hash(), sh.Hash())
	}
	if err := lb.ValidateBasic(verifyFuzzChainID); err != nil {
		return nil, err
	}
	checkValidatorSet(t, lb.ValidatorSet)
	_ = lb.ValidatorSet.VerifyCommitLight(verifyFuzzChainID, lb.Commit.BlockID, lb.Height, lb.Commit)
	again, err := lb.ToProto()
	require.NoError(t, err)
	back, err := LightBlockFromProto(again)
	require.NoError(t, err)
	require.NoError(t, back.ValidateBasic(verifyFuzzChainID))
	require.Equal(t, lb.Hash(), back.Hash(), "header hash changed across a proto round trip")
	require.Equal(t, lb.ValidatorSet.Hash(), back.ValidatorSet.Hash(), "validator set hash changed across a proto round trip")
	return lb, nil
}

// proposer edits the proposer of a validator set: a copy of a member, nil,
// or a changed priority or address.
func (e *fuzzEditor) proposer(vs *cmtproto.ValidatorSet) {
	switch e.r.Intn(4) {
	case 0:
		if i := e.index(len(vs.Validators)); i >= 0 {
			v := *vs.Validators[i]
			vs.Proposer = &v
		}
	case 1:
		vs.Proposer = nil
	case 2:
		if vs.Proposer != nil {
			vs.Proposer.ProposerPriority = nearInt(e, vs.Proposer.ProposerPriority, 0, math.MaxInt64, math.MinInt64)
		}
	default:
		if vs.Proposer != nil {
			vs.Proposer.Address = e.bytes(vs.Proposer.Address, 64)
		}
	}
}

// Edit kinds of a light block.
const (
	lightEditHeader = iota
	lightEditCommit
	lightEditValidators
	lightEditProposer
	lightEditNilValidatorSet
	lightEditRelink // neutral: recompute the validators hash and the commit's block hash
	lightEditResign // neutral: re-sign the commit
	lightEditKinds
)

// relinkLightBlock points the header at the validator set and the commit at
// the header, when both still decode.
func relinkLightBlock(pb *cmtproto.LightBlock) {
	sh := pb.SignedHeader
	if pb.ValidatorSet != nil {
		if vals, err := ValidatorSetFromProto(pb.ValidatorSet); err == nil {
			sh.Header.ValidatorsHash = vals.Hash()
		}
	}
	if !encodable(sh.Header) {
		return
	}
	if h, err := HeaderFromProto(sh.Header); err == nil {
		sh.Commit.BlockID.Hash = h.Hash()
	}
}

// FuzzLightBlockFromProto decodes a light block the way the light client
// HTTP provider and state sync do, validates it, and runs the light-client
// commit check against its own validator set.
//
// With edits, it starts from a valid light block and edits the header, the
// commit and the validator set, optionally relinking the hashes and
// re-signing the commit. A relinked, re-signed light block that validates and
// still carries more than two thirds of the power must pass
// VerifyCommitLight.
func FuzzLightBlockFromProto(f *testing.F) {
	seed := lightBlockSeed(f)
	f.Add(seed, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{lightEditRelink, lightEditResign})
	f.Add([]byte{}, []byte{lightEditHeader, 1, 0, 1, 0, lightEditRelink, lightEditResign})
	f.Add([]byte{}, []byte{lightEditValidators, valEditPower, 0, 0, 0, 5, lightEditRelink, lightEditResign})
	f.Add([]byte{}, []byte{lightEditValidators, valEditDrop, 0, 0, lightEditRelink, lightEditResign})
	f.Add([]byte{}, []byte{lightEditCommit, commitEditAbsent, 0, 0, lightEditResign})
	f.Add([]byte{}, []byte{lightEditCommit, commitEditRound, 0, 1, lightEditRelink, lightEditResign})
	f.Add([]byte{}, []byte{lightEditProposer, 0, 2, 0})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkLightBlockBytes(t, raw)
			return
		}
		var pb cmtproto.LightBlock
		unmarshalFixture(t, seed, &pb)
		sh := pb.SignedHeader
		e := newFuzzEditor(edits)
		relink, resign := false, false
		e.run(lightEditKinds, lightEditRelink, func(kind int) {
			switch kind {
			case lightEditHeader:
				e.header(sh.Header)
			case lightEditCommit:
				e.commit(sh.Commit, e.r.Intn(commitEditKinds))
			case lightEditValidators:
				if pb.ValidatorSet != nil {
					e.validators(t, &pb.ValidatorSet.Validators, e.r.Intn(valEditKinds))
				}
			case lightEditProposer:
				if pb.ValidatorSet != nil {
					e.proposer(pb.ValidatorSet)
				}
			case lightEditNilValidatorSet:
				pb.ValidatorSet = nil
			case lightEditRelink:
				relink = true
			case lightEditResign:
				resign = true
			}
		})
		if relink {
			relinkLightBlock(&pb)
		}
		if resign {
			c := sh.Commit
			resignCommit(t, c.Height, c.Round, c.BlockID, c.Signatures, commitSigOf)
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		lb, err := checkLightBlockBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "relinked, re-signed valid light block rejected")
		}
		if err == nil && (resign || !e.mutated) && wantCommitLight(lb.ValidatorSet, lb.Commit) {
			require.NoError(t, lb.ValidatorSet.VerifyCommitLight(verifyFuzzChainID, lb.Commit.BlockID, lb.Height, lb.Commit),
				"re-signed light block with enough power rejected")
		}
	})
}

// checkValidatorSet asserts the invariants of a decoded, valid validator set.
func checkValidatorSet(t *testing.T, vals *ValidatorSet) {
	total, err := vals.TotalVotingPowerSafe()
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(0))
	require.LessOrEqual(t, total, MaxTotalVotingPower)
	var sum int64
	for _, v := range vals.Validators {
		sum += v.VotingPower
	}
	require.Equal(t, sum, total)
	_, proposer := vals.GetByAddress(vals.GetProposer().Address)
	require.NotNil(t, proposer, "proposer not in the set")
}

// checkValidatorSetBytes decodes a validator set the way the light client
// and state sync do. It returns the decoded set, or the decode error.
func checkValidatorSetBytes(t *testing.T, raw []byte) (*ValidatorSet, error) {
	var pb cmtproto.ValidatorSet
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil, err
	}
	vals, err := ValidatorSetFromProto(&pb)
	if err != nil {
		return nil, err
	}
	checkValidatorSet(t, vals)
	_ = vals.Hash()
	again, err := vals.ToProto()
	require.NoError(t, err)
	back, err := ValidatorSetFromProto(again)
	require.NoError(t, err)
	require.Equal(t, vals.Hash(), back.Hash(), "validator set hash changed across a proto round trip")
	return vals, nil
}

// Edit kinds of a validator set, after the validator list ones.
const (
	valSetEditProposer = valEditKinds + iota
	valSetEditTotalPower
	valSetEditReaddress // neutral: rederive every address from its public key
	valSetEditKinds
)

// readdress sets the address of v to the one derived from its public key.
func readdress(v *cmtproto.Validator) {
	if v == nil {
		return
	}
	if pk, err := cryptoenc.PubKeyFromProto(v.PubKey); err == nil {
		v.Address = pk.Address()
	}
}

// FuzzValidatorSetFromProto decodes a validator set the way the light client
// and state sync do.
//
// With edits, it starts from a valid three-validator set and drops,
// duplicates, adds, reorders and edits validators (power, priority, address,
// public key), the proposer and the total power field, optionally rederiving
// addresses from public keys so that key swaps still decode.
func FuzzValidatorSetFromProto(f *testing.F) {
	var pbLight cmtproto.LightBlock
	require.NoError(f, proto.Unmarshal(lightBlockSeed(f), &pbLight))
	seed, err := proto.Marshal(pbLight.ValidatorSet)
	require.NoError(f, err)
	f.Add(seed, []byte(nil))
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{}, []byte{valSetEditReaddress})
	f.Add([]byte{}, []byte{valEditAdd, 5, 7})
	f.Add([]byte{}, []byte{valEditDup, 1, 0})
	f.Add([]byte{}, []byte{valEditPubKey, 0, 0, 6, valSetEditReaddress})
	f.Add([]byte{}, []byte{valEditPower, 1, 0, 2, 3})
	f.Add([]byte{}, []byte{valSetEditProposer, 1})
	f.Fuzz(func(t *testing.T, raw, edits []byte) {
		if len(edits) == 0 {
			_, _ = checkValidatorSetBytes(t, raw)
			return
		}
		var pb cmtproto.ValidatorSet
		unmarshalFixture(t, seed, &pb)
		e := newFuzzEditor(edits)
		addresses := false
		e.run(valSetEditKinds, valSetEditReaddress, func(kind int) {
			switch kind {
			case valSetEditProposer:
				e.proposer(&pb)
			case valSetEditTotalPower:
				pb.TotalVotingPower = nearInt(e, pb.TotalVotingPower, 0, -1, math.MaxInt64)
			case valSetEditReaddress:
				addresses = true
			default:
				e.validators(t, &pb.Validators, kind)
			}
		})
		if addresses {
			for _, v := range pb.Validators {
				readdress(v)
			}
			readdress(pb.Proposer)
		}
		raw, err := proto.Marshal(&pb)
		if err != nil {
			return
		}
		vals, err := checkValidatorSetBytes(t, raw)
		if !e.mutated {
			require.NoError(t, err, "valid validator set rejected")
		}
		if err == nil {
			for _, v := range vals.Validators {
				idx, _ := vals.GetByAddress(v.Address)
				require.GreaterOrEqual(t, idx, int32(0), "validator not found by its address")
			}
		}
	})
}

// TestVerifyCommitFuzzSeed keeps the first seed meaningful: a fully signed
// commit from a three-validator set verifies on every path.
func TestVerifyCommitFuzzSeed(t *testing.T) {
	vals := buildVerifyValSet(&fuzzBytes{b: []byte{10, 10, 10}}, []int{0, 1, 2})
	blockID := makeBlockID(bytes.Repeat([]byte{1}, 32), 1, bytes.Repeat([]byte{2}, 32))
	commit := buildVerifyCommit(t, &fuzzBytes{}, vals, blockID, 0, vals.Size())
	require.NoError(t, commit.ValidateBasic())
	require.NoError(t, vals.VerifyCommit(verifyFuzzChainID, blockID, verifyFuzzHeight, commit))
	require.NoError(t, vals.VerifyCommitLight(verifyFuzzChainID, blockID, verifyFuzzHeight, commit))
	require.NoError(t, vals.VerifyCommitLightTrusting(verifyFuzzChainID, commit, cmtmath.Fraction{Numerator: 1, Denominator: 3}))
}

// TestVerifyCommitFuzzAbsentSlot checks that the harness encodes an absent
// slot as a valid absent CommitSig, so absent votes reach the verifiers.
func TestVerifyCommitFuzzAbsentSlot(t *testing.T) {
	vals := buildVerifyValSet(&fuzzBytes{b: []byte{10, 10, 10, 10}}, []int{0, 1, 2, 3})
	blockID := makeBlockID(bytes.Repeat([]byte{1}, 32), 1, bytes.Repeat([]byte{2}, 32))
	// Slot 0 absent, slots 1-3 honest commits.
	commit := buildVerifyCommit(t, &fuzzBytes{b: []byte{0, 0, 1, 0, 1, 0, 1, 0}}, vals, blockID, 0, vals.Size())
	require.Equal(t, BlockIDFlagAbsent, commit.Signatures[0].BlockIDFlag)
	require.NoError(t, commit.ValidateBasic())
	require.NoError(t, vals.VerifyCommit(verifyFuzzChainID, blockID, verifyFuzzHeight, commit))
	require.NoError(t, vals.VerifyCommitLight(verifyFuzzChainID, blockID, verifyFuzzHeight, commit))
}
