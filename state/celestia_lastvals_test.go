package state

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/types"
)

// lastValsStore serves LoadValidators from a fixed set and counts the calls.
type lastValsStore struct {
	Store
	vals  *types.ValidatorSet
	calls int
}

func (s *lastValsStore) LoadValidators(int64) (*types.ValidatorSet, error) {
	s.calls++
	return s.vals.Copy(), nil
}

func lastValsFixture(rng *rand.Rand, n int) (*types.ValidatorSet, *types.Block) {
	vals := make([]*types.Validator, n)
	for i := range vals {
		secret := make([]byte, 8)
		rng.Read(secret)
		vals[i] = types.NewValidator(ed25519.GenPrivKeyFromSecret(secret).PubKey(), 1+rng.Int63n(100))
	}
	set := types.NewValidatorSet(vals)
	sigs := make([]types.CommitSig, n)
	for i, v := range set.Validators {
		switch rng.Intn(3) {
		case 0:
			sigs[i] = types.NewCommitSigAbsent()
		case 1:
			sigs[i] = types.CommitSig{BlockIDFlag: types.BlockIDFlagNil, ValidatorAddress: v.Address, Timestamp: time.Unix(1, 0)}
		default:
			sigs[i] = types.CommitSig{BlockIDFlag: types.BlockIDFlagCommit, ValidatorAddress: v.Address, Timestamp: time.Unix(1, 0)}
		}
	}
	block := &types.Block{
		Header:     types.Header{Height: 10},
		LastCommit: &types.Commit{Height: 9, Round: int32(rng.Intn(3)), Signatures: sigs},
	}
	return set, block
}

func TestLastCommitInfoForApplyGuards(t *testing.T) {
	prev := SetLastValidatorsFromState(true)
	defer SetLastValidatorsFromState(prev)

	rng := rand.New(rand.NewSource(1))
	set, block := lastValsFixture(rng, 12)
	// make sure at least one non-absent sig exists for the address check
	block.LastCommit.Signatures[0] = types.CommitSig{
		BlockIDFlag: types.BlockIDFlagCommit, ValidatorAddress: set.Validators[0].Address, Timestamp: time.Unix(1, 0),
	}
	base := State{InitialHeight: 1, LastBlockHeight: 9, LastValidators: set.Copy()}

	cases := []struct {
		name      string
		mutate    func(s *State, b *types.Block)
		wantStore bool
	}{
		{"consistent", func(*State, *types.Block) {}, false},
		{"height mismatch", func(s *State, _ *types.Block) { s.LastBlockHeight = 8 }, true},
		{"initial height", func(s *State, _ *types.Block) { s.InitialHeight = 10 }, false},
		{"nil last validators", func(s *State, _ *types.Block) { s.LastValidators = nil }, true},
		{"empty last validators", func(s *State, _ *types.Block) { s.LastValidators = types.NewValidatorSet(nil) }, true},
		{"size mismatch", func(s *State, _ *types.Block) {
			s.LastValidators = types.NewValidatorSet(s.LastValidators.Copy().Validators[:11])
		}, true},
		{"address mismatch", func(_ *State, b *types.Block) {
			b.LastCommit.Signatures[0].ValidatorAddress = make([]byte, 20)
		}, true},
		{"absent sig address ignored", func(_ *State, b *types.Block) {
			b.LastCommit.Signatures[0] = types.NewCommitSigAbsent()
		}, false},
		{"disabled", func(*State, *types.Block) { SetLastValidatorsFromState(false) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetLastValidatorsFromState(true)
			st := base
			st.LastValidators = base.LastValidators.Copy()
			lc := *block.LastCommit
			lc.Signatures = append([]types.CommitSig(nil), block.LastCommit.Signatures...)
			b := &types.Block{Header: block.Header, LastCommit: &lc}
			tc.mutate(&st, b)

			store := &lastValsStore{vals: set}
			hits := LastValidatorsFromStateHits()
			got := lastCommitInfoForApply(st, b, store)
			usedState := LastValidatorsFromStateHits() > hits
			if b.Height == st.InitialHeight {
				require.False(t, usedState)
				require.Zero(t, store.calls)
				require.Empty(t, got.Votes)
				return
			}
			require.Equal(t, !tc.wantStore, usedState)
			if tc.wantStore {
				require.Equal(t, 1, store.calls)
			} else {
				require.Zero(t, store.calls)
			}
			require.Equal(t, BuildLastCommitInfo(b, set, 1), got)
		})
	}
}

// TestLastCommitInfoForApplyRandomEquivalence checks that, whenever the
// shortcut is taken for a state whose LastValidators equals the stored set
// (up to proposer priorities, which the store replays), the CommitInfo is
// identical to the store path.
func TestLastCommitInfoForApplyRandomEquivalence(t *testing.T) {
	prev := SetLastValidatorsFromState(true)
	defer SetLastValidatorsFromState(prev)
	rng := rand.New(rand.NewSource(2))
	for it := 0; it < 200; it++ {
		set, block := lastValsFixture(rng, 1+rng.Intn(150))
		stateVals := set.Copy()
		stateVals.IncrementProposerPriority(int32(1 + rng.Intn(50)))
		st := State{InitialHeight: 1, LastBlockHeight: 9, LastValidators: stateVals}
		store := &lastValsStore{vals: set}
		got := lastCommitInfoForApply(st, block, store)
		require.Zero(t, store.calls)
		require.Equal(t, BuildLastCommitInfo(block, set, 1), got)
	}
}
