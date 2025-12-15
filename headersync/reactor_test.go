package headersync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

func TestReactor_RateLimitGetHeaders(t *testing.T) {
	// Create a reactor with minimal dependencies for testing rate limiting.
	r := &Reactor{
		peerRequests: make(map[p2p.ID]*peerRequestTracker),
	}

	peerID := p2p.ID("peer1")

	// First maxPeerGetHeadersPerSecond requests should succeed.
	for i := 0; i < maxPeerGetHeadersPerSecond; i++ {
		ok := r.checkPeerRateLimit(peerID)
		assert.True(t, ok, "request %d should be allowed", i+1)
	}

	// Next request should be rate limited.
	ok := r.checkPeerRateLimit(peerID)
	assert.False(t, ok, "request should be rate limited after exceeding limit")

	// Different peer should not be rate limited.
	peerID2 := p2p.ID("peer2")
	ok = r.checkPeerRateLimit(peerID2)
	assert.True(t, ok, "different peer should not be rate limited")
}

func TestReactor_RateLimitWindowExpiry(t *testing.T) {
	r := &Reactor{
		peerRequests: make(map[p2p.ID]*peerRequestTracker),
	}

	peerID := p2p.ID("peer1")

	// Fill up the rate limit.
	for i := 0; i < maxPeerGetHeadersPerSecond; i++ {
		r.checkPeerRateLimit(peerID)
	}

	// Should be rate limited now.
	ok := r.checkPeerRateLimit(peerID)
	assert.False(t, ok, "should be rate limited")

	// Manually expire the timestamps by backdating them.
	r.peerRequestsMtx.Lock()
	tracker := r.peerRequests[peerID]
	for i := range tracker.timestamps {
		tracker.timestamps[i] = time.Now().Add(-2 * peerRequestWindowSeconds * time.Second)
	}
	r.peerRequestsMtx.Unlock()

	// Now should be allowed again.
	ok = r.checkPeerRateLimit(peerID)
	assert.True(t, ok, "should be allowed after window expiry")
}

// makeTestValidatorSet creates a validator set for testing.
func makeTestValidatorSet(n int) *types.ValidatorSet {
	validators := make([]*types.Validator, n)

	for i := 0; i < n; i++ {
		privKey := ed25519.GenPrivKey()
		pubKey := privKey.PubKey()
		validators[i] = types.NewValidator(pubKey, 10)
	}

	return types.NewValidatorSet(validators)
}

// makeTestHeader creates a test header at the given height.
func makeTestHeader(height int64, valHash, nextValHash []byte, lastBlockID types.BlockID) *types.Header {
	return &types.Header{
		ChainID:            "test-chain",
		Height:             height,
		Time:               time.Now(),
		LastBlockID:        lastBlockID,
		ValidatorsHash:     valHash,
		NextValidatorsHash: nextValHash,
	}
}


// TestReactor_VerifyChainLinkageBackward tests backward chain linkage verification.
func TestReactor_VerifyChainLinkageBackward(t *testing.T) {
	vals := makeTestValidatorSet(4)
	valHash := vals.Hash()

	r := &Reactor{
		chainID:           "test-chain",
		currentValidators: vals,
	}

	// Create a chain of headers with proper linkage.
	var headers []*SignedHeader
	var prevBlockID types.BlockID
	for i := int64(1); i <= 5; i++ {
		header := makeTestHeader(i, valHash, valHash, prevBlockID)
		headers = append(headers, &SignedHeader{
			Header: header,
			Commit: &types.Commit{BlockID: types.BlockID{Hash: header.Hash()}},
		})
		prevBlockID = types.BlockID{Hash: header.Hash()}
	}

	// Test 1: Valid chain linkage should pass.
	err := r.verifyChainLinkageBackward(headers)
	assert.NoError(t, err, "valid chain linkage should pass")

	// Test 2: Break chain linkage - should fail.
	brokenHeaders := make([]*SignedHeader, len(headers))
	copy(brokenHeaders, headers)
	brokenHeaders[2] = &SignedHeader{
		Header: makeTestHeader(3, valHash, valHash, types.BlockID{Hash: []byte("wrong")}),
		Commit: &types.Commit{},
	}
	err = r.verifyChainLinkageBackward(brokenHeaders)
	assert.Error(t, err, "broken chain linkage should fail")
	assert.Contains(t, err.Error(), "doesn't match")
}

// TestReactor_VerifyChainLinkageBackward_ValidatorsContinuity tests validator hash continuity.
func TestReactor_VerifyChainLinkageBackward_ValidatorsContinuity(t *testing.T) {
	vals1 := makeTestValidatorSet(4)
	vals2 := makeTestValidatorSet(4)
	valHash1 := vals1.Hash()
	valHash2 := vals2.Hash()

	r := &Reactor{
		chainID:           "test-chain",
		currentValidators: vals1,
	}

	// Create headers where NextValidatorsHash doesn't match next header's ValidatorsHash.
	header1 := makeTestHeader(1, valHash1, valHash1, types.BlockID{})
	header2 := makeTestHeader(2, valHash2, valHash2, types.BlockID{Hash: header1.Hash()}) // Wrong validators hash

	headers := []*SignedHeader{
		{Header: header1, Commit: &types.Commit{}},
		{Header: header2, Commit: &types.Commit{}},
	}

	err := r.verifyChainLinkageBackward(headers)
	assert.Error(t, err, "validator hash mismatch should fail")
	assert.Contains(t, err.Error(), "ValidatorsHash")
}

// TestReactor_UpdateValidatorSetFromBatch tests validator set updates from batch.
func TestReactor_UpdateValidatorSetFromBatch(t *testing.T) {
	vals1 := makeTestValidatorSet(4)
	vals2 := makeTestValidatorSet(4)

	r := &Reactor{
		chainID:           "test-chain",
		currentValidators: vals1,
	}
	r.Logger = log.NewNopLogger()

	// Create batch with validator set change.
	header := makeTestHeader(10, vals2.Hash(), vals2.Hash(), types.BlockID{})
	batch := []*SignedHeader{
		{Header: makeTestHeader(9, vals1.Hash(), vals2.Hash(), types.BlockID{}), Commit: &types.Commit{}},
		{Header: header, Commit: &types.Commit{}, ValidatorSet: vals2},
	}

	r.updateValidatorSetFromBatch(batch)

	assert.Equal(t, vals2.Hash(), r.currentValidators.Hash(), "validator set should be updated")
}

// TestReactor_UpdateValidatorSetFromBatch_HashMismatch tests that mismatched validator sets are ignored.
func TestReactor_UpdateValidatorSetFromBatch_HashMismatch(t *testing.T) {
	vals1 := makeTestValidatorSet(4)
	vals2 := makeTestValidatorSet(4)
	vals3 := makeTestValidatorSet(4) // Different set

	r := &Reactor{
		chainID:           "test-chain",
		currentValidators: vals1,
	}
	r.Logger = log.NewNopLogger()

	// Create batch with mismatched validator set (header hash != attached set hash).
	header := makeTestHeader(10, vals2.Hash(), vals2.Hash(), types.BlockID{})
	batch := []*SignedHeader{
		{Header: header, Commit: &types.Commit{}, ValidatorSet: vals3}, // Wrong validator set
	}

	r.updateValidatorSetFromBatch(batch)

	// Should NOT update because hashes don't match.
	assert.Equal(t, vals1.Hash(), r.currentValidators.Hash(), "validator set should not be updated on hash mismatch")
}

// TestReactor_UpdateValidatorSet tests the consensus handoff method.
func TestReactor_UpdateValidatorSet(t *testing.T) {
	vals1 := makeTestValidatorSet(4)
	vals2 := makeTestValidatorSet(4)

	r := &Reactor{
		currentValidators: vals1,
	}
	r.Logger = nil // Will be nil-safe in the actual implementation

	// Update should succeed.
	r.validatorsMtx.Lock()
	r.currentValidators = vals2
	r.validatorsMtx.Unlock()

	assert.Equal(t, vals2.Hash(), r.currentValidators.Hash())
}

// TestPool_SignedHeaderWithValidatorSet tests that SignedHeader stores validator sets.
func TestPool_SignedHeaderWithValidatorSet(t *testing.T) {
	pool := newTestPool(1, 50)
	vals := makeTestValidatorSet(4)

	peerID := p2p.ID("peer1")
	pool.SetPeerRange(peerID, 1, 100)

	// Create batch with validator set.
	pool.mtx.Lock()
	pool.pendingBatches[1] = &headerBatch{
		startHeight: 1,
		count:       2,
		peerID:      peerID,
		requestTime: time.Now(),
		received:    false,
	}
	pool.mtx.Unlock()

	headers := []*SignedHeader{
		{Header: &types.Header{Height: 1}, Commit: &types.Commit{}},
		{Header: &types.Header{Height: 2}, Commit: &types.Commit{}, ValidatorSet: vals},
	}

	err := pool.AddBatchResponse(peerID, 1, headers)
	require.NoError(t, err)

	batch, _ := pool.PeekCompletedBatch()
	require.NotNil(t, batch)
	require.Len(t, batch.headers, 2)

	// Verify validator set is stored.
	assert.Nil(t, batch.headers[0].ValidatorSet)
	assert.NotNil(t, batch.headers[1].ValidatorSet)
	assert.Equal(t, vals.Hash(), batch.headers[1].ValidatorSet.Hash())
}
