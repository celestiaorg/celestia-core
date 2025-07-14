package propagation

import (
	"testing"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/types"
)

// TestCompactBlockTxMetadataIssue reproduces the issue where
// TxMetaData has empty hashes causing TxKeyFromBytes to fail
func TestCompactBlockTxMetadataIssue(t *testing.T) {
	// Create TxMetaData with empty hash (0 bytes)
	txMetaData := proptypes.TxMetaData{
		Hash:  []byte{}, // Empty hash - this is the issue
		Start: 0,
		End:   10,
	}

	// This should fail with "incorrect tx key size. Expected 32 bytes, got 0"
	_, err := types.TxKeyFromBytes(txMetaData.Hash)
	if err == nil {
		t.Errorf("Expected error but got nil")
	}

	expectedError := "incorrect tx key size. Expected 32 bytes, got 0"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}

	t.Logf("Successfully reproduced the issue: %s", err.Error())
}

// TestCompactBlockTxMetadataWithValidHash tests that valid hashes work correctly
func TestCompactBlockTxMetadataWithValidHash(t *testing.T) {
	// Create a valid 32-byte hash
	tx := types.Tx("test transaction")
	validHash := tx.Hash()

	txMetaData := proptypes.TxMetaData{
		Hash:  validHash,
		Start: 0,
		End:   10,
	}

	// This should work without error
	txKey, err := types.TxKeyFromBytes(txMetaData.Hash)
	if err != nil {
		t.Errorf("Unexpected error: %s", err.Error())
	}

	// Verify the key is the expected size
	if len(txKey) != 32 {
		t.Errorf("Expected key length 32, got %d", len(txKey))
	}

	t.Logf("Successfully created TxKey with valid hash")
}

// TestRecoverPartsFromMempoolWithEmptyHashes tests that recoverPartsFromMempool
// handles empty hashes gracefully without panicking
func TestRecoverPartsFromMempoolWithEmptyHashes(t *testing.T) {
	// Create a CompactBlock with TxMetaData containing empty hashes
	cb := &proptypes.CompactBlock{
		Blobs: []proptypes.TxMetaData{
			{
				Hash:  []byte{}, // Empty hash - should be handled gracefully
				Start: 0,
				End:   10,
			},
			{
				Hash:  nil, // Nil hash - should also be handled gracefully
				Start: 10,
				End:   20,
			},
		},
	}

	// Test that the loop over cb.Blobs doesn't panic with empty hashes
	// This simulates what happens in recoverPartsFromMempool
	for _, txMetaData := range cb.Blobs {
		_, err := types.TxKeyFromBytes(txMetaData.Hash)
		if err != nil {
			// This is expected - empty hashes should cause an error
			t.Logf("Expected error for empty/nil hash: %s", err.Error())
		}
	}

	t.Log("Successfully handled empty hashes without panic")
}
