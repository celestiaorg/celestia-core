package e2e_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	e2e "github.com/tendermint/tendermint/test/e2e/pkg"
)

// Tests that block headers are identical across nodes where present.
func TestBlock_Header(t *testing.T) {
	blocks := fetchBlockChain(t)
	testNode(t, func(t *testing.T, node e2e.Node) {
		t.Helper()
		if node.Mode == e2e.ModeSeed {
			return
		}

		client, err := node.Client()
		require.NoError(t, err)
		status, err := client.Status(ctx)
		require.NoError(t, err)

		first := status.SyncInfo.EarliestBlockHeight
		last := status.SyncInfo.LatestBlockHeight
		if node.RetainBlocks > 0 {
			first++ // avoid race conditions with block pruning
		}

		for _, block := range blocks {
			if block.Header.Height < first {
				continue
			}
			if block.Header.Height > last {
				break
			}
			resp, err := client.Block(ctx, &block.Header.Height)
			require.NoError(t, err)

			require.Equal(t, block, resp.Block,
				"block mismatch for height %d", block.Header.Height)

			require.NoError(t, resp.Block.ValidateBasic(),
				"block at height %d is invalid", block.Header.Height)
		}
	})
}

// Tests that the node contains the expected block range.
func TestBlock_Range(t *testing.T) {
	testNode(t, func(t *testing.T, node e2e.Node) {
		t.Helper()
		if node.Mode == e2e.ModeSeed {
			return
		}

		client, err := node.Client()
		require.NoError(t, err)
		status, err := client.Status(ctx)
		require.NoError(t, err)

		first := status.SyncInfo.EarliestBlockHeight
		last := status.SyncInfo.LatestBlockHeight

		switch {
		case node.StateSync:
			assert.Greater(t, first, node.Testnet.InitialHeight,
				"state synced nodes should not contain network's initial height")

		case node.RetainBlocks > 0 && int64(node.RetainBlocks) < (last-node.Testnet.InitialHeight+1):
			// Delta handles race conditions in reading first/last heights.
			assert.InDelta(t, node.RetainBlocks, last-first+1, 1,
				"node not pruning expected blocks")

		default:
			assert.Equal(t, node.Testnet.InitialHeight, first,
				"node's first block should be network's initial height")
		}

		for h := first; h <= last; h++ {
			resp, err := client.Block(ctx, &(h))
			if err != nil && node.RetainBlocks > 0 && h == first {
				// Ignore errors in first block if node is pruning blocks due to race conditions.
				continue
			}
			require.NoError(t, err)
			assert.Equal(t, h, resp.Block.Height)
		}

		for h := node.Testnet.InitialHeight; h < first; h++ {
			_, err := client.Block(ctx, &(h))
			require.Error(t, err)
		}
	})
}

func TestBlock_SignedData(t *testing.T) {
	testNode(t, func(t *testing.T, node e2e.Node) {
		t.Helper()
		client, err := node.Client()
		require.NoError(t, err)

		resp, err := client.SignedBlock(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, resp.Header.Height, resp.Commit.Height)

		err = resp.ValidatorSet.VerifyCommit(resp.Header.ChainID, resp.Commit.BlockID, resp.Header.Height, &resp.Commit)
		require.NoError(t, err)

		if !bytes.Equal(resp.Commit.BlockID.Hash, resp.Header.Hash()) {
			t.Fatal("commit is for a different block")
		}

		if !bytes.Equal(resp.Header.DataHash, resp.Data.Hash()) {
			t.Fatal("data does not match header	data hash")
		}
	})
}
