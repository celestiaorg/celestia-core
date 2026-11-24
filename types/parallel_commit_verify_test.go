package types

import (
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

const pcvChainID = "test_chain_id"

// pcvCommit returns a validator set of size n and a commit in which every
// validator signed blockID.
func pcvCommit(t testing.TB, n int, height int64) (*ValidatorSet, *Commit, BlockID) {
	t.Helper()
	blockID := MakeBlockIDRandom()
	voteSet, vals, privs := RandVoteSet(height, 1, cmtproto.PrecommitType, n, 10)
	ext, err := MakeExtCommit(blockID, height, 1, voteSet, privs, time.Unix(1700000000, 0), false)
	require.NoError(t, err)
	return vals, ext.ToCommit(), blockID
}

// pcvCorrupt returns a copy of c with one byzantine change.
func pcvCorrupt(rng *rand.Rand, c *Commit, kind int) (*Commit, string) {
	cc := *c
	cc.Signatures = append([]CommitSig(nil), c.Signatures...)
	n := len(cc.Signatures)
	i := rng.Intn(n)
	j := (i + 1 + rng.Intn(n-1)) % n
	switch kind {
	case 0:
		s := append([]byte(nil), cc.Signatures[i].Signature...)
		s[rng.Intn(len(s))] ^= 0x01
		cc.Signatures[i].Signature = s
		return &cc, fmt.Sprintf("flipped sig %d", i)
	case 1:
		cc.Signatures[i].ValidatorAddress, cc.Signatures[j].ValidatorAddress =
			cc.Signatures[j].ValidatorAddress, cc.Signatures[i].ValidatorAddress
		return &cc, fmt.Sprintf("swapped addresses %d %d", i, j)
	case 2:
		cc.Signatures[i].Signature = nil
		return &cc, fmt.Sprintf("nil sig %d", i)
	case 3:
		cc.Signatures[i].BlockIDFlag = BlockIDFlagNil // signature no longer matches
		return &cc, fmt.Sprintf("commit->nil flag %d", i)
	default:
		for k := 0; k <= len(cc.Signatures)/3; k++ {
			cc.Signatures[(i+k)%len(cc.Signatures)] = NewCommitSigAbsent()
		}
		return &cc, "power shortfall"
	}
}

type pcvOutcome struct{ typ, msg string }

func pcvRun(f func() error) pcvOutcome {
	if err := f(); err != nil {
		return pcvOutcome{fmt.Sprintf("%T", err), err.Error()}
	}
	return pcvOutcome{}
}

// pcvCompare asserts that every verification entry point gives the same
// result and error with the parallel pass off and on, at several GOMAXPROCS.
func pcvCompare(t *testing.T, vals *ValidatorSet, blockID BlockID, height int64, commit *Commit, what string) {
	t.Helper()
	trust := cmtmath.Fraction{Numerator: 1, Denominator: 3}
	fns := map[string]func() error{
		"VerifyCommit":      func() error { return vals.VerifyCommit(pcvChainID, blockID, height, commit) },
		"VerifyCommitLight": func() error { return VerifyCommitLight(pcvChainID, vals, blockID, height, commit) },
		"VerifyCommitLightAllSignatures": func() error {
			return VerifyCommitLightAllSignatures(pcvChainID, vals, blockID, height, commit)
		},
		"VerifyCommitLightTrusting": func() error { return VerifyCommitLightTrusting(pcvChainID, vals, commit, trust) },
		"VerifyCommitLightTrustingAllSignatures": func() error {
			return VerifyCommitLightTrustingAllSignatures(pcvChainID, vals, commit, trust)
		},
	}
	procs := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(procs)
	defer SetParallelCommitVerify(true)
	for name, fn := range fns {
		SetParallelCommitVerify(false)
		want := pcvRun(fn)
		SetParallelCommitVerify(true)
		for _, p := range []int{1, 3, 8} {
			runtime.GOMAXPROCS(p)
			require.Equal(t, want, pcvRun(fn), "%s, %s, GOMAXPROCS=%d", name, what, p)
		}
	}
}

// TestParallelCommitVerifyDifferential compares the parallel and sequential
// paths on valid commits and on random byzantine corruptions.
func TestParallelCommitVerifyDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(20261123))
	for _, n := range []int{16, 17, 33, 64, 100} {
		vals, commit, blockID := pcvCommit(t, n, 7)
		require.NoError(t, vals.VerifyCommit(pcvChainID, blockID, 7, commit))
		pcvCompare(t, vals, blockID, 7, commit, "valid")
		for it := 0; it < 6; it++ {
			for kind := 0; kind < 5; kind++ {
				bad, what := pcvCorrupt(rng, commit, kind)
				pcvCompare(t, vals, blockID, 7, bad, fmt.Sprintf("n=%d %s", n, what))
				require.Error(t, vals.VerifyCommit(pcvChainID, blockID, 7, bad), what)
			}
		}
	}
}

// TestParallelCommitVerifyManySigsOneValidator uses a commit with far more
// signatures than validators, all naming one validator, as a light-client
// header could carry. Results must match the sequential path.
func TestParallelCommitVerifyManySigsOneValidator(t *testing.T) {
	vals, commit, blockID := pcvCommit(t, 100, 5)
	adv := &Commit{Height: 5, Round: 1, BlockID: blockID, Signatures: make([]CommitSig, 19000)}
	for i := range adv.Signatures {
		adv.Signatures[i] = commit.Signatures[0]
		adv.Signatures[i].Signature = make([]byte, 64)
	}
	pcvCompare(t, vals, blockID, 5, adv, "19k sigs from one validator")
}

func benchCommitVerify(b *testing.B, parallel bool) {
	vals, commit, bid := pcvCommit(b, 100, 3)
	SetParallelCommitVerify(parallel)
	defer SetParallelCommitVerify(true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := vals.VerifyCommit(pcvChainID, bid, 3, commit); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyCommit100Sequential(b *testing.B) { benchCommitVerify(b, false) }
func BenchmarkVerifyCommit100Parallel(b *testing.B)   { benchCommitVerify(b, true) }
