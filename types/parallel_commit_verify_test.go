package types

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

const pcvChainID = "pcv-chain"

// pcvCommit builds a commit over a fresh validator set of size n with the
// given per-index flags. Validators have voting power 10 unless powers is
// non-nil.
func pcvCommit(t *testing.T, n int, flags []BlockIDFlag, height int64) (*ValidatorSet, *Commit, BlockID) {
	t.Helper()
	valSet, privs := RandValidatorSet(n, 10)
	blockID := makeBlockIDRandom()
	return valSet, pcvSign(t, valSet, privs, flags, blockID, height), blockID
}

func pcvSign(t *testing.T, valSet *ValidatorSet, privs []PrivValidator, flags []BlockIDFlag, blockID BlockID, height int64) *Commit {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	sigs := make([]CommitSig, len(valSet.Validators))
	for i, val := range valSet.Validators {
		flag := BlockIDFlagCommit
		if flags != nil {
			flag = flags[i]
		}
		if flag == BlockIDFlagAbsent {
			sigs[i] = NewCommitSigAbsent()
			continue
		}
		var priv PrivValidator
		for _, p := range privs {
			pk, err := p.GetPubKey()
			require.NoError(t, err)
			if pk.Equals(val.PubKey) {
				priv = p
			}
		}
		require.NotNil(t, priv)
		vote := &Vote{
			ValidatorAddress: val.Address,
			ValidatorIndex:   int32(i),
			Height:           height,
			Round:            0,
			Type:             cmtproto.PrecommitType,
			Timestamp:        now.Add(time.Duration(i) * time.Millisecond),
		}
		if flag == BlockIDFlagCommit {
			vote.BlockID = blockID
		}
		v := vote.ToProto()
		require.NoError(t, priv.SignVote(pcvChainID, v))
		vote.Signature = v.Signature
		sigs[i] = vote.CommitSig()
	}
	return &Commit{Height: height, Round: 0, BlockID: blockID, Signatures: sigs}
}

type pcvResult struct {
	msg       string
	notEnough bool
}

func pcvRun(f func() error) pcvResult {
	err := f()
	if err == nil {
		return pcvResult{}
	}
	var ne ErrNotEnoughVotingPowerSigned
	return pcvResult{msg: err.Error(), notEnough: errors.As(err, &ne)}
}

// pcvCompare runs all verify entry points with the parallel path off and on
// and asserts identical outcomes. Returns the VerifyCommit outcome.
func pcvCompare(t *testing.T, vals *ValidatorSet, blockID BlockID, height int64, commit *Commit) pcvResult {
	t.Helper()
	fns := map[string]func() error{
		"VerifyCommit": func() error { return vals.VerifyCommit(pcvChainID, blockID, height, commit) },
		"VerifyCommitLight": func() error {
			return VerifyCommitLight(pcvChainID, vals, blockID, height, commit)
		},
		"VerifyCommitLightAllSignatures": func() error {
			return VerifyCommitLightAllSignatures(pcvChainID, vals, blockID, height, commit)
		},
		"VerifyCommitLightTrustingAllSignatures": func() error {
			return VerifyCommitLightTrustingAllSignatures(pcvChainID, vals, commit, cmtmath.Fraction{Numerator: 1, Denominator: 3})
		},
	}
	var out pcvResult
	for name, fn := range fns {
		prev := SetParallelCommitVerify(false)
		off := pcvRun(fn)
		SetParallelCommitVerify(true)
		on := pcvRun(fn)
		SetParallelCommitVerify(prev)
		require.Equal(t, off, on, "%s: outcome differs with parallel verification", name)
		if name == "VerifyCommit" {
			out = on
		}
	}
	return out
}

func corruptSig(c *Commit, idx int) {
	sig := append([]byte(nil), c.Signatures[idx].Signature...)
	sig[0] ^= 0xff
	c.Signatures[idx].Signature = sig
}

func TestParallelCommitVerify_Equivalence(t *testing.T) {
	const height = int64(42)
	for _, n := range []int{1, 2, 15, 16, 17, 33, 64, 100, 129} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			// valid commit, all signed
			vals, commit, bid := pcvCommit(t, n, nil, height)
			require.Equal(t, pcvResult{}, pcvCompare(t, vals, bid, height, commit))

			positions := []int{0, n / 2, n - 1}
			for _, p := range positions {
				cc := *commit
				cc.Signatures = append([]CommitSig(nil), commit.Signatures...)
				corruptSig(&cc, p)
				r := pcvCompare(t, vals, bid, height, &cc)
				require.Contains(t, r.msg, fmt.Sprintf("wrong signature (#%d)", p))
			}

			if n >= 3 {
				// several bad signatures: first one must be reported
				cc := *commit
				cc.Signatures = append([]CommitSig(nil), commit.Signatures...)
				corruptSig(&cc, n-1)
				corruptSig(&cc, n/2)
				corruptSig(&cc, 1)
				r := pcvCompare(t, vals, bid, height, &cc)
				require.Contains(t, r.msg, "wrong signature (#1)")

				// malformed signature length
				cc2 := *commit
				cc2.Signatures = append([]CommitSig(nil), commit.Signatures...)
				cc2.Signatures[n/2].Signature = cc2.Signatures[n/2].Signature[:10]
				r = pcvCompare(t, vals, bid, height, &cc2)
				require.NotEmpty(t, r.msg)

				// address mismatch (swap two sigs)
				cc3 := *commit
				cc3.Signatures = append([]CommitSig(nil), commit.Signatures...)
				cc3.Signatures[1], cc3.Signatures[2] = cc3.Signatures[2], cc3.Signatures[1]
				r = pcvCompare(t, vals, bid, height, &cc3)
				require.Contains(t, r.msg, "validator address mismatch at index 1")

				// wrong chain ID in signatures => every sig wrong
				r2 := pcvRun(func() error { return vals.VerifyCommit("other-chain", bid, height, commit) })
				prev := SetParallelCommitVerify(true)
				r3 := pcvRun(func() error { return vals.VerifyCommit("other-chain", bid, height, commit) })
				SetParallelCommitVerify(prev)
				require.Equal(t, r2, r3)
			}
		})
	}
}

func TestParallelCommitVerify_PowerAndFlags(t *testing.T) {
	const height = int64(7)
	n := 60
	mk := func(nilN, absentN int) []BlockIDFlag {
		f := make([]BlockIDFlag, n)
		for i := range f {
			switch {
			case i%3 == 0 && nilN > 0:
				f[i] = BlockIDFlagNil
				nilN--
			case i%3 == 1 && absentN > 0:
				f[i] = BlockIDFlagAbsent
				absentN--
			default:
				f[i] = BlockIDFlagCommit
			}
		}
		return f
	}

	// sufficient with nil and absent votes
	vals, commit, bid := pcvCommit(t, n, mk(5, 5), height)
	require.Equal(t, pcvResult{}, pcvCompare(t, vals, bid, height, commit))

	// insufficient voting power (exactly 2/3 is not enough)
	vals, commit, bid = pcvCommit(t, n, mk(10, 10), height)
	r := pcvCompare(t, vals, bid, height, commit)
	require.True(t, r.notEnough, r.msg)

	// insufficient voting power AND a bad signature: original batch path
	// reports insufficient power first; must be preserved.
	cc := *commit
	cc.Signatures = append([]CommitSig(nil), commit.Signatures...)
	for i := range cc.Signatures {
		if cc.Signatures[i].BlockIDFlag == BlockIDFlagCommit {
			corruptSig(&cc, i)
			break
		}
	}
	r = pcvCompare(t, vals, bid, height, &cc)
	require.True(t, r.notEnough, r.msg)

	// bad signature on a nil vote (not counted but verified with countAll)
	vals, commit, bid = pcvCommit(t, n, mk(5, 0), height)
	cc = *commit
	cc.Signatures = append([]CommitSig(nil), commit.Signatures...)
	corruptSig(&cc, 0) // index 0 is a nil vote
	require.Equal(t, BlockIDFlagNil, cc.Signatures[0].BlockIDFlag)
	r = pcvCompare(t, vals, bid, height, &cc)
	require.Contains(t, r.msg, "wrong signature (#0)")

	// wrong block ID / height / size
	r = pcvCompare(t, vals, makeBlockIDRandom(), height, commit)
	require.Contains(t, r.msg, "wrong block ID")
	r = pcvCompare(t, vals, bid, height+1, commit)
	require.NotEmpty(t, r.msg)
	short := *commit
	short.Signatures = commit.Signatures[:n-1]
	r = pcvCompare(t, vals, bid, height, &short)
	require.NotEmpty(t, r.msg)
}

func TestParallelCommitVerify_NonBatchableKeys(t *testing.T) {
	const height = int64(9)
	n := 20
	privs := make([]PrivValidator, n)
	valz := make([]*Validator, n)
	for i := 0; i < n; i++ {
		pk := secp256k1.GenPrivKey()
		pv := MockPV{PrivKey: pk}
		privs[i] = pv
		valz[i] = NewValidator(pk.PubKey(), 10)
	}
	vals := NewValidatorSet(valz)
	bid := makeBlockIDRandom()
	commit := pcvSign(t, vals, privs, nil, bid, height)
	require.Equal(t, pcvResult{}, pcvCompare(t, vals, bid, height, commit))
	cc := *commit
	cc.Signatures = append([]CommitSig(nil), commit.Signatures...)
	corruptSig(&cc, 5)
	r := pcvCompare(t, vals, bid, height, &cc)
	require.Contains(t, r.msg, "wrong signature (#5)")
}

func TestParallelCommitVerify_PathTaken(t *testing.T) {
	const height = int64(3)
	vals, commit, bid := pcvCommit(t, 100, nil, height)
	prev := SetParallelCommitVerify(true)
	defer SetParallelCommitVerify(prev)
	parallelCommitVerifyStats.Lock()
	before := parallelCommitVerifyStats.accepted
	parallelCommitVerifyStats.Unlock()
	require.NoError(t, vals.VerifyCommit(pcvChainID, bid, height, commit))
	parallelCommitVerifyStats.Lock()
	after := parallelCommitVerifyStats.accepted
	parallelCommitVerifyStats.Unlock()
	require.Equal(t, before+1, after, "parallel path should have accepted the commit")
}

func benchCommitVerify(b *testing.B, parallel bool) {
	t := &testing.T{}
	const height = int64(3)
	vals, commit, bid := pcvCommit(t, 100, nil, height)
	prev := SetParallelCommitVerify(parallel)
	defer SetParallelCommitVerify(prev)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := vals.VerifyCommit(pcvChainID, bid, height, commit); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyCommit100Sequential(b *testing.B) { benchCommitVerify(b, false) }
func BenchmarkVerifyCommit100Parallel(b *testing.B)   { benchCommitVerify(b, true) }

// BenchmarkValidatorSetHash100 measures the use-time Hash() check the
// blocksync pipeline performs on the main goroutine.
func BenchmarkValidatorSetHash100(b *testing.B) {
	vals, _ := RandValidatorSet(100, 10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vals.Hash()
	}
}
