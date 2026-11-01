package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
)

// The KMS signing allowance must apply to a remote signer however the node
// wraps it: with priv_validator_laddr set, consensus gets a RetrySignerClient,
// not a bare SignerClient.
func TestPrecommitDelayKMSAllowanceForRemoteSigners(t *testing.T) {
	cs := &State{propagator: propagation.NewNoOpPropagator()}
	cs.state.Timeouts.DelayedPrecommitTimeout = 2100 * time.Millisecond

	delay := func(signer cmttypes.PrivValidator) time.Duration {
		cs.privValidator = signer
		cs.rs.StartTime = time.Now()
		return cs.precommitDelay()
	}
	local := delay(cmttypes.NewMockPV())

	raw := &privval.SignerClient{}
	for name, signer := range map[string]cmttypes.PrivValidator{
		"SignerClient":      raw,
		"RetrySignerClient": privval.NewRetrySignerClient(raw, 1, time.Millisecond),
	} {
		t.Run(name, func(t *testing.T) {
			require.InDelta(t, float64(KMSSigningDelay), float64(local-delay(signer)), float64(20*time.Millisecond))
		})
	}
}
