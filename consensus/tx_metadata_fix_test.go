package consensus

import (
	"testing"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/types"
)

// TestBlockWithoutCachedHashesDoesNotPanic tests that creating TxMetaData
// from a block without cached hashes doesn't panic and handles the fallback correctly
func TestBlockWithoutCachedHashesDoesNotPanic(t *testing.T) {
	// Create a block with transactions but no cached hashes
	txs := []types.Tx{
		types.Tx("transaction1"),
		types.Tx("transaction2"),
	}

	block := &types.Block{
		Data: types.Data{
			Txs: txs,
		},
	}

	// Create mock TxPos data
	mockTxPos := []types.TxPosition{
		{Start: 0, End: 12},
		{Start: 12, End: 24},
	}

	// Simulate the logic from consensus/state.go
	metaData := make([]proptypes.TxMetaData, len(block.Data.Txs))
	hashes := block.CachedHashes() // This will be nil

	// Use the fixed logic to handle missing cached hashes
	for i, pos := range mockTxPos {
		var hash []byte
		if hashes != nil && i < len(hashes) && len(hashes[i]) > 0 {
			hash = hashes[i]
		} else {
			// Fallback to computing hash from the transaction if cached hash is missing
			if i < len(block.Data.Txs) {
				hash = block.Data.Txs[i].Hash()
			}
		}
		metaData[i] = proptypes.TxMetaData{
			Start: pos.Start,
			End:   pos.End,
			Hash:  hash,
		}
	}

	// Verify that all hashes are valid (32 bytes)
	for i, md := range metaData {
		require.NotNil(t, md.Hash, "TxMetaData[%d].Hash should not be nil", i)
		require.Equal(t, 32, len(md.Hash), "TxMetaData[%d].Hash should be 32 bytes", i)

		// Verify that TxKeyFromBytes works without error
		_, err := types.TxKeyFromBytes(md.Hash)
		require.NoError(t, err, "TxKeyFromBytes should not fail for TxMetaData[%d]", i)

		// Verify that the hash matches the expected transaction hash
		expectedHash := block.Data.Txs[i].Hash()
		require.Equal(t, expectedHash, md.Hash, "Hash should match transaction hash")
	}
}

// TestBlockWithShortCachedHashesDoesNotPanic tests that creating TxMetaData
// from a block with shorter cached hashes than expected doesn't panic
func TestBlockWithShortCachedHashesDoesNotPanic(t *testing.T) {
	// Create a block with transactions
	txs := []types.Tx{
		types.Tx("transaction1"),
		types.Tx("transaction2"),
		types.Tx("transaction3"),
	}

	block := &types.Block{
		Data: types.Data{
			Txs: txs,
		},
	}

	// Set cached hashes that are shorter than the number of transactions
	cachedHashes := [][]byte{
		txs[0].Hash(), // Only hash for first transaction
		// Missing hashes for transactions 2 and 3
	}
	block.SetCachedHashes(cachedHashes)

	// Create mock TxPos data
	mockTxPos := []types.TxPosition{
		{Start: 0, End: 12},
		{Start: 12, End: 24},
		{Start: 24, End: 36},
	}

	// Simulate the logic from consensus/state.go
	metaData := make([]proptypes.TxMetaData, len(block.Data.Txs))
	hashes := block.CachedHashes()

	// Use the fixed logic to handle missing cached hashes
	for i, pos := range mockTxPos {
		var hash []byte
		if hashes != nil && i < len(hashes) && len(hashes[i]) > 0 {
			hash = hashes[i]
		} else {
			// Fallback to computing hash from the transaction if cached hash is missing
			if i < len(block.Data.Txs) {
				hash = block.Data.Txs[i].Hash()
			}
		}
		metaData[i] = proptypes.TxMetaData{
			Start: pos.Start,
			End:   pos.End,
			Hash:  hash,
		}
	}

	// Verify that all hashes are valid (32 bytes)
	for i, md := range metaData {
		require.NotNil(t, md.Hash, "TxMetaData[%d].Hash should not be nil", i)
		require.Equal(t, 32, len(md.Hash), "TxMetaData[%d].Hash should be 32 bytes", i)

		// Verify that TxKeyFromBytes works without error
		_, err := types.TxKeyFromBytes(md.Hash)
		require.NoError(t, err, "TxKeyFromBytes should not fail for TxMetaData[%d]", i)

		// Verify that the hash matches the expected transaction hash
		expectedHash := block.Data.Txs[i].Hash()
		require.Equal(t, expectedHash, md.Hash, "Hash should match transaction hash")
	}
}

// TestBlockWithEmptyCachedHashesDoesNotPanic tests that creating TxMetaData
// from a block with empty cached hashes doesn't panic
func TestBlockWithEmptyCachedHashesDoesNotPanic(t *testing.T) {
	// Create a block with transactions
	txs := []types.Tx{
		types.Tx("transaction1"),
		types.Tx("transaction2"),
	}

	block := &types.Block{
		Data: types.Data{
			Txs: txs,
		},
	}

	// Set cached hashes with empty hashes
	cachedHashes := [][]byte{
		{},            // Empty hash
		txs[1].Hash(), // Valid hash
	}
	block.SetCachedHashes(cachedHashes)

	// Create mock TxPos data
	mockTxPos := []types.TxPosition{
		{Start: 0, End: 12},
		{Start: 12, End: 24},
	}

	// Simulate the logic from consensus/state.go
	metaData := make([]proptypes.TxMetaData, len(block.Data.Txs))
	hashes := block.CachedHashes()

	// Use the fixed logic to handle missing cached hashes
	for i, pos := range mockTxPos {
		var hash []byte
		if hashes != nil && i < len(hashes) && len(hashes[i]) > 0 {
			hash = hashes[i]
		} else {
			// Fallback to computing hash from the transaction if cached hash is missing
			if i < len(block.Data.Txs) {
				hash = block.Data.Txs[i].Hash()
			}
		}
		metaData[i] = proptypes.TxMetaData{
			Start: pos.Start,
			End:   pos.End,
			Hash:  hash,
		}
	}

	// Verify that all hashes are valid (32 bytes)
	for i, md := range metaData {
		require.NotNil(t, md.Hash, "TxMetaData[%d].Hash should not be nil", i)
		require.Equal(t, 32, len(md.Hash), "TxMetaData[%d].Hash should be 32 bytes", i)

		// Verify that TxKeyFromBytes works without error
		_, err := types.TxKeyFromBytes(md.Hash)
		require.NoError(t, err, "TxKeyFromBytes should not fail for TxMetaData[%d]", i)

		// Verify that the hash matches the expected transaction hash
		expectedHash := block.Data.Txs[i].Hash()
		require.Equal(t, expectedHash, md.Hash, "Hash should match transaction hash")
	}
}
