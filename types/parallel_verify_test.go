package types

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/crypto"
	cmtmath "github.com/tendermint/tendermint/libs/math"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

func setParallelSigVerify(t *testing.T, on bool) {
	t.Helper()
	prev := parallelSigVerify
	parallelSigVerify = on
	t.Cleanup(func() { parallelSigVerify = prev })
}

// randLightCommit returns a validator set with random voting powers and a
// commit at height 1 whose signatures are randomly valid, invalid, absent, nil
// votes or reassigned to another validator's address.
func randLightCommit(t *testing.T, r *rand.Rand, chainID string, blockID BlockID) (*ValidatorSet, *Commit) {
	n := 1 + r.Intn(30) // spans both sides of minParallelSigs
	privs := make(map[string]PrivValidator, n)
	vals := make([]*Validator, n)
	for i := range vals {
		pv := NewMockPV()
		pk, err := pv.GetPubKey()
		require.NoError(t, err)
		privs[string(pk.Address())] = pv
		vals[i] = NewValidator(pk, 1+r.Int63n(100))
	}
	valSet := NewValidatorSet(vals)
	now := time.Unix(1700000000, 0)
	commit := &Commit{Height: 1, Round: 0, BlockID: blockID, Signatures: make([]CommitSig, n)}
	for idx, val := range valSet.Validators {
		ts := now.Add(time.Duration(idx) * time.Millisecond)
		sign := func(bid BlockID) []byte {
			v := &Vote{Type: cmtproto.PrecommitType, Height: 1, Round: 0, BlockID: bid, Timestamp: ts,
				ValidatorAddress: val.Address, ValidatorIndex: int32(idx)}
			pv := v.ToProto()
			require.NoError(t, privs[string(val.Address)].SignVote(chainID, pv))
			return pv.Signature
		}
		cs := CommitSig{BlockIDFlag: BlockIDFlagCommit, ValidatorAddress: val.Address, Timestamp: ts}
		switch k := r.Intn(10); {
		case k < 5:
			cs.Signature = sign(blockID)
		case k == 5:
			cs = NewCommitSigAbsent()
		case k == 6:
			cs.BlockIDFlag = BlockIDFlagNil
			cs.Signature = sign(BlockID{})
		case k == 7:
			cs.Signature = sign(blockID)
			cs.Signature[r.Intn(len(cs.Signature))] ^= 1
		case k == 8:
			cs.Signature = sign(blockID)
			cs.ValidatorAddress = valSet.Validators[r.Intn(n)].Address // duplicate or wrong address
		default:
			cs.Signature = sign(blockID)
			cs.Timestamp = ts.Add(time.Second) // signature does not match sign bytes
		}
		commit.Signatures[idx] = cs
	}
	return valSet, commit
}

type lightVerifyResult struct {
	err   string
	panic string
}

func runLightVerify(f func() error) (res lightVerifyResult) {
	defer func() {
		if r := recover(); r != nil {
			res.panic = fmt.Sprint(r)
		}
	}()
	if err := f(); err != nil {
		res.err = err.Error()
	}
	return res
}

// TestParallelSigVerifyMatchesSequential runs every light-client verification
// method on random commits with parallel signature verification off and on and
// requires identical results, including the exact error text.
func TestParallelSigVerifyMatchesSequential(t *testing.T) {
	const chainID = "test_chain_id"
	r := rand.New(rand.NewSource(42))
	blockID := makeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	trustLevels := []cmtmath.Fraction{{Numerator: 1, Denominator: 3}, {Numerator: 2, Denominator: 3}, {Numerator: 1, Denominator: 1}}
	var errs, oks int
	for iter := 0; iter < 400; iter++ {
		valSet, commit := randLightCommit(t, r, chainID, blockID)
		// A trusted set that only partially overlaps, in a different order.
		other, _ := RandValidatorSet(1+r.Intn(3), 1+r.Int63n(50))
		trusted := NewValidatorSet(append(other.Copy().Validators, valSet.Copy().Validators[r.Intn(valSet.Size()):]...))
		tl := trustLevels[r.Intn(len(trustLevels))]
		methods := map[string]func() error{
			"light":          func() error { return valSet.VerifyCommitLight(chainID, blockID, 1, commit) },
			"lightAll":       func() error { return valSet.VerifyCommitLightAllSignatures(chainID, blockID, 1, commit) },
			"trusting":       func() error { return trusted.VerifyCommitLightTrusting(chainID, commit, tl) },
			"trustingAll":    func() error { return trusted.VerifyCommitLightTrustingAllSignatures(chainID, commit, tl) },
			"trustingSelf":   func() error { return valSet.VerifyCommitLightTrusting(chainID, commit, tl) },
			"trustingSelfAl": func() error { return valSet.VerifyCommitLightTrustingAllSignatures(chainID, commit, tl) },
		}
		for name, f := range methods {
			setParallelSigVerify(t, false)
			want := runLightVerify(f)
			setParallelSigVerify(t, true)
			got := runLightVerify(f)
			require.Equal(t, want, got, "iter %d method %s", iter, name)
			if want.err == "" {
				oks++
			} else {
				errs++
			}
		}
	}
	// Make sure the generator exercises both outcomes.
	require.Greater(t, oks, 100)
	require.Greater(t, errs, 100)
}

// panicPubKey wraps a real key but panics on verification.
type panicPubKey struct{ crypto.PubKey }

func (panicPubKey) VerifySignature([]byte, []byte) bool { panic("boom") }

// TestParallelSigVerifyPanicParity checks that a panic during verification is
// raised on the caller's goroutine (where the SDK recovers it) exactly when the
// sequential loop would raise it, and never crashes a worker goroutine.
func TestParallelSigVerifyPanicParity(t *testing.T) {
	const chainID = "test_chain_id"
	blockID := makeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	voteSet, valSet, privs := randVoteSet(1, 1, cmtproto.PrecommitType, 10, 1)
	commit, err := MakeCommit(blockID, 1, 1, voteSet, privs, time.Unix(1700000000, 0))
	require.NoError(t, err)
	for _, idx := range []int{0, 9} { // 9 is past the 2/3 threshold: never reached sequentially
		vs := valSet.Copy()
		vs.Validators[idx].PubKey = panicPubKey{vs.Validators[idx].PubKey}
		for _, f := range []func() error{
			func() error { return vs.VerifyCommitLight(chainID, blockID, 1, commit) },
			func() error {
				return vs.VerifyCommitLightTrusting(chainID, commit, cmtmath.Fraction{Numerator: 1, Denominator: 3})
			},
		} {
			setParallelSigVerify(t, false)
			want := runLightVerify(f)
			setParallelSigVerify(t, true)
			got := runLightVerify(f)
			require.Equal(t, want, got, "idx %d", idx)
			require.Equal(t, idx == 0, want.panic != "", "idx %d: %+v", idx, want)
		}
	}
}

// countingPubKey counts VerifySignature calls.
type countingPubKey struct {
	crypto.PubKey
	n *atomic.Int64
}

func (k countingPubKey) VerifySignature(msg, sig []byte) bool {
	k.n.Add(1)
	return k.PubKey.VerifySignature(msg, sig)
}

// TestParallelSigVerifyTrustingNoAmplification checks that the pre-pass does
// not verify more signatures than the sequential path can reach: a commit
// whose entries all name the same trusted validator must cost one
// verification, not one per entry.
func TestParallelSigVerifyTrustingNoAmplification(t *testing.T) {
	var calls atomic.Int64
	pv := NewMockPV()
	pk, err := pv.GetPubKey()
	require.NoError(t, err)
	vals := NewValidatorSet([]*Validator{NewValidator(pk, 10)})
	vals.Validators[0].PubKey = countingPubKey{pk, &calls}
	blockID := makeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	sigs := make([]CommitSig, 5000)
	for i := range sigs {
		sigs[i] = CommitSig{BlockIDFlag: BlockIDFlagCommit, ValidatorAddress: pk.Address(),
			Timestamp: time.Unix(1700000000, 0), Signature: make([]byte, 64)}
	}
	commit := &Commit{Height: 1, BlockID: blockID, Signatures: sigs}
	f := func() error {
		return vals.VerifyCommitLightTrusting("test_chain_id", commit, cmtmath.Fraction{Numerator: 1, Denominator: 3})
	}

	setParallelSigVerify(t, false)
	calls.Store(0)
	want := runLightVerify(f)
	stock := calls.Load()
	setParallelSigVerify(t, true)
	calls.Store(0)
	got := runLightVerify(f)
	require.Equal(t, want, got)
	require.LessOrEqual(t, calls.Load(), stock+int64(vals.Size()), "parallel %d vs stock %d", calls.Load(), stock)
}
