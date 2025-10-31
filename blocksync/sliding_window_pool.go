package blocksync

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

const (
	// Default sliding window size (can be configurable)
	defaultWindowSize = 20

	// Maximum number of concurrent requesters
	// This should be <= windowSize to prevent requesting outside window
	defaultMaxRequesters = 20
)

// SlidingWindowPool manages block requests using a sliding window buffer.
// It enforces:
// 1. Only blocks within [baseHeight, baseHeight+windowSize) can be requested
// 2. Maximum number of concurrent requesters is limited
// 3. Blocks are validated and saved sequentially
type SlidingWindowPool struct {
	service.BaseService

	mtx sync.Mutex

	// Core components
	buffer        *BlockBuffer
	maxRequesters int32

	// Peer management (reuse existing pool structures)
	peers         map[p2p.ID]*bpPeer
	sortedPeers   []*bpPeer
	maxPeerHeight int64

	// Requester management
	requesters map[int64]*bpRequester
	numPending int32

	// Channels for communication
	requestsCh chan<- BlockRequest
	errorsCh   chan<- peerError

	// State
	startTime time.Time

	// Peer bans
	bannedPeers map[p2p.ID]time.Time
	banDuration time.Duration

	tracer trace.Tracer

	logger log.Logger
}

// NewSlidingWindowPool creates a new pool with sliding window semantics
func NewSlidingWindowPool(
	startHeight int64,
	windowSize int64,
	maxRequesters int32,
	requestsCh chan<- BlockRequest,
	errorsCh chan<- peerError,
	logger log.Logger,
	traceClient trace.Tracer,
) *SlidingWindowPool {

	if windowSize <= 0 {
		windowSize = defaultWindowSize
	}

	if maxRequesters <= 0 {
		maxRequesters = defaultMaxRequesters
	}

	// Ensure maxRequesters doesn't exceed window size
	if maxRequesters > int32(windowSize) {
		maxRequesters = int32(windowSize)
	}

	pool := &SlidingWindowPool{
		buffer:        NewBlockBuffer(startHeight, windowSize, logger),
		maxRequesters: maxRequesters,
		peers:         make(map[p2p.ID]*bpPeer),
		requesters:    make(map[int64]*bpRequester),
		requestsCh:    requestsCh,
		errorsCh:      errorsCh,
		bannedPeers:   make(map[p2p.ID]time.Time),
		banDuration:   60 * time.Second,
		logger:        logger,
		tracer:        traceClient,
	}

	pool.BaseService = *service.NewBaseService(logger, "SlidingWindowPool", pool)
	return pool
}

// OnStart implements service.Service
func (pool *SlidingWindowPool) OnStart() error {
	pool.startTime = time.Now()
	go pool.makeRequestersRoutine()
	return nil
}

// OnStop implements service.Service
func (pool *SlidingWindowPool) OnStop() {
	// Stop all requesters
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	for _, requester := range pool.requesters {
		requester.Stop()
	}
}

// makeRequestersRoutine creates new requesters within the sliding window
func (pool *SlidingWindowPool) makeRequestersRoutine() {
	for {
		if !pool.IsRunning() {
			return
		}

		// Wait for initial peer connections
		if time.Since(pool.startTime) < peerConnWait {
			sleepDuration := peerConnWait - time.Since(pool.startTime)
			time.Sleep(sleepDuration)
			continue
		}

		pool.mtx.Lock()

		// Calculate next height to request
		baseHeight := pool.buffer.BaseHeight()
		maxHeight := pool.buffer.MaxHeight()
		numRequesters := len(pool.requesters)

		// Find next missing height within window
		var nextHeight int64 = -1
		for h := baseHeight; h <= maxHeight; h++ {
			if _, exists := pool.requesters[h]; !exists && !pool.buffer.HasBlock(h) {
				nextHeight = h
				break
			}
		}

		// Check if we should create a new requester
		shouldCreate := nextHeight != -1 &&
			numRequesters < int(pool.maxRequesters) &&
			len(pool.peers) > 0

		pool.mtx.Unlock()

		if shouldCreate {
			pool.makeRequesterForHeight(nextHeight)
			time.Sleep(requestIntervalMS * time.Millisecond)
		} else {
			// Wait before checking again
			time.Sleep(10 * time.Millisecond)
			pool.removeTimedoutPeers()
		}
	}
}

// makeRequesterForHeight creates a requester for a specific height
func (pool *SlidingWindowPool) makeRequesterForHeight(height int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	// Double-check height is still valid
	if !pool.buffer.IsInWindow(height) {
		return
	}

	// Don't create if already exists or block already in buffer
	if _, exists := pool.requesters[height]; exists {
		return
	}
	if pool.buffer.HasBlock(height) {
		return
	}

	request := newBPRequester(pool.toBlockPoolLocked(), height, pool.tracer)
	pool.requesters[height] = request
	atomic.AddInt32(&pool.numPending, 1)

	pool.logger.Debug("Created requester",
		"height", height,
		"num_requesters", len(pool.requesters),
		"window", fmt.Sprintf("[%d, %d]", pool.buffer.BaseHeight(), pool.buffer.MaxHeight()))

	if err := request.Start(); err != nil {
		request.Logger.Error("Error starting requester", "err", err)
	}
}

// AddBlock adds a block to the buffer
func (pool *SlidingWindowPool) AddBlock(peerID p2p.ID, block *types.Block, extCommit *types.ExtendedCommit, blockSize int) error {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	height := block.Height

	// Try to add to buffer (enforces sliding window)
	err := pool.buffer.AddBlock(height, block, extCommit, peerID, blockSize)
	if err != nil {
		// Block outside window or below base - this is a protocol violation
		if height < pool.buffer.BaseHeight() {
			// Already processed - not an error (late arrival from slow peer)
			pool.logger.Debug("Received already processed block", "height", height, "peer", peerID)
			return nil
		}
		// Outside window - disconnect peer
		pool.sendError(fmt.Errorf("peer sent block outside window: %w", err), peerID)
		return err
	}

	// Block successfully added to buffer
	// Stop the requester for this height if it exists
	if requester, exists := pool.requesters[height]; exists {
		if requester.setBlock(block, extCommit, peerID) {
			atomic.AddInt32(&pool.numPending, -1)

			// Update peer stats
			if peer := pool.peers[peerID]; peer != nil {
				peer.decrPending(blockSize)
			}
		}
	}

	return nil
}

// PeekTwoBlocks returns the next two sequential blocks for validation
func (pool *SlidingWindowPool) PeekTwoBlocks() (first, second *types.Block, firstExtCommit *types.ExtendedCommit) {
	firstBuf, secondBuf := pool.buffer.PeekTwoNext()

	if firstBuf == nil || secondBuf == nil {
		return nil, nil, nil
	}

	return firstBuf.Block, secondBuf.Block, firstBuf.ExtCommit
}

// PopRequest removes the next block from the buffer and advances the window
func (pool *SlidingWindowPool) PopRequest() {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	height := pool.buffer.BaseHeight()

	// Stop and remove the requester
	if requester := pool.requesters[height]; requester != nil {
		if err := requester.Stop(); err != nil {
			pool.logger.Error("Error stopping requester", "err", err)
		}
		delete(pool.requesters, height)
	}

	// Pop from buffer (advances window)
	block := pool.buffer.PopNext()
	if block != nil {
		pool.logger.Debug("Popped block",
			"height", height,
			"new_base", pool.buffer.BaseHeight(),
			"requesters", len(pool.requesters))
	}
}

// RemovePeerAndRedoAllPeerRequests removes a peer and redoes their requests
func (pool *SlidingWindowPool) RemovePeerAndRedoAllPeerRequests(height int64) p2p.ID {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	var peerID p2p.ID

	// Try to get peer ID from requester first
	requester := pool.requesters[height]
	if requester != nil {
		peerID = requester.gotBlockFromPeerID()
	}

	// If no requester, try to get peer ID from buffer
	if peerID == "" && pool.buffer.HasBlock(height) {
		pool.buffer.mtx.RLock()
		if bufferedBlock := pool.buffer.blocks[height]; bufferedBlock != nil {
			peerID = bufferedBlock.PeerID
		}
		pool.buffer.mtx.RUnlock()
	}

	if peerID == "" {
		return ""
	}

	// Remove block from buffer if it came from this peer
	if pool.buffer.HasBlock(height) {
		pool.buffer.RemoveBlock(height)
	}

	// Redo all requests from this peer
	for _, req := range pool.requesters {
		if req.didRequestFrom(peerID) {
			req.redo(peerID)
		}
	}

	pool.removePeer(peerID)
	return peerID
}

// Height returns the current base height
func (pool *SlidingWindowPool) Height() int64 {
	return pool.buffer.BaseHeight()
}

func (pool *SlidingWindowPool) SetHeight(height int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	pool.buffer.Clear(height)
	// Clear all requesters since we're resetting to a new height
	for _, requester := range pool.requesters {
		requester.Stop()
	}
	pool.requesters = make(map[int64]*bpRequester)
	atomic.StoreInt32(&pool.numPending, 0)
}

// IsCaughtUp returns true if we're caught up with peers
func (pool *SlidingWindowPool) IsCaughtUp() bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if len(pool.peers) == 0 {
		return false
	}

	receivedBlockOrTimedOut := pool.buffer.BaseHeight() > 0 || time.Since(pool.startTime) > 5*time.Second
	ourChainIsLongestAmongPeers := pool.maxPeerHeight == 0 || pool.buffer.BaseHeight() >= (pool.maxPeerHeight-1)

	return receivedBlockOrTimedOut && ourChainIsLongestAmongPeers
}

// MaxPeerHeight returns the maximum height reported by peers
func (pool *SlidingWindowPool) MaxPeerHeight() int64 {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.maxPeerHeight
}

// GetStatus returns pool statistics
func (pool *SlidingWindowPool) GetStatus() (height int64, numPending int32, lenRequesters int) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.buffer.BaseHeight(), atomic.LoadInt32(&pool.numPending), len(pool.requesters)
}

// toBlockPool converts to legacy BlockPool interface for requester compatibility
// This is a temporary adapter while transitioning
func (pool *SlidingWindowPool) toBlockPool() *BlockPool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.toBlockPoolLocked()
}

// toBlockPoolLocked is the same as toBlockPool but assumes mutex is already held
func (pool *SlidingWindowPool) toBlockPoolLocked() *BlockPool {
	// Create a minimal BlockPool struct that satisfies bpRequester dependencies
	bp := &BlockPool{
		requestsCh:    pool.requestsCh,
		errorsCh:      pool.errorsCh,
		peers:         pool.peers,
		sortedPeers:   pool.sortedPeers,
		maxPeerHeight: pool.maxPeerHeight,
		height:        pool.buffer.BaseHeight(),
		numPending:    pool.numPending,
	}
	bp.Logger = pool.logger
	return bp
}

// Peer management methods (delegate to existing implementations)

func (pool *SlidingWindowPool) sendRequest(height int64, peerID p2p.ID) {
	// Note: We don't check IsRunning() here because the channel will be closed
	// when the pool service stops anyway, and this provides better error visibility
	pool.requestsCh <- BlockRequest{height, peerID}
}

func (pool *SlidingWindowPool) sendError(err error, peerID p2p.ID) {
	// Note: We don't check IsRunning() here for the same reason as sendRequest
	pool.errorsCh <- peerError{err, peerID}
}

func (pool *SlidingWindowPool) SetPeerRange(peerID p2p.ID, base int64, height int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	peer := pool.peers[peerID]
	if peer != nil {
		// Check for height regression (security issue)
		if base < peer.base || height < peer.height {
			pool.logger.Info("Peer reporting lower height/base than previously reported",
				"peer", peerID,
				"old_base", peer.base,
				"new_base", base,
				"old_height", peer.height,
				"new_height", height)
			pool.removePeer(peerID)
			pool.banPeer(peerID)
			return
		}

		peer.base = base
		peer.height = height
	} else {
		peer = newBPPeer(pool.toBlockPoolLocked(), peerID, base, height)
		peer.setLogger(pool.logger.With("peer", peerID))
		pool.peers[peerID] = peer
	}

	pool.updateMaxPeerHeight()
	pool.updateSortedPeers()
}

func (pool *SlidingWindowPool) RemovePeer(peerID p2p.ID) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	pool.removePeer(peerID)
}

func (pool *SlidingWindowPool) RedoRequestFrom(height int64, peerID p2p.ID) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if requester, ok := pool.requesters[height]; ok {
		if requester.didRequestFrom(peerID) {
			requester.redo(peerID)
		}
	}
}

func (pool *SlidingWindowPool) SetLogger(l log.Logger) {
	pool.logger = l
	pool.BaseService.Logger = l
}

func (pool *SlidingWindowPool) removePeer(peerID p2p.ID) {
	delete(pool.peers, peerID)
	pool.updateMaxPeerHeight()
	pool.updateSortedPeers()
}

func (pool *SlidingWindowPool) banPeer(peerID p2p.ID) {
	pool.bannedPeers[peerID] = time.Now()
}

func (pool *SlidingWindowPool) isPeerBanned(peerID p2p.ID) bool {
	if bannedAt, exists := pool.bannedPeers[peerID]; exists {
		if time.Since(bannedAt) < pool.banDuration {
			return true
		}
		delete(pool.bannedPeers, peerID)
	}
	return false
}

func (pool *SlidingWindowPool) updateMaxPeerHeight() {
	pool.maxPeerHeight = 0
	for _, peer := range pool.peers {
		if peer.height > pool.maxPeerHeight {
			pool.maxPeerHeight = peer.height
		}
	}
}

func (pool *SlidingWindowPool) updateSortedPeers() {
	pool.sortedPeers = make([]*bpPeer, 0, len(pool.peers))
	for _, peer := range pool.peers {
		pool.sortedPeers = append(pool.sortedPeers, peer)
	}
	pool.sortPeers()
}

func (pool *SlidingWindowPool) sortPeers() {
	// Sort peers by receive rate (highest first)
	// Same logic as pool.go
	sort.Slice(pool.sortedPeers, func(i, j int) bool {
		return pool.sortedPeers[i].curRate > pool.sortedPeers[j].curRate
	})
}

func (pool *SlidingWindowPool) removeTimedoutPeers() {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	for _, peer := range pool.peers {
		if !peer.didTimeout && peer.numPending > 0 {
			curRate := peer.recvMonitor.Status().CurRate
			if curRate != 0 && curRate < minRecvRate {
				err := fmt.Errorf("peer is not sending us data fast enough")
				pool.sendError(err, peer.id)
				pool.logger.Error("SendTimeout", "peer", peer.id,
					"reason", err,
					"curRate", fmt.Sprintf("%d KB/s", curRate/1024),
					"minRate", fmt.Sprintf("%d KB/s", minRecvRate/1024))
				peer.didTimeout = true
			}
		}
	}
}

func (pool *SlidingWindowPool) pickIncrAvailablePeer(height int64, excludePeerID p2p.ID) *bpPeer {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	pool.sortPeers()

	for _, peer := range pool.sortedPeers {
		if peer.id == excludePeerID {
			continue
		}
		if peer.didTimeout {
			continue
		}
		if peer.numPending >= maxPendingRequestsPerPeer {
			continue
		}
		if height < peer.base || height > peer.height {
			continue
		}
		peer.incrPending()
		return peer
	}
	return nil
}
