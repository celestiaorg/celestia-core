package headersync

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

const (
	// requestIntervalMS is the interval between making new header requests.
	requestIntervalMS = 10

	// peerConnWait is the time to wait for peers to connect before starting requests.
	peerConnWait = 3 * time.Second

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
	numPending int32
	didTimeout bool
}

// HeaderPool manages peer connections and header requests.
type HeaderPool struct {
	service.BaseService
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

	// Channels for communication
	requestsCh chan<- HeaderBatchRequest
	errorsCh   chan<- peerError
}

// NewHeaderPool returns a new HeaderPool.
func NewHeaderPool(
	startHeight int64,
	batchSize int64,
	requestsCh chan<- HeaderBatchRequest,
	errorsCh chan<- peerError,
) *HeaderPool {
	if batchSize < 1 {
		batchSize = MaxHeaderBatchSize
	}
	if batchSize > MaxHeaderBatchSize {
		batchSize = MaxHeaderBatchSize
	}

	pool := &HeaderPool{
		peers:             make(map[p2p.ID]*hsPeer),
		sortedPeers:       make([]*hsPeer, 0),
		bannedPeers:       make(map[p2p.ID]time.Time),
		pendingBatches:    make(map[int64]*headerBatch),
		height:            startHeight,
		batchSize:         batchSize,
		maxPendingBatches: defaultMaxPendingBatches,
		requestTimeout:    requestTimeout,
		requestsCh:        requestsCh,
		errorsCh:          errorsCh,
	}
	pool.BaseService = *service.NewBaseService(nil, "HeaderPool", pool)
	return pool
}

// OnStart implements service.Service.
func (pool *HeaderPool) OnStart() error {
	pool.startTime = time.Now()
	go pool.makeRequestersRoutine()
	return nil
}

// makeRequestersRoutine spawns batch requests as needed.
func (pool *HeaderPool) makeRequestersRoutine() {
	// Wait for peers to connect.
	if time.Since(pool.startTime) < peerConnWait {
		sleepDuration := peerConnWait - time.Since(pool.startTime)
		time.Sleep(sleepDuration)
	}

	for {
		if !pool.IsRunning() {
			return
		}

		pool.mtx.Lock()

		// Clean up timed out requests and peers.
		pool.cleanupTimedOut()

		// Check if we need to make more requests.
		numPending := len(pool.pendingBatches)
		maxPendingReached := numPending >= pool.maxPendingBatches
		maxHeightReached := pool.height > pool.maxPeerHeight

		if !maxPendingReached && !maxHeightReached && len(pool.peers) > 0 {
			pool.makeNextBatchRequest()
		}

		pool.mtx.Unlock()

		time.Sleep(requestIntervalMS * time.Millisecond)
	}
}

// cleanupTimedOut removes timed out requests and peers.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) cleanupTimedOut() {
	now := time.Now()

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
			pool.redoBatch(startHeight)
			pool.sendError(errors.New("header batch request timed out"), batch.peerID)
		}
	}

	// Clean up banned peers.
	for peerID, banTime := range pool.bannedPeers {
		if now.Sub(banTime) > banDuration {
			delete(pool.bannedPeers, peerID)
		}
	}
}

// makeNextBatchRequest creates a new batch request for the next height range.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) makeNextBatchRequest() {
	// Find the next height range to request.
	startHeight := pool.height
	for {
		if _, exists := pool.pendingBatches[startHeight]; !exists {
			break
		}
		startHeight += pool.batchSize
		if startHeight > pool.maxPeerHeight {
			return // All heights are covered.
		}
	}

	count := pool.batchSize
	if startHeight+count-1 > pool.maxPeerHeight {
		count = pool.maxPeerHeight - startHeight + 1
	}
	if count < 1 {
		return
	}

	// Pick a peer that has the requested height range.
	peer := pool.pickPeer(startHeight)
	if peer == nil {
		return
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
	peer.numPending++

	// Send request via channel.
	select {
	case pool.requestsCh <- HeaderBatchRequest{
		StartHeight: startHeight,
		Count:       count,
		PeerID:      peer.id,
	}:
	default:
		pool.Logger.Debug("Request channel full, dropping request",
			"startHeight", startHeight)
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
	if peer != nil {
		// DoS protection: peers must only send updates when height increases.
		// Sending the same height or lower indicates misbehavior.
		if height <= peer.height {
			pool.Logger.Info("Peer sent status update with non-increasing height, disconnecting",
				"peer", peerID,
				"height", height,
				"prevHeight", peer.height)
			pool.removePeer(peerID)
			pool.banPeer(peerID)
			return false
		}
		// Also check base hasn't decreased.
		if base < peer.base {
			pool.Logger.Info("Peer sent status update with decreased base, disconnecting",
				"peer", peerID,
				"base", base,
				"prevBase", peer.base)
			pool.removePeer(peerID)
			pool.banPeer(peerID)
			return false
		}
		peer.base = base
		peer.height = height
	} else {
		peer = &hsPeer{
			id:     peerID,
			base:   base,
			height: height,
		}
		pool.peers[peerID] = peer
		pool.sortedPeers = append(pool.sortedPeers, peer)
	}

	if height > pool.maxPeerHeight {
		pool.maxPeerHeight = height
	}

	pool.sortPeers()
	return true
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
			pool.redoBatchUnsafe(startHeight)
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
func (pool *HeaderPool) AddBatchResponse(peerID p2p.ID, headers []*SignedHeader) error {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	// Find the batch this response belongs to.
	var batch *headerBatch
	for _, b := range pool.pendingBatches {
		if b.peerID == peerID && !b.received {
			batch = b
			break
		}
	}

	if batch == nil {
		return fmt.Errorf("no pending batch from peer %s", peerID)
	}

	batch.headers = headers
	batch.received = true

	// Decrease pending count for peer and clear timeout flag on successful response.
	if peer := pool.peers[peerID]; peer != nil {
		peer.numPending--
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
	pool.redoBatch(startHeight)
}

// redoBatch marks a batch as needing to be re-requested.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) redoBatch(startHeight int64) {
	batch := pool.pendingBatches[startHeight]
	if batch == nil {
		return
	}

	// Decrease pending count for peer.
	if peer := pool.peers[batch.peerID]; peer != nil && !batch.received {
		peer.numPending--
	}

	delete(pool.pendingBatches, startHeight)
}

// redoBatchUnsafe is like redoBatch but doesn't modify peer pending count.
// CONTRACT: pool.mtx must be held.
func (pool *HeaderPool) redoBatchUnsafe(startHeight int64) {
	delete(pool.pendingBatches, startHeight)
}

// sendError sends an error to the error channel.
func (pool *HeaderPool) sendError(err error, peerID p2p.ID) {
	if !pool.IsRunning() {
		return
	}
	select {
	case pool.errorsCh <- peerError{err, peerID}:
	default:
	}
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
