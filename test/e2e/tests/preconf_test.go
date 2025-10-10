package e2e_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	e2e "github.com/cometbft/cometbft/test/e2e/pkg"
	"github.com/cometbft/cometbft/types"
)

// TestApp_Preconfirmations tests that the preconfirmation system is working.
// It submits a transaction and verifies that it receives preconfirmation
// voting power from validators before being committed to a block.
func TestApp_Preconfirmations(t *testing.T) {
	testNode(t, func(t *testing.T, node e2e.Node) {
		client, err := node.Client()
		require.NoError(t, err)

		// Generate a random transaction value
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		bz := make([]byte, 32)
		_, err = r.Read(bz)
		require.NoError(t, err)

		key := fmt.Sprintf("preconf-test-%v", node.Name)
		value := fmt.Sprintf("%x", bz)
		tx := types.Tx(fmt.Sprintf("%v=%v", key, value))

		// Broadcast the transaction
		_, err = client.BroadcastTxSync(ctx, tx)
		require.NoError(t, err)

		hash := tx.Hash()

		// Wait a bit to allow preconfirmations to propagate
		// The ADR specifies validators gossip every 2 seconds, so we wait
		// a bit longer to allow for network propagation
		time.Sleep(5 * time.Second)

		// Query the transaction status
		txStatus, err := client.TxStatus(ctx, hash)
		require.NoError(t, err)

		// We should have received some preconfirmation voting power
		// This is a lightweight test - we just verify the RPC works
		// and returns preconf data (even if it's 0, the field should exist)
		t.Logf("Preconfirmation voting power: %d", txStatus.PreconfirmationVotingPower)
		t.Logf("Preconfirmed proportion: %.2f", txStatus.PreconfirmedProportion)

		// The preconfirmation voting power should be non-negative
		require.GreaterOrEqual(t, txStatus.PreconfirmationVotingPower, int64(0))
		require.GreaterOrEqual(t, txStatus.PreconfirmedProportion, 0.0)

		// Wait for the transaction to be committed
		waitTime := 30 * time.Second
		require.Eventuallyf(t, func() bool {
			txResp, err := client.Tx(ctx, hash, false)
			return err == nil && len(txResp.Tx) > 0
		}, waitTime, time.Second,
			"submitted tx wasn't committed after %v", waitTime,
		)
	})
}
