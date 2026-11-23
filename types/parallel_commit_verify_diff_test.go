package types

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// diffValidators builds a deterministic validator set of size n from rng with
// random voting powers and returns the set and the signers by address.
func diffValidators(rng *rand.Rand, n int) (*ValidatorSet, map[string]PrivValidator) {
	vals := make([]*Validator, n)
	privs := make(map[string]PrivValidator, n)
	for i := range vals {
		secret := make([]byte, 16)
		binary.BigEndian.PutUint64(secret, rng.Uint64())
		binary.BigEndian.PutUint64(secret[8:], rng.Uint64())
		pk := ed25519.GenPrivKeyFromSecret(secret)
		vals[i] = NewValidator(pk.PubKey(), 1+rng.Int63n(1000))
		privs[string(vals[i].Address)] = NewMockPVWithParams(pk, false, false)
	}
	return NewValidatorSet(vals), privs
}

// diffCommit signs a commit for blockID where each validator votes for the
// block, nil or is absent according to rng.
func diffCommit(t *testing.T, rng *rand.Rand, vals *ValidatorSet, privs map[string]PrivValidator, blockID BlockID, height int64) *Commit {
	t.Helper()
	base := time.Unix(1700000000, 0).UTC()
	sigs := make([]CommitSig, len(vals.Validators))
	for i, val := range vals.Validators {
		r := rng.Intn(10)
		if r == 0 {
			sigs[i] = NewCommitSigAbsent()
			continue
		}
		vote := &Vote{
			Type:             cmtproto.PrecommitType,
			Height:           height,
			Round:            1,
			Timestamp:        base.Add(time.Duration(rng.Intn(1000)) * time.Millisecond),
			ValidatorAddress: val.Address,
			ValidatorIndex:   int32(i),
		}
		if r != 1 { // r == 1 votes nil
			vote.BlockID = blockID
		}
		v := vote.ToProto()
		require.NoError(t, privs[string(val.Address)].SignVote(pcvChainID, v))
		vote.Signature = v.Signature
		sigs[i] = vote.CommitSig()
	}
	return &Commit{Height: height, Round: 1, BlockID: blockID, Signatures: sigs}
}

func diffBlockID(rng *rand.Rand) BlockID {
	h := make([]byte, tmhash.Size)
	p := make([]byte, tmhash.Size)
	rng.Read(h)
	rng.Read(p)
	return BlockID{Hash: h, PartSetHeader: PartSetHeader{Total: 1 + uint32(rng.Intn(100)), Hash: p}}
}

// diffCorrupt applies one random byzantine mutation to a copy of commit and
// returns the copy and a description of the mutation.
func diffCorrupt(rng *rand.Rand, c *Commit) (*Commit, string) {
	cc := *c
	cc.Signatures = make([]CommitSig, len(c.Signatures))
	copy(cc.Signatures, c.Signatures)
	n := len(cc.Signatures)
	i, j := rng.Intn(n), rng.Intn(n)
	switch k := rng.Intn(16); k {
	case 0:
		return &cc, "none"
	case 1:
		s := append([]byte(nil), cc.Signatures[i].Signature...)
		if len(s) > 0 {
			s[rng.Intn(len(s))] ^= byte(1 + rng.Intn(255))
		}
		cc.Signatures[i].Signature = s
		return &cc, fmt.Sprintf("flip sig byte %d", i)
	case 2:
		s := cc.Signatures[i].Signature
		cc.Signatures[i].Signature = append([]byte(nil), s[:len(s)/2]...)
		return &cc, fmt.Sprintf("truncate sig %d", i)
	case 3:
		cc.Signatures[i].Signature = nil
		return &cc, fmt.Sprintf("nil sig %d", i)
	case 4:
		cc.Signatures[i].ValidatorAddress, cc.Signatures[j].ValidatorAddress =
			cc.Signatures[j].ValidatorAddress, cc.Signatures[i].ValidatorAddress
		return &cc, fmt.Sprintf("swap addr %d %d", i, j)
	case 5:
		switch cc.Signatures[i].BlockIDFlag {
		case BlockIDFlagCommit:
			cc.Signatures[i].BlockIDFlag = BlockIDFlagNil
		case BlockIDFlagNil:
			cc.Signatures[i].BlockIDFlag = BlockIDFlagCommit
		}
		return &cc, fmt.Sprintf("toggle commit/nil flag %d", i)
	case 6:
		for m := 0; m < n/3+1; m++ {
			cc.Signatures[rng.Intn(n)] = NewCommitSigAbsent()
		}
		return &cc, "drop ~1/3 to absent"
	case 7:
		cc.Signatures[i].Timestamp = cc.Signatures[i].Timestamp.Add(time.Nanosecond)
		return &cc, fmt.Sprintf("timestamp %d", i)
	case 8:
		cc.Signatures[i].Signature, cc.Signatures[j].Signature = cc.Signatures[j].Signature, cc.Signatures[i].Signature
		return &cc, fmt.Sprintf("swap sigs %d %d", i, j)
	case 9:
		cc.BlockID = diffBlockID(rng)
		return &cc, "wrong commit block id"
	case 10:
		cc.Height++
		return &cc, "wrong height"
	case 11:
		cc.Signatures = cc.Signatures[:n-1]
		return &cc, "missing signature"
	case 12:
		a := make([]byte, 20)
		rng.Read(a)
		cc.Signatures[i].ValidatorAddress = a
		return &cc, fmt.Sprintf("random addr %d", i)
	case 13:
		for m := 0; m < 1+rng.Intn(5); m++ {
			p := rng.Intn(n)
			s := append([]byte(nil), cc.Signatures[p].Signature...)
			if len(s) > 0 {
				s[len(s)-1] ^= 0x80
			}
			cc.Signatures[p].Signature = s
		}
		return &cc, "flip several sigs"
	case 14:
		cc.Signatures[i].BlockIDFlag = BlockIDFlag(4 + rng.Intn(10))
		return &cc, fmt.Sprintf("unknown flag %d", i)
	default:
		if cc.Signatures[i].BlockIDFlag == BlockIDFlagAbsent {
			// absent entry carrying a (copied) signature and address
			cc.Signatures[i].Signature = cc.Signatures[j].Signature
			cc.Signatures[i].ValidatorAddress = cc.Signatures[j].ValidatorAddress
		}
		return &cc, fmt.Sprintf("absent with data %d", i)
	}
}

type diffOutcome struct {
	typ, msg string
}

func diffRun(f func() error) (out diffOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out = diffOutcome{typ: "panic", msg: fmt.Sprint(r)}
		}
	}()
	err := f()
	if err == nil {
		return diffOutcome{}
	}
	return diffOutcome{typ: fmt.Sprintf("%T", err), msg: err.Error()}
}

// TestParallelCommitVerify_RandomDifferential compares every commit
// verification entry point with the parallel path off and on (at several
// GOMAXPROCS values, which changes the chunking) for random validator sets,
// random vote mixes and random byzantine corruptions. The accept/reject
// decision, the error type and the error message must be identical.
func TestParallelCommitVerify_RandomDifferential(t *testing.T) {
	iters := 300
	if testing.Short() {
		iters = 60
	}
	rng := rand.New(rand.NewSource(20261123))
	prevPar := SetParallelCommitVerify(false)
	defer SetParallelCommitVerify(prevPar)
	prevProcs := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(prevProcs)

	parBefore := parallelCommitVerifyAccepted.Load()
	accepted, rejected := 0, 0
	for it := 0; it < iters; it++ {
		n := 1 + rng.Intn(120)
		vals, privs := diffValidators(rng, n)
		blockID := diffBlockID(rng)
		height := 1 + rng.Int63n(1000)
		commit, what := diffCorrupt(rng, diffCommit(t, rng, vals, privs, blockID, height))
		// a second, partially overlapping set for the by-address (trusting) paths
		other, _ := diffValidators(rng, 1+rng.Intn(20))
		trustVals := NewValidatorSet(append(other.Copy().Validators, vals.Copy().Validators[:n/2]...))
		trust := cmtmath.Fraction{Numerator: 1, Denominator: 3}

		fns := []struct {
			name string
			fn   func() error
		}{
			{"VerifyCommit", func() error { return vals.VerifyCommit(pcvChainID, blockID, height, commit) }},
			{"VerifyCommitLight", func() error { return VerifyCommitLight(pcvChainID, vals, blockID, height, commit) }},
			{"VerifyCommitLightAllSignatures", func() error {
				return VerifyCommitLightAllSignatures(pcvChainID, vals, blockID, height, commit)
			}},
			{"VerifyCommitLightTrusting", func() error { return VerifyCommitLightTrusting(pcvChainID, vals, commit, trust) }},
			{"VerifyCommitLightTrustingAllSignatures", func() error {
				return VerifyCommitLightTrustingAllSignatures(pcvChainID, vals, commit, trust)
			}},
			{"VerifyCommitLightTrusting/other", func() error {
				return VerifyCommitLightTrusting(pcvChainID, trustVals, commit, trust)
			}},
			{"VerifyCommitLightTrustingAllSignatures/other", func() error {
				return VerifyCommitLightTrustingAllSignatures(pcvChainID, trustVals, commit, trust)
			}},
		}
		for _, f := range fns {
			SetParallelCommitVerify(false)
			runtime.GOMAXPROCS(prevProcs)
			want := diffRun(f.fn)
			// The stock code panics on unknown BlockIDFlags (Commit.ValidateBasic
			// rejects them before any verification); the parallel path must then
			// behave identically, so panics are compared like errors.
			SetParallelCommitVerify(true)
			for _, procs := range []int{1, 2, 3, 8} {
				runtime.GOMAXPROCS(procs)
				got := diffRun(f.fn)
				require.Equal(t, want, got, "it=%d n=%d procs=%d %s (%s)", it, n, procs, f.name, what)
			}
			if f.name == "VerifyCommit" {
				if want.typ == "" {
					accepted++
				} else {
					rejected++
				}
			}
		}
	}
	t.Logf("VerifyCommit accepted=%d rejected=%d", accepted, rejected)
	require.Positive(t, accepted)
	require.Positive(t, rejected)
	parAccepted := parallelCommitVerifyAccepted.Load() - parBefore
	t.Logf("parallel path accepted %d commits", parAccepted)
	require.Positive(t, parAccepted, "the parallel path must actually be exercised")
}

// TestParallelCommitVerify_BoundedWork checks that the parallel pass cannot
// be made to do more signature work than the validator set size: it never runs
// for the by-address (trusting) paths, where a commit may carry far more
// signatures than the trusted set, and for the by-index paths it handles at
// most one signature per validator, like the stock batch.
func TestParallelCommitVerify_BoundedWork(t *testing.T) {
	prev := SetParallelCommitVerify(true)
	defer SetParallelCommitVerify(prev)
	rng := rand.New(rand.NewSource(7))
	vals, privs := diffValidators(rng, 100)
	blockID := diffBlockID(rng)
	commit := diffCommit(t, rng, vals, privs, blockID, 5)

	// ~19k signatures all naming validator 0, with garbage signatures.
	victim := vals.Validators[0].Address
	adv := &Commit{Height: 5, Round: 1, BlockID: blockID, Signatures: make([]CommitSig, 19000)}
	for i := range adv.Signatures {
		sig := make([]byte, 64)
		rng.Read(sig)
		adv.Signatures[i] = CommitSig{
			BlockIDFlag: BlockIDFlagCommit, ValidatorAddress: victim,
			Timestamp: time.Unix(1700000000, 0).UTC(), Signature: sig,
		}
	}
	trust := cmtmath.Fraction{Numerator: 1, Denominator: 3}
	for name, fn := range map[string]func() error{
		"VerifyCommitLightTrusting": func() error { return VerifyCommitLightTrusting(pcvChainID, vals, adv, trust) },
		"VerifyCommitLightTrustingAllSignatures": func() error {
			return VerifyCommitLightTrustingAllSignatures(pcvChainID, vals, adv, trust)
		},
		"VerifyCommit":                   func() error { return vals.VerifyCommit(pcvChainID, blockID, 5, adv) },
		"VerifyCommitLightAllSignatures": func() error { return VerifyCommitLightAllSignatures(pcvChainID, vals, blockID, 5, adv) },
	} {
		SetParallelCommitVerify(false)
		want := diffRun(fn)
		SetParallelCommitVerify(true)
		before := parallelCommitVerifySigs.Load()
		got := diffRun(fn)
		require.Equal(t, want, got, name)
		require.Zero(t, parallelCommitVerifySigs.Load()-before, "%s: parallel pass must not run", name)
	}

	// All signatures garbage on a by-index commit: the parallel pass handles
	// each validator's signature at most once before falling back.
	bad := *commit
	bad.Signatures = append([]CommitSig(nil), commit.Signatures...)
	for i := range bad.Signatures {
		if bad.Signatures[i].BlockIDFlag != BlockIDFlagAbsent {
			s := make([]byte, 64)
			rng.Read(s)
			bad.Signatures[i].Signature = s
		}
	}
	SetParallelCommitVerify(false)
	want := diffRun(func() error { return vals.VerifyCommit(pcvChainID, blockID, 5, &bad) })
	SetParallelCommitVerify(true)
	before := parallelCommitVerifySigs.Load()
	got := diffRun(func() error { return vals.VerifyCommit(pcvChainID, blockID, 5, &bad) })
	require.Equal(t, want, got)
	require.LessOrEqual(t, parallelCommitVerifySigs.Load()-before, int64(vals.Size()))
}
