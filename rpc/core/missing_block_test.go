package core

import (
	"testing"

	"github.com/stretchr/testify/assert"

	dbm "github.com/cometbft/cometbft-db"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/state/mocks"
)

// TestMissingBlockHeader tests that Header returns an error for missing blocks
func TestMissingBlockHeader(t *testing.T) {
	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	
	// Mock block store that returns nil for blocks within valid range but pruned
	mockstore := &mocks.BlockStore{}
	mockstore.On("Height").Return(int64(100))
	mockstore.On("Base").Return(int64(10))
	mockstore.On("LoadBlockMeta", int64(50)).Return(nil) // Valid height but pruned block
	env.BlockStore = mockstore

	// Test that requesting a valid but pruned block returns an error
	prunedHeight := int64(50)
	result, err := env.Header(&rpctypes.Context{}, &prunedHeight)
	
	// Should return an error, not an empty success response
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not available")
}

// TestMissingBlockHeaderByHash tests that HeaderByHash returns an error for missing blocks
func TestMissingBlockHeaderByHash(t *testing.T) {
	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	
	// Mock block store that returns nil for missing blocks
	mockstore := &mocks.BlockStore{}
	missingHash := []byte("missing_hash")
	mockstore.On("LoadBlockMetaByHash", missingHash).Return(nil) // Missing block
	env.BlockStore = mockstore

	// Test that requesting a missing block by hash returns an error
	result, err := env.HeaderByHash(&rpctypes.Context{}, missingHash)
	
	// Should return an error, not an empty success response
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not available")
}

// TestMissingBlock tests that Block returns an error for missing blocks
func TestMissingBlock(t *testing.T) {
	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	
	// Mock block store that returns nil for blocks within valid range but pruned
	mockstore := &mocks.BlockStore{}
	mockstore.On("Height").Return(int64(100))
	mockstore.On("Base").Return(int64(10))
	mockstore.On("LoadBlock", int64(50)).Return(nil) // Valid height but pruned block
	mockstore.On("LoadBlockMeta", int64(50)).Return(nil) // Valid height but pruned block meta
	env.BlockStore = mockstore

	// Test that requesting a valid but pruned block returns an error
	prunedHeight := int64(50)
	result, err := env.Block(&rpctypes.Context{}, &prunedHeight)
	
	// Should return an error, not an empty success response
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not available")
}

// TestMissingBlockByHash tests that BlockByHash returns an error for missing blocks
func TestMissingBlockByHash(t *testing.T) {
	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	
	// Mock block store that returns nil for missing blocks
	mockstore := &mocks.BlockStore{}
	missingHash := []byte("missing_hash")
	mockstore.On("LoadBlockByHash", missingHash).Return(nil) // Missing block
	env.BlockStore = mockstore

	// Test that requesting a missing block by hash returns an error
	result, err := env.BlockByHash(&rpctypes.Context{}, missingHash)
	
	// Should return an error, not an empty success response  
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not available")
}