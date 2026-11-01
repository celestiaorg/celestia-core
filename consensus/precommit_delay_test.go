package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/types"
)

// The KMS signing allowance must apply to a remote signer however the node
// wraps it: with priv_validator_laddr set, consensus gets a RetrySignerClient,
// not a bare SignerClient.
func TestPrecommitDelayKMSAllowanceForRemoteSigners(t *testing.T) {
	cs, _ := randState(1)
	cs.state.Timeouts.DelayedPrecommitTimeout = 2100 * time.Millisecond
	localSigner := cs.privValidator
	t.Cleanup(func() { cs.privValidator = localSigner })

	delay := func() time.Duration {
		cs.rs.StartTime = time.Now()
		return cs.precommitDelay()
	}
	local := delay()

	raw := &privval.SignerClient{}
	for name, signer := range map[string]types.PrivValidator{
		"SignerClient":      raw,
		"RetrySignerClient": privval.NewRetrySignerClient(raw, 1, time.Millisecond),
	} {
		t.Run(name, func(t *testing.T) {
			cs.privValidator = signer
			require.InDelta(t, float64(KMSSigningDelay), float64(local-delay()), float64(20*time.Millisecond))
		})
	}
}
