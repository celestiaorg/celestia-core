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

// extendedCommitSeed is a valid extended commit with vote extensions.
func extendedCommitSeed(tb testing.TB) []byte {
	tb.Helper()
	pv := NewMockPVWithParams(verifyFuzzPool[0], false, false)
	pubKey, err := pv.GetPubKey()
	require.NoError(tb, err)
	valSet := NewValidatorSet([]*Validator{NewValidator(pubKey, 10)})
	blockID := BlockID{Hash: make([]byte, 32), PartSetHeader: PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	voteSet := NewExtendedVoteSet(verifyFuzzChainID, 9, 0, cmtproto.PrecommitType, valSet)
	ec, err := MakeExtCommit(blockID, 9, 0, voteSet, []PrivValidator{pv}, verifyFuzzTime, true)
	require.NoError(tb, err)
	raw, err := proto.Marshal(ec.ToProto())
	require.NoError(tb, err)
	return raw
}

// FuzzExtendedCommitFromProto decodes an extended commit the way blocksync
// does and checks the helpers applied to it before it is stored.
func FuzzExtendedCommitFromProto(f *testing.F) {
	f.Add(extendedCommitSeed(f))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.ExtendedCommit
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		ec, err := ExtendedCommitFromProto(&pb)
		if err != nil {
			return
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

// FuzzLightBlockFromProto decodes a light block the way the light client
// HTTP provider and state sync do, validates it, and runs the light-client
// commit check against its own validator set.
func FuzzLightBlockFromProto(f *testing.F) {
	f.Add(lightBlockSeed(f))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.LightBlock
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		lb, err := LightBlockFromProto(&pb)
		if err != nil {
			return
		}
		if lb.SignedHeader != nil {
			_ = lb.SignedHeader.ValidateBasic(verifyFuzzChainID)
			sh, err := SignedHeaderFromProto(lb.SignedHeader.ToProto())
			require.NoError(t, err)
			require.Equal(t, lb.Hash(), sh.Hash())
		}
		if err := lb.ValidateBasic(verifyFuzzChainID); err != nil {
			return
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

// FuzzValidatorSetFromProto decodes a validator set the way the light client
// and state sync do.
func FuzzValidatorSetFromProto(f *testing.F) {
	var pb cmtproto.LightBlock
	require.NoError(f, proto.Unmarshal(lightBlockSeed(f), &pb))
	seed, err := proto.Marshal(pb.ValidatorSet)
	require.NoError(f, err)
	f.Add(seed)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.ValidatorSet
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		vals, err := ValidatorSetFromProto(&pb)
		if err != nil {
			return
		}
		checkValidatorSet(t, vals)
		_ = vals.Hash()
		again, err := vals.ToProto()
		require.NoError(t, err)
		back, err := ValidatorSetFromProto(again)
		require.NoError(t, err)
		require.Equal(t, vals.Hash(), back.Hash(), "validator set hash changed across a proto round trip")
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
