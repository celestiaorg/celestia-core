package tests

import (
	"testing"

	"github.com/celestiaorg/celestia-core/types"
	"github.com/stretchr/testify/require"
)

func TestConsensusBreakingChanges(t *testing.T) {
	// Test case 1: Early adoption of ABCI++ methods
	{
		// Arrange
		proposal := types.NewProposal("test_proposal")

		// Act
		prepareProposalResponse := types.PrepareProposal(proposal)
		processProposalResponse := types.ProcessProposal(proposal)

		// Assert
		require.NotNil(t, prepareProposalResponse)
		require.NotNil(t, processProposalResponse)
	}

	// Test case 2: Modifications to DataHash in block header
	{
		// Arrange
		block := types.NewBlock(1, "test_block")

		// Act
		dataHash := block.DataHash()

		// Assert
		require.NotNil(t, dataHash)
	}

	// Test case 3: GRPC server modifications
	{
		// Arrange
		blockAPI := types.NewBlockAPI()
		blobstreamAPI := types.NewBlobstreamAPI()

		// Act
		blockResponse := blockAPI.Block()
		blobstreamResponse := blobstreamAPI.Blobstream()

		// Assert
		require.NotNil(t, blockResponse)
		require.NotNil(t, blobstreamResponse)
	}
}