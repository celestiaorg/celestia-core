package headersync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cometbft/cometbft/p2p"
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
