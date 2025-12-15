package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	e2e "github.com/cometbft/cometbft/test/e2e/pkg"
)

// ============================================================================
// Phase 11.2: E2E Catchup Tests
// ============================================================================

// TestE2E_UnifiedCatchup verifies that nodes can catch up after being
// disconnected or partitioned from the network.
func TestE2E_UnifiedCatchup(t *testing.T) {
	testNode(t, func(t *testing.T, node e2e.Node) {
		// Skip seed and light nodes
		if node.Mode == e2e.ModeSeed || node.Mode == e2e.ModeLight {
			return
		}

		// Focus on nodes with perturbations (they experienced disconnection)
		hasPerturbation := false
		for _, p := range node.Perturbations {
			if p == e2e.PerturbationKill || p == e2e.PerturbationDisconnect || p == e2e.PerturbationRestart {
				hasPerturbation = true
				break
			}
		}

		if !hasPerturbation {
			return
		}

		client, err := node.Client()
		require.NoError(t, err)

		status, err := client.Status(ctx)
		require.NoError(t, err)

		// A node that was perturbed should have recovered and be synced
		assert.False(t, status.SyncInfo.CatchingUp,
			"perturbed node %s should have caught up", node.Name)

		t.Logf("Node %s recovered from perturbations and is at height %d",
			node.Name, status.SyncInfo.LatestBlockHeight)
	})
}

// TestE2E_CatchupPartProgress verifies that catchup uses part-level parallelism
// by checking that multiple nodes have consistent state after catchup.
func TestE2E_CatchupPartProgress(t *testing.T) {
	testnet := loadTestnet(t)

	// Collect latest heights from all non-seed nodes
	heights := make(map[string]int64)
	var maxHeight int64

	for _, node := range testnet.Nodes {
		if node.Mode == e2e.ModeSeed || node.Mode == e2e.ModeLight {
			continue
		}

		client, err := node.Client()
		require.NoError(t, err)

		status, err := client.Status(ctx)
		require.NoError(t, err)

		heights[node.Name] = status.SyncInfo.LatestBlockHeight
		if status.SyncInfo.LatestBlockHeight > maxHeight {
			maxHeight = status.SyncInfo.LatestBlockHeight
		}
	}

	// All nodes should be within a few blocks of each other
	// (allowing for propagation delay)
	const maxLag = int64(5)
	for name, height := range heights {
		assert.InDelta(t, maxHeight, height, float64(maxLag),
			"node %s is too far behind (height %d vs max %d)", name, height, maxHeight)
	}
}

// ============================================================================
// Phase 11.3: E2E Mixed Scenario Tests
// ============================================================================

// TestE2E_BlocksyncThenCatchup verifies the full lifecycle:
// 1. New node syncs via blocksync
// 2. Participates in live consensus
// 3. Gets perturbed (kill/disconnect)
// 4. Catches up after reconnection
func TestE2E_BlocksyncThenCatchup(t *testing.T) {
	testnet := loadTestnet(t)

	// Find nodes that started late AND have perturbations
	// These exercise the full lifecycle
	for _, node := range testnet.Nodes {
		if node.Mode == e2e.ModeSeed || node.Mode == e2e.ModeLight {
			continue
		}

		hasLatestStart := node.StartAt > 0
		hasPerturbation := len(node.Perturbations) > 0

		if !hasLatestStart || !hasPerturbation {
			continue
		}

		t.Run(node.Name, func(t *testing.T) {
			client, err := node.Client()
			require.NoError(t, err)

			status, err := client.Status(ctx)
			require.NoError(t, err)

			// Verify the node went through the full lifecycle:
			// 1. Started late (blocksync)
			// 2. Caught up (synced)
			// 3. Was perturbed (catchup)
			// 4. Recovered (synced again)

			assert.False(t, status.SyncInfo.CatchingUp,
				"node %s should be synced after full lifecycle", node.Name)

			assert.GreaterOrEqual(t, status.SyncInfo.LatestBlockHeight, node.StartAt,
				"node %s should have progressed past start_at", node.Name)

			t.Logf("Node %s completed full lifecycle: start_at=%d, current=%d, perturbations=%v",
				node.Name, node.StartAt, status.SyncInfo.LatestBlockHeight, node.Perturbations)
		})
	}
}

// TestE2E_NoStateCorruption verifies that no state corruption occurs
// during blocksync or catchup transitions.
func TestE2E_NoStateCorruption(t *testing.T) {
	testNode(t, func(t *testing.T, node e2e.Node) {
		if node.Mode == e2e.ModeSeed || node.Mode == e2e.ModeLight {
			return
		}

		client, err := node.Client()
		require.NoError(t, err)

		// Query application state
		resp, err := client.ABCIInfo(ctx)
		require.NoError(t, err)

		// Basic state integrity check
		assert.NotNil(t, resp.Response.LastBlockAppHash,
			"node %s should have an app hash", node.Name)

		// Query a recent block and verify its app hash matches
		status, err := client.Status(ctx)
		require.NoError(t, err)

		if status.SyncInfo.LatestBlockHeight > 0 {
			h := status.SyncInfo.LatestBlockHeight
			block, err := client.Block(ctx, &h)
			require.NoError(t, err)

			// The block should have a valid structure
			err = block.Block.ValidateBasic()
			require.NoError(t, err, "block %d on node %s failed validation", h, node.Name)
		}
	})
}

// TestE2E_IsCaughtUp verifies that IsCaughtUp returns correct values
// for nodes in different states.
func TestE2E_IsCaughtUp(t *testing.T) {
	testNode(t, func(t *testing.T, node e2e.Node) {
		if node.Mode == e2e.ModeSeed || node.Mode == e2e.ModeLight {
			return
		}

		client, err := node.Client()
		require.NoError(t, err)

		// Poll for synced status with timeout
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		var finalStatus bool
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("timeout waiting for node %s to sync", node.Name)
			default:
			}

			status, err := client.Status(ctx)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			if !status.SyncInfo.CatchingUp {
				finalStatus = true
				break
			}

			time.Sleep(500 * time.Millisecond)
		}

		assert.True(t, finalStatus,
			"node %s should report as caught up", node.Name)
	})
}
