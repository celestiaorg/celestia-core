package consensus

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cometbft/cometbft/types"
)

func TestLogLevelForProcessMsgErr(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		expected processMsgLogLevel
	}{
		// Debug: stale / routine conditions on a public network.
		{
			name:     "stale proposal height/round",
			err:      fmt.Errorf("%w: details", errInvalidProposalHeightRound),
			expected: logLevelDebug,
		},
		{
			name:     "catch-all ErrAddingVote",
			err:      ErrAddingVote,
			expected: logLevelDebug,
		},
		// Info: peer misbehavior (malformed messages, invalid signatures, equivocation).
		{
			name:     "invalid proposal signature",
			err:      ErrInvalidProposalSignature,
			expected: logLevelInfo,
		},
		{
			name:     "proposal POL round invalid",
			err:      fmt.Errorf("%w: POLRound 0 Round 1", ErrInvalidProposalPOLRound),
			expected: logLevelInfo,
		},
		{
			name:     "proposal has too many parts",
			err:      ErrProposalTooManyParts,
			expected: logLevelInfo,
		},
		{
			name:     "invalid block part proof",
			err:      fmt.Errorf("add part: %w", types.ErrPartSetInvalidProof),
			expected: logLevelInfo,
		},
		{
			name:     "unexpected block part index",
			err:      types.ErrPartSetUnexpectedIndex,
			expected: logLevelInfo,
		},
		{
			name:     "conflicting votes (equivocation)",
			err:      types.NewConflictingVoteError(&types.Vote{}, &types.Vote{}),
			expected: logLevelInfo,
		},
		// Error: unexpected internal failures / state inconsistencies.
		{
			name:     "private validator pubkey not set",
			err:      errPubKeyIsNotSet,
			expected: logLevelError,
		},
		{
			name:     "unknown error",
			err:      errors.New("some unexpected internal error"),
			expected: logLevelError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, logLevelForProcessMsgErr(tc.err))
		})
	}
}
