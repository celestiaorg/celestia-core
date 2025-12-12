package headersync

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

const (
	// requestTimeout is the default timeout for a batch request.
	requestTimeout = 15 * time.Second

	// defaultMaxPendingBatches is the default maximum number of concurrent batch requests.
	defaultMaxPendingBatches = 10

	// banDuration is how long a misbehaving peer is banned.
	banDuration = 60 * time.Second
)

// SignedHeader pairs a header with its commit.
type SignedHeader struct {
	Header *types.Header
	Commit *types.Commit
}

// VerifiedHeader contains a header that has been fully verified.
type VerifiedHeader struct {
	Header  *types.Header
	Commit  *types.Commit
	BlockID types.BlockID
}

// HeaderBatchRequest represents a request for a range of headers.
type HeaderBatchRequest struct {
	StartHeight int64
	Count       int64
	PeerID      p2p.ID
}

// headerBatch tracks a pending batch request.
type headerBatch struct {
	startHeight int64
	count       int64
	peerID      p2p.ID
	requestTime time.Time
	headers     []*SignedHeader // filled in as response arrives
	received    bool
}

// peerError represents an error associated with a peer.
type peerError struct {
	err    error
	peerID p2p.ID
}

func (e peerError) Error() string {
	return fmt.Sprintf("error with peer %v: %s", e.peerID, e.err.Error())
}

// hsPeer represents a peer in the header sync pool.
type hsPeer struct {
	id         p2p.ID
	base       int64
	height     int64
	didTimeout bool
}

// HeaderPool manages peer connections and header requests.
// It is a passive data structure - the reactor drives all logic.
type HeaderPool struct {
	Logger log.Logger

	mtx            sync.Mutex
	peers          map[p2p.ID]*hsPeer
	sortedPeers    []*hsPeer // sorted by height, highest first
	bannedPeers    map[p2p.ID]time.Time
	pendingBatches map[int64]*headerBatch // startHeight -> batch
	height         int64                  // Next header height to request
	maxPeerHeight  int64                  // Highest header height reported by any peer

	startTime         time.Time
	batchSize         int64
	maxPendingBatches int
	requestTimeout    time.Duration
}

// NewHeaderPool returns a new HeaderPool.
func NewHeaderPool(startHeight, batchSize int64) *HeaderPool {
	if batchSize < 1 {
		batchSize = MaxHeaderBatchSize
	}
	if batchSize > MaxHeaderBatchSize {
		batchSize = MaxHeaderBatchSize
	}

	return &HeaderPool{
		peers:             make(map[p2p.ID]*hsPeer),
		sortedPeers:       make([]*hsPeer, 0),
		bannedPeers:       make(map[p2p.ID]time.Time),
		pendingBatches:    make(map[int64]*headerBatch),
		height:            startHeight,
		batchSize:         batchSize,
		maxPendingBatches: defaultMaxPendingBatches,
		requestTimeout:    requestTimeout,
		startTime:         time.Now(),
	}
}

// GetNextRequest returns the next batch request to make, if any.
// Returns nil if no request should be made.
// Also cleans up timed out requests and marks those peers as timed out.
func (pool *HeaderPool) GetNextRequest() *HeaderBatchRequest {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	pool.cleanupTimedOut()

	if len(pool.peers) == 0 {
		return nil
	}
	if len(pool.pendingBatches) >= pool.maxPendingBatches {
		return nil
	}
	if pool.height > pool.maxPeerHeight {
		return nil
	}

	return pool.makeNextBatchRequest()
}

// cleanupTimedOut removes timed out requests.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) cleanupTimedOut() []p2p.ID {
	now := time.Now()
	var timedOutPeers []p2p.ID

	// Check for timed out batch requests.
	for startHeight, batch := range pool.pendingBatches {
		if !batch.received && now.Sub(batch.requestTime) > pool.requestTimeout {
			pool.Logger.Debug("Header batch request timed out",
				"startHeight", startHeight,
				"peer", batch.peerID)
			// Mark peer as timed out so we try a different peer next time.
			if peer := pool.peers[batch.peerID]; peer != nil {
				peer.didTimeout = true
			}
			delete(pool.pendingBatches, startHeight)
			timedOutPeers = append(timedOutPeers, batch.peerID)
		}
	}

	// Clean up banned peers.
	for peerID, banTime := range pool.bannedPeers {
		if now.Sub(banTime) > banDuration {
			delete(pool.bannedPeers, peerID)
		}
	}

	return timedOutPeers
}

// makeNextBatchRequest creates a new batch request for the next height range.
// Returns the request, or nil if no request should be made.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) makeNextBatchRequest() *HeaderBatchRequest {
	// Find the next height range to request, skipping heights already covered by pending batches.
	startHeight := pool.height
	for {
		batch := pool.findPendingBatchForHeight(startHeight)
		if batch == nil {
			break
		}
		startHeight = batch.startHeight + batch.count
		if startHeight > pool.maxPeerHeight {
			return nil // All heights are covered.
		}
	}

	count := pool.batchSize
	if startHeight+count-1 > pool.maxPeerHeight {
		count = pool.maxPeerHeight - startHeight + 1
	}
	if count < 1 {
		return nil
	}

	// Pick a peer that has the requested height range.
	peer := pool.pickPeer(startHeight)
	if peer == nil {
		return nil
	}

	batch := &headerBatch{
		startHeight: startHeight,
		count:       count,
		peerID:      peer.id,
		requestTime: time.Now(),
		headers:     nil,
		received:    false,
	}
	pool.pendingBatches[startHeight] = batch

	return &HeaderBatchRequest{
		StartHeight: startHeight,
		Count:       count,
		PeerID:      peer.id,
	}
}

// pickPeer selects a peer that has headers for the given height.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) pickPeer(height int64) *hsPeer {
	for _, peer := range pool.sortedPeers {
		if peer.didTimeout {
			continue
		}
		if height >= peer.base && height <= peer.height {
			return peer
		}
	}
	return nil
}

// SetPeerRange sets the peer's reported blockchain base and height.
// Returns true if the update was accepted, false if the peer should be disconnected.
// Peers should only send updates when their height increases. Sending an update
// for the same or lower height is considered a DoS attempt and results in disconnection.
func (pool *HeaderPool) SetPeerRange(peerID p2p.ID, base, height int64) bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if pool.isPeerBanned(peerID) {
		return false
	}

	peer := pool.peers[peerID]

	// New peer: add and return early.
	if peer == nil {
		peer = &hsPeer{
			id:     peerID,
			base:   base,
			height: height,
		}
		pool.peers[peerID] = peer
		pool.sortedPeers = append(pool.sortedPeers, peer)
		pool.updateMaxPeerHeightIfNeeded(height)
		pool.sortPeers()
		return true
	}

	// Existing peer - DoS protection checks.
	if height <= peer.height {
		pool.Logger.Error("Peer sent status update with non-increasing height, disconnecting",
			"peer", peerID,
			"height", height,
			"prevHeight", peer.height)
		pool.removePeer(peerID)
		pool.banPeer(peerID)
		return false
	}
	if base < peer.base {
		pool.Logger.Error("Peer sent status update with decreased base, disconnecting",
			"peer", peerID,
			"base", base,
			"prevBase", peer.base)
		pool.removePeer(peerID)
		pool.banPeer(peerID)
		return false
	}

	peer.base = base
	peer.height = height
	pool.updateMaxPeerHeightIfNeeded(height)
	pool.sortPeers()
	return true
}

// updateMaxPeerHeightIfNeeded updates maxPeerHeight if the given height is higher.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) updateMaxPeerHeightIfNeeded(height int64) {
	if height > pool.maxPeerHeight {
		pool.maxPeerHeight = height
	}
}

// RemovePeer removes the peer from the pool.
func (pool *HeaderPool) RemovePeer(peerID p2p.ID) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	pool.removePeer(peerID)
}

// removePeer removes the peer and redoes its pending requests.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) removePeer(peerID p2p.ID) {
	// Redo pending requests from this peer.
	for startHeight, batch := range pool.pendingBatches {
		if batch.peerID == peerID && !batch.received {
			delete(pool.pendingBatches, startHeight)
		}
	}

	peer := pool.peers[peerID]
	if peer == nil {
		return
	}

	delete(pool.peers, peerID)
	for i, p := range pool.sortedPeers {
		if p.id == peerID {
			pool.sortedPeers = append(pool.sortedPeers[:i], pool.sortedPeers[i+1:]...)
			break
		}
	}

	// Update max peer height if needed.
	if peer.height == pool.maxPeerHeight {
		pool.updateMaxPeerHeight()
	}
}

// updateMaxPeerHeight recalculates the maximum peer height.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) updateMaxPeerHeight() {
	var max int64
	for _, peer := range pool.peers {
		if peer.height > max {
			max = peer.height
		}
	}
	pool.maxPeerHeight = max
}

// banPeer adds a peer to the banned list.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) banPeer(peerID p2p.ID) {
	pool.bannedPeers[peerID] = time.Now()
}

// isPeerBanned checks if a peer is banned.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) isPeerBanned(peerID p2p.ID) bool {
	banTime, exists := pool.bannedPeers[peerID]
	if !exists {
		return false
	}
	return time.Since(banTime) < banDuration
}

// IsPeerBanned checks if a peer is banned (thread-safe).
func (pool *HeaderPool) IsPeerBanned(peerID p2p.ID) bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.isPeerBanned(peerID)
}

// sortPeers sorts peers by height, highest first.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) sortPeers() {
	sort.Slice(pool.sortedPeers, func(i, j int) bool {
		return pool.sortedPeers[i].height > pool.sortedPeers[j].height
	})
}

// AddBatchResponse adds a response to a pending batch request.
// Uses startHeight for O(1) lookup instead of scanning all pending batches.
func (pool *HeaderPool) AddBatchResponse(peerID p2p.ID, startHeight int64, headers []*SignedHeader) error {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	batch := pool.pendingBatches[startHeight]
	if batch == nil {
		return fmt.Errorf("no pending batch at height %d", startHeight)
	}
	if batch.peerID != peerID {
		return fmt.Errorf("batch at height %d is from peer %s, not %s", startHeight, batch.peerID, peerID)
	}
	if batch.received {
		return fmt.Errorf("batch at height %d already received", startHeight)
	}

	batch.headers = headers
	batch.received = true

	// Clear timeout flag on successful response.
	if peer := pool.peers[peerID]; peer != nil {
		peer.didTimeout = false
	}

	return nil
}

// PeekCompletedBatch returns the next completed batch in height order, if available.
func (pool *HeaderPool) PeekCompletedBatch() (*headerBatch, p2p.ID) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	batch := pool.pendingBatches[pool.height]
	if batch == nil || !batch.received {
		return nil, ""
	}
	return batch, batch.peerID
}

// PeekNextHeader returns the next header to verify, if available.
// This enables streaming: headers can be verified as soon as they arrive in order,
// without waiting for the entire batch to complete.
// Returns the signed header, peer ID, and the batch's start height (for error handling).
func (pool *HeaderPool) PeekNextHeader() (*SignedHeader, p2p.ID, int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	batch := pool.findReceivedBatchForHeight(pool.height)
	if batch == nil {
		return nil, "", 0
	}

	// Calculate the index within this batch
	idx := int(pool.height - batch.startHeight)
	if idx < 0 || idx >= len(batch.headers) {
		return nil, "", 0
	}

	return batch.headers[idx], batch.peerID, batch.startHeight
}

// findPendingBatchForHeight finds any pending batch (received or not) that covers the given height.
// Used to check if a height range is already being requested.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) findPendingBatchForHeight(height int64) *headerBatch {
	for startHeight, batch := range pool.pendingBatches {
		if height >= startHeight && height < startHeight+batch.count {
			return batch
		}
	}
	return nil
}

// findReceivedBatchForHeight finds a batch with received headers that contains the given height.
// Used to find headers for processing.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) findReceivedBatchForHeight(height int64) *headerBatch {
	for startHeight, batch := range pool.pendingBatches {
		if !batch.received {
			continue
		}
		if height >= startHeight && height < startHeight+int64(len(batch.headers)) {
			return batch
		}
	}
	return nil
}

// MarkHeaderProcessed marks the current header as processed and advances.
// Returns true if the batch is complete and should be removed.
func (pool *HeaderPool) MarkHeaderProcessed() bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	batch := pool.findReceivedBatchForHeight(pool.height)
	if batch == nil {
		return false
	}

	pool.height++

	// Check if we've completed this batch
	complete := pool.height >= batch.startHeight+int64(len(batch.headers))
	if complete {
		delete(pool.pendingBatches, batch.startHeight)
	}

	return complete
}

// PopBatch removes a completed batch from the pool and advances the height.
func (pool *HeaderPool) PopBatch(startHeight int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	batch := pool.pendingBatches[startHeight]
	if batch == nil {
		return
	}

	delete(pool.pendingBatches, startHeight)

	// Advance pool height past this batch.
	if startHeight == pool.height {
		pool.height = startHeight + int64(len(batch.headers))
	}
}

// RedoBatch marks a batch as needing to be re-requested.
func (pool *HeaderPool) RedoBatch(startHeight int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	delete(pool.pendingBatches, startHeight)
}

// GetStatus returns the pool's current status.
func (pool *HeaderPool) GetStatus() (height int64, numPending int, numPeers int) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.height, len(pool.pendingBatches), len(pool.peers)
}

// Height returns the pool's current height.
func (pool *HeaderPool) Height() int64 {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.height
}

// MaxPeerHeight returns the highest reported header height.
func (pool *HeaderPool) MaxPeerHeight() int64 {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.maxPeerHeight
}

// IsCaughtUp returns true if we've caught up to peers.
func (pool *HeaderPool) IsCaughtUp() bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if len(pool.peers) == 0 {
		return false
	}

	receivedHeaderOrTimedOut := pool.height > 0 || time.Since(pool.startTime) > 5*time.Second
	ourChainIsLongestAmongPeers := pool.maxPeerHeight == 0 || pool.height >= pool.maxPeerHeight
	return receivedHeaderOrTimedOut && ourChainIsLongestAmongPeers
}

// ResetHeight resets the pool to start syncing from the given height.
// This is used when the node state jumps (e.g. after state sync).
func (pool *HeaderPool) ResetHeight(height int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	pool.height = height
	// Clear pending batches as they are likely for old heights
	pool.pendingBatches = make(map[int64]*headerBatch)
}
