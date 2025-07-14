package types

import (
	"testing"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
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
	_, err := TxKeyFromBytes(txMetaData.Hash)
	if err == nil {
		t.Errorf("Expected error but got nil")
	}
	
	expectedError := "incorrect tx key size. Expected 32 bytes, got 0"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
	
	t.Logf("Successfully reproduced the issue: %s", err.Error())
}