package blocksync

import (
	"testing"

	"github.com/stretchr/testify/require"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// lightClientAttackEvidence builds an Evidence carrying a LightClientAttackEvidence
// whose conflicting block holds nCommitSigs commit signatures and nValidators
// validators, plus nByzantine byzantine validators.
func lightClientAttackEvidence(nCommitSigs, nValidators, nByzantine int) cmtproto.Evidence {
	validators := make([]*cmtproto.Validator, nValidators)
	for i := range validators {
		validators[i] = &cmtproto.Validator{}
	}
	byzantine := make([]*cmtproto.Validator, nByzantine)
	for i := range byzantine {
		byzantine[i] = &cmtproto.Validator{}
	}
	return cmtproto.Evidence{
		Sum: &cmtproto.Evidence_LightClientAttackEvidence{
			LightClientAttackEvidence: &cmtproto.LightClientAttackEvidence{
				ConflictingBlock: &cmtproto.LightBlock{
					SignedHeader: &cmtproto.SignedHeader{Commit: commitWithSigs(nCommitSigs)},
					ValidatorSet: &cmtproto.ValidatorSet{Validators: validators},
				},
				ByzantineValidators: byzantine,
			},
		},
	}
}

// emptyEvidence returns n evidence items that carry no nested signature or
// validator lists (nil oneof), i.e. neither duplicate-vote nor light-client
// attack evidence.
func emptyEvidence(n int) []cmtproto.Evidence {
	return make([]cmtproto.Evidence, n)
}

// blockWithEvidenceBytes encodes a blocksync Message carrying a BlockResponse
// whose block holds the given evidence.
func blockWithEvidenceBytes(t *testing.T, ev ...cmtproto.Evidence) []byte {
	t.Helper()
	return mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{
		Block: &cmtproto.Block{Evidence: cmtproto.EvidenceList{Evidence: ev}},
	}))
}

// The signature-count cap must also apply to commits, validator sets and
// byzantine validators reached through Block.evidence, not only Block.last_commit.
func TestValidateBlockSyncBytes_EvidenceSigCount(t *testing.T) {
	// aggItems is the number of MaxVotesCount-sized commits whose summed
	// signatures exceed the aggregate evidence bound.
	aggItems := maxEvidenceSigs/types.MaxVotesCount + 1
	aggEvidence := make([]cmtproto.Evidence, aggItems)
	for i := range aggEvidence {
		aggEvidence[i] = lightClientAttackEvidence(types.MaxVotesCount, 0, 0)
	}

	tests := []struct {
		name    string
		msg     []byte
		wantErr error
	}{
		{
			"evidence commit under limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(100, 0, 0)),
			nil,
		},
		{
			"evidence commit over per-list limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(types.MaxVotesCount+1, 0, 0)),
			errTooManySigs,
		},
		{
			"evidence validator set over per-list limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(0, types.MaxVotesCount+1, 0)),
			errTooManySigs,
		},
		{
			"evidence byzantine validators over per-list limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(0, 0, types.MaxVotesCount+1)),
			errTooManySigs,
		},
		{
			"aggregate summed across evidence items",
			blockWithEvidenceBytes(t, aggEvidence...),
			errTooManyEvidenceSigs,
		},
		{
			"too many items without nested lists",
			blockWithEvidenceBytes(t, emptyEvidence(maxEvidenceSigs+1)...),
			errTooManyEvidenceSigs,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBlockSyncBytes(tc.msg)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A legal last_commit combined with evidence within the cap still passes.
func TestValidateBlockSyncBytes_EvidenceAndLastCommit(t *testing.T) {
	msg := mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{
		Block: &cmtproto.Block{
			LastCommit: commitWithSigs(types.MaxVotesCount),
			Evidence: cmtproto.EvidenceList{
				Evidence: []cmtproto.Evidence{lightClientAttackEvidence(100, 100, 5)},
			},
		},
	}))
	require.NoError(t, validateBlockSyncBytes(msg))
}
