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
	tests := []struct {
		name    string
		msg     []byte
		wantErr bool
	}{
		{
			"evidence commit under limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(100, 0, 0)),
			false,
		},
		{
			"evidence commit over limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(maxEvidenceSigs+1, 0, 0)),
			true,
		},
		{
			"evidence validator set over limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(0, maxEvidenceSigs+1, 0)),
			true,
		},
		{
			"evidence byzantine validators over limit",
			blockWithEvidenceBytes(t, lightClientAttackEvidence(0, 0, maxEvidenceSigs+1)),
			true,
		},
		{
			"count summed across evidence items",
			blockWithEvidenceBytes(t,
				lightClientAttackEvidence(maxEvidenceSigs/2+1, 0, 0),
				lightClientAttackEvidence(maxEvidenceSigs/2+1, 0, 0),
			),
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBlockSyncBytes(tc.msg)
			if tc.wantErr {
				require.ErrorIs(t, err, errTooManySigs)
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
