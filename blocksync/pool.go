package blocksync

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync/atomic"
	"time"

	flow "github.com/cometbft/cometbft/libs/flowrate"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

/*
eg, L = latency = 0.1s
	P = num peers = 10
	FN = num full nodes
	BS = 1kB block size
	CB = 1 Mbit/s = 128 kB/s
	CB/P = 12.8 kB
	B/S = CB/P/BS = 12.8 blocks/s

	12.8 * 0.1 = 1.28 blocks on conn
*/

const (
	requestIntervalMS = 2

	// minBlockSizeBytes is the minimum block size (1 KB) used for dynamic retry timer calculation.
	minBlockSizeBytes = 1024

	// maxBlockSizeBytes is the maximum block size (20 MB) used for dynamic retry timer calculation.
	maxBlockSizeBytes = 20 * 1024 * 1024

	// minRetrySeconds is the minimum retry timeout for small blocks.
	minRetrySeconds = 5

	// maxRetrySeconds is the maximum retry timeout for large blocks.
	maxRetrySeconds = 60

	// minReqLimit is the maximum concurrent requests per peer for small blocks.
	// Small blocks can have more concurrent requests.
	minReqLimit = 2

	// maxReqLimit is the maximum concurrent requests per peer for large blocks.
	// Large blocks should have fewer to avoid bandwidth saturation.
	maxReqLimit = 10

	// blockSizeBufferCapacity is the number of block sizes to track for averaging.
	blockSizeBufferCapacity = 70

	// Minimum recv rate to ensure we're receiving blocks from a peer fast
	// enough. If a peer is not sending us data at at least that rate, we
	// consider them to have timedout and we disconnect.
	//
	// Based on the experiments with [Osmosis](https://osmosis.zone/), the
	// minimum rate could be as high as 500 KB/s. However, we're setting it to
	// 128 KB/s for now to be conservative.
	minRecvRate = 128 * 1024 // 128 KB/s

	// peerConnWait is the time that must have elapsed since the pool routine
	// was created before we start making requests. This is to give the peer
	// routine time to connect to peers.
	peerConnWait = 3 * time.Second

	// defaultMaxRequesters is the default maximum number of concurrent block requesters.
	defaultMaxRequesters = 40
)

var peerTimeout = 120 * time.Second // not const so we can override with tests

/*
	Peers self report their heights when we join the block pool.
	Starting from our latest pool.height, we request blocks
	in sequence from peers that reported higher heights than ours.
	Every so often we ask peers what height they're on so we can keep going.

	Requests are continuously made for blocks of higher heights until
	the limit is reached. If most of the requests have no available peers, and we
	are not at peer limits, we can probably switch to consensus reactor
*/

// BlockPool keeps track of the block sync peers, block requests and block responses.
type BlockPool struct {
	service.BaseService
	startTime   time.Time
	startHeight int64

	mtx cmtsync.Mutex
	// block requests
	requesters map[int64]*bpRequester
	height     int64 // the lowest key in requesters.
	// peers
	peers           map[p2p.ID]*bpPeer
	bannedPeers     map[p2p.ID]time.Time
	sortedPeers     []*bpPeer // sorted by curRate, highest first
	maxPeerHeight   int64     // the biggest reported height
	reqLimit        int
	retryTimeout    time.Duration
	blockSizeBuffer *blockStats

	// atomic
	numPending int32 // number of requests pending assignment or block response

	requestsCh chan<- BlockRequest
	errorsCh   chan<- peerError

	traceClient trace.Tracer
}

// NewBlockPool returns a new BlockPool with the height equal to start. Block
// requests and errors will be sent to requestsCh and errorsCh accordingly.
func NewBlockPool(start int64, requestsCh chan<- BlockRequest, errorsCh chan<- peerError) *BlockPool {
	return newBlockPoolWithTracer(start, requestsCh, errorsCh, trace.NoOpTracer())
}

// newBlockPoolWithTracer returns a new BlockPool with custom tracer
func newBlockPoolWithTracer(start int64, requestsCh chan<- BlockRequest, errorsCh chan<- peerError, traceClient trace.Tracer) *BlockPool {
	bp := &BlockPool{
		peers:       make(map[p2p.ID]*bpPeer),
		bannedPeers: make(map[p2p.ID]time.Time),
		requesters:  make(map[int64]*bpRequester),
		height:      start,
		startHeight: start,
		numPending:  0,

		requestsCh:      requestsCh,
		errorsCh:        errorsCh,
		traceClient:     traceClient,
		blockSizeBuffer: newBlockStats(blockSizeBufferCapacity),
	}
	// Initialize with default parameters
	bp.BaseService = *service.NewBaseService(nil, "BlockPool", bp)
	bp.recalculateParams()
	return bp
}

// OnStart implements service.Service by spawning requesters routine and recording
// pool's start time.
func (pool *BlockPool) OnStart() error {
	pool.startTime = time.Now()
	go pool.makeRequestersRoutine()
	return nil
}

// recalculateParams updates all parameters based on current conditions
// numPeers is passed in since it's external state from BlockPool
func (pool *BlockPool) recalculateParams() {
	blockSize := pool.blockSizeBuffer.GetMax()
	if pool.blockSizeBuffer.Size() == 0 {
		pool.reqLimit = maxReqLimit / 2
		pool.retryTimeout = time.Duration((maxRetrySeconds / 2) * float64(time.Second))
	} else {
		pool.reqLimit = interpolate(blockSize, minBlockSizeBytes, maxBlockSizeBytes, maxReqLimit, minReqLimit)
		pool.retryTimeout = time.Second * time.Duration(
			interpolate(blockSize, minBlockSizeBytes, maxBlockSizeBytes, minRetrySeconds, maxRetrySeconds))
	}
}

// spawns requesters as needed
func (pool *BlockPool) makeRequestersRoutine() {
	for {
		if !pool.IsRunning() {
			return
		}

		// Check if we are within peerConnWait seconds of start time
		// This gives us some time to connect to peers before starting a wave of requests
		if time.Since(pool.startTime) < peerConnWait {
			// Calculate the duration to sleep until peerConnWait seconds have passed since pool.startTime
			sleepDuration := peerConnWait - time.Since(pool.startTime)
			time.Sleep(sleepDuration)
		}

		pool.mtx.Lock()

		var (
			maxRequestersCreated = len(pool.requesters) >= defaultMaxRequesters

			nextHeight           = pool.height + int64(len(pool.requesters))
			maxPeerHeightReached = nextHeight > pool.maxPeerHeight
		)
		pool.mtx.Unlock()

		switch {
		case maxRequestersCreated: // If we have enough requesters, wait for them to finish.
			time.Sleep(requestIntervalMS * time.Millisecond)
			pool.removeTimedoutPeers()
		case maxPeerHeightReached: // If we're caught up, wait for a bit so reactor could finish or a higher height is reported.
			time.Sleep(requestIntervalMS * time.Millisecond)
		default:
			// request for more blocks.
			pool.makeNextRequester(nextHeight)
			// Sleep for a bit to make the requests more ordered.
			time.Sleep(requestIntervalMS * time.Millisecond)
		}
	}
}

func (pool *BlockPool) removeTimedoutPeers() {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	for _, peer := range pool.peers {
		if !peer.didTimeout && peer.numPending > 0 {
			curRate := peer.recvMonitor.Status().CurRate
			// curRate can be 0 on start
			if curRate != 0 && curRate < minRecvRate {
				err := errors.New("peer is not sending us data fast enough")
				pool.sendError(err, peer.id)
				pool.Logger.Error("SendTimeout", "peer", peer.id,
					"reason", err,
					"curRate", fmt.Sprintf("%d KB/s", curRate/1024),
					"minRate", fmt.Sprintf("%d KB/s", minRecvRate/1024))
				peer.didTimeout = true
			}

			peer.curRate = curRate
		}

		if peer.didTimeout {
			pool.removePeer(peer.id)
		}
	}

	for peerID := range pool.bannedPeers {
		if !pool.isPeerBanned(peerID) {
			delete(pool.bannedPeers, peerID)
		}
	}

	pool.sortPeers()
}

// GetStatus returns pool's height, numPending requests and the number of
// requesters.
func (pool *BlockPool) GetStatus() (height int64, numPending int32, lenRequesters int) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	return pool.height, atomic.LoadInt32(&pool.numPending), len(pool.requesters)
}

// IsCaughtUp returns true if this node is caught up, false - otherwise.
// TODO: relax conditions, prevent abuse.
func (pool *BlockPool) IsCaughtUp() bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	// Need at least 1 peer to be considered caught up.
	if len(pool.peers) == 0 {
		pool.Logger.Trace("Blockpool has no peers")
		return false
	}

	// Some conditions to determine if we're caught up.
	// Ensures we've either received a block or waited some amount of time,
	// and that we're synced to the highest known height.
	// Note we use maxPeerHeight - 1 because to sync block H requires block H+1
	// to verify the LastCommit.
	receivedBlockOrTimedOut := pool.height > 0 || time.Since(pool.startTime) > 5*time.Second
	ourChainIsLongestAmongPeers := pool.maxPeerHeight == 0 || pool.height >= (pool.maxPeerHeight-1)
	isCaughtUp := receivedBlockOrTimedOut && ourChainIsLongestAmongPeers
	return isCaughtUp
}

// PeekTwoBlocks returns blocks at pool.height and pool.height+1. We need to
// see the second block's Commit to validate the first block. So we peek two
// blocks at a time. We return an extended commit, containing vote extensions
// and their associated signatures, as this is critical to consensus in ABCI++
// as we switch from block sync to consensus mode.
//
// The caller will verify the commit.
func (pool *BlockPool) PeekTwoBlocks() (first, second *types.Block, firstExtCommit *types.ExtendedCommit) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if r := pool.requesters[pool.height]; r != nil {
		first = r.getBlock()
		firstExtCommit = r.getExtendedCommit()
	}
	if r := pool.requesters[pool.height+1]; r != nil {
		second = r.getBlock()
	}
	return
}

// PopRequest removes the requester at pool.height and increments pool.height.
func (pool *BlockPool) PopRequest() {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	r := pool.requesters[pool.height]
	if r == nil {
		panic(fmt.Sprintf("Expected requester to pop, got nothing at height %v", pool.height))
	}

	if err := r.Stop(); err != nil {
		pool.Logger.Error("Error stopping requester", "err", err)
	}
	delete(pool.requesters, pool.height)
	pool.height++
}

// RemovePeerAndRedoAllPeerRequests retries the request at the given height and
// all the requests made to the same peer. The peer is removed from the pool.
// Returns the ID of the removed peer.
func (pool *BlockPool) RemovePeerAndRedoAllPeerRequests(height int64) p2p.ID {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	request := pool.requesters[height]
	if request == nil {
		// this shouldn't happen, but to be on the safe side, let's ignore
		return ""
	}
	peerID := request.gotBlockFromPeerID()
	// RemovePeer will redo all requesters associated with this peer.
	pool.removePeer(peerID)
	pool.banPeer(peerID)
	return peerID
}

// RedoRequestFrom retries the request at the given height. It does not remove the
// peer.
func (pool *BlockPool) RedoRequestFrom(height int64, peerID p2p.ID) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if requester, ok := pool.requesters[height]; ok { // If we requested this block
		if requester.didRequestFrom(peerID) { // From this specific peer
			requester.redo(peerID)
		}
	}
}

// AddBlock validates that the block comes from the peer it was expected from
// and calls the requester to store it.
//
// This requires an extended commit at the same height as the supplied block -
// the block contains the last commit, but we need the latest commit in case we
// need to switch over from block sync to consensus at this height. If the
// height of the extended commit and the height of the block do not match, we
// do not add the block and return an error.
// TODO: ensure that blocks come in order for each peer.
func (pool *BlockPool) AddBlock(peerID p2p.ID, block *types.Block, extCommit *types.ExtendedCommit, blockSize int) error {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	if extCommit != nil && block.Height != extCommit.Height {
		err := fmt.Errorf("block height %d != extCommit height %d", block.Height, extCommit.Height)
		// Peer sent us an invalid block => remove it.
		pool.sendError(err, peerID)
		return err
	}

	requester := pool.requesters[block.Height]
	if requester == nil {
		// If the peer sent us a block we clearly didn't request, we disconnect.
		if block.Height > pool.height || block.Height < pool.startHeight {
			err := fmt.Errorf("peer sent us block #%d we didn't expect (current height: %d, start height: %d)",
				block.Height, pool.height, pool.startHeight)
			pool.sendError(err, peerID)
			return err
		}

		return fmt.Errorf("got an already committed block #%d (possibly from the slow peer %s)", block.Height, peerID)
	}

	blockSet, err := requester.setBlock(block, extCommit, peerID)
	if err != nil {
		// Check if this peer was recently banned. If so, this is likely a race condition
		// where the block arrived after the peer was banned and reset from the requester.
		// This is not an error, just a timing issue.
		if pool.isPeerBanned(peerID) {
			pool.Logger.Trace("Ignoring block from recently banned peer", "peer", peerID, "height", block.Height)
			return nil
		}
		err := fmt.Errorf("requested block #%d from %v, not %s, %w", block.Height, requester.peerID, peerID, err)
		pool.sendError(err, peerID)
		return err
	}

	atomic.AddInt32(&pool.numPending, -1)
	peer := pool.peers[peerID]
	if peer != nil {
		peer.decrPending(blockSize)
	}

	if blockSet {
		pool.blockSizeBuffer.Add(blockSize)
		pool.recalculateParams()
		pool.Logger.Trace("Block added, current maxPending",
			"height", block.Height,
			"peer", peerID,
			"blockSize", fmt.Sprintf("%.2f KB", float64(blockSize)/1024),
			"blockSize", fmt.Sprintf("%.2f KB", float64(blockSize)/1024),
			"maxPending", pool.reqLimit,
			"retryTimeout", pool.retryTimeout.Seconds(),
			"numRequesters", len(pool.requesters),
			"numPeers", len(pool.peers))
	}

	return nil
}

// Height returns the pool's height.
func (pool *BlockPool) Height() int64 {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.height
}

// MaxPeerHeight returns the highest reported height.
func (pool *BlockPool) MaxPeerHeight() int64 {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.maxPeerHeight
}

// SetPeerRange sets the peer's alleged blockchain base and height.
func (pool *BlockPool) SetPeerRange(peerID p2p.ID, base int64, height int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	peer := pool.peers[peerID]
	if peer != nil {
		if base < peer.base || height < peer.height {
			pool.Logger.Info("Peer is reporting height/base that is lower than what it previously reported",
				"peer", peerID,
				"height", height, "base", base,
				"prevHeight", peer.height, "prevBase", peer.base)
			// RemovePeer will redo all requesters associated with this peer.
			pool.removePeer(peerID)
			pool.banPeer(peerID)
			return
		}
		peer.base = base
		peer.height = height
	} else {
		if pool.isPeerBanned(peerID) {
			pool.Logger.Trace("Ignoring banned peer", "peer", peerID)
			return
		}
		peer = newBPPeer(pool, peerID, base, height)
		peer.setLogger(pool.Logger.With("peer", peerID))
		pool.peers[peerID] = peer
		// no need to sort because curRate is 0 at start.
		// just add to the beginning so it's picked first by pickIncrAvailablePeer.
		pool.sortedPeers = append([]*bpPeer{peer}, pool.sortedPeers...)
	}

	if height > pool.maxPeerHeight {
		pool.maxPeerHeight = height
	}
}

// RemovePeer removes the peer with peerID from the pool. If there's no peer
// with peerID, function is a no-op.
func (pool *BlockPool) RemovePeer(peerID p2p.ID) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	pool.removePeer(peerID)
}

// CONTRACT: pool.mtx must be locked.
func (pool *BlockPool) removePeer(peerID p2p.ID) {
	for _, requester := range pool.requesters {
		if requester.didRequestFrom(peerID) {
			requester.redo(peerID)
		}
	}

	peer, ok := pool.peers[peerID]
	if ok {
		if peer.timeout != nil {
			peer.timeout.Stop()
		}

		delete(pool.peers, peerID)
		for i, p := range pool.sortedPeers {
			if p.id == peerID {
				pool.sortedPeers = append(pool.sortedPeers[:i], pool.sortedPeers[i+1:]...)
				break
			}
		}

		// Find a new peer with the biggest height and update maxPeerHeight if the
		// peer's height was the biggest.
		if peer.height == pool.maxPeerHeight {
			pool.updateMaxPeerHeight()
		}
	}
}

// If no peers are left, maxPeerHeight is set to 0.
func (pool *BlockPool) updateMaxPeerHeight() {
	var max int64
	for _, peer := range pool.peers {
		if peer.height > max {
			max = peer.height
		}
	}
	pool.maxPeerHeight = max
}

// IsPeerBanned returns true if the peer is banned.
func (pool *BlockPool) IsPeerBanned(peerID p2p.ID) bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.isPeerBanned(peerID)
}

// CONTRACT: pool.mtx must be locked.
func (pool *BlockPool) isPeerBanned(peerID p2p.ID) bool {
	// Todo: replace with cmttime.Since in future versions
	return time.Since(pool.bannedPeers[peerID]) < time.Second*60
}

// CONTRACT: pool.mtx must be locked.
func (pool *BlockPool) banPeer(peerID p2p.ID) {
	pool.Logger.Debug("Banning peer", peerID)
	pool.bannedPeers[peerID] = cmttime.Now()
}

// getRetryTimeout returns the current retry timeout (thread-safe)
func (pool *BlockPool) getRetryTimeout() time.Duration {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	return pool.retryTimeout
}

// Pick an available peer with the given height available.
// If no peers are available, returns nil.
// prevPeerID is a peer to avoid if possible (e.g., peer used for previous height).
// This promotes peer diversity by alternating between peers across consecutive blocks.
func (pool *BlockPool) pickIncrAvailablePeer(height int64, ignorePeerID, prevPeerID p2p.ID) *bpPeer {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	maxPending := pool.reqLimit
	var fallbackPeer *bpPeer // Peer that matches all criteria except prevPeerID

	for _, peer := range pool.sortedPeers {
		if peer.id == ignorePeerID {
			continue
		}
		if peer.didTimeout {
			pool.removePeer(peer.id)
			continue
		}
		if peer.numPending >= int32(maxPending) {
			continue
		}
		if height < peer.base || height > peer.height {
			continue
		}

		// If this is the prevPeerID, save as fallback but continue looking
		// the idea is that we want to add diversity to peers, so we will try to alternate peers when loading
		// this works a bit better with less amount of peers and large blocks
		if peer.id == prevPeerID {
			if fallbackPeer == nil {
				fallbackPeer = peer
			}
			continue
		}
		peer.incrPending()
		return peer
	}

	if fallbackPeer != nil {
		fallbackPeer.incrPending()
		return fallbackPeer
	}

	return nil
}

// Sort peers by curRate, highest first.
//
// CONTRACT: pool.mtx must be locked.
func (pool *BlockPool) sortPeers() {
	sort.Slice(pool.sortedPeers, func(i, j int) bool {
		return pool.sortedPeers[i].curRate > pool.sortedPeers[j].curRate
	})
}

func (pool *BlockPool) makeNextRequester(nextHeight int64) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	request := newBPRequester(pool, nextHeight)

	pool.requesters[nextHeight] = request
	atomic.AddInt32(&pool.numPending, 1)

	if err := request.Start(); err != nil {
		request.Logger.Error("Error starting request", "err", err)
	}
}

func (pool *BlockPool) sendRequest(height int64, peerID p2p.ID) {
	if !pool.IsRunning() {
		return
	}
	pool.requestsCh <- BlockRequest{height, peerID}
}

func (pool *BlockPool) sendError(err error, peerID p2p.ID) {
	if !pool.IsRunning() {
		return
	}
	pool.errorsCh <- peerError{err, peerID}
}

// for debugging purposes
//
//nolint:unused
func (pool *BlockPool) debug() string {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	str := ""
	nextHeight := pool.height + int64(len(pool.requesters))
	for h := pool.height; h < nextHeight; h++ {
		if pool.requesters[h] == nil {
			str += fmt.Sprintf("H(%v):X ", h)
		} else {
			str += fmt.Sprintf("H(%v):", h)
			str += fmt.Sprintf("B?(%v) ", pool.requesters[h].block != nil)
			str += fmt.Sprintf("C?(%v) ", pool.requesters[h].extCommit != nil)
		}
	}
	return str
}

//-------------------------------------

type bpPeer struct {
	didTimeout  bool
	curRate     int64
	numPending  int32
	height      int64
	base        int64
	pool        *BlockPool
	id          p2p.ID
	recvMonitor *flow.Monitor

	timeout *time.Timer

	logger log.Logger
}

func newBPPeer(pool *BlockPool, peerID p2p.ID, base int64, height int64) *bpPeer {
	peer := &bpPeer{
		pool:       pool,
		id:         peerID,
		base:       base,
		height:     height,
		numPending: 0,
		logger:     log.NewNopLogger(),
	}
	return peer
}

func (peer *bpPeer) setLogger(l log.Logger) {
	peer.logger = l
}

func (peer *bpPeer) resetMonitor() {
	peer.recvMonitor = flow.New(time.Second, time.Second*40)
	initialValue := float64(minRecvRate) * math.E
	peer.recvMonitor.SetREMA(initialValue)
}

func (peer *bpPeer) resetTimeout() {
	if peer.timeout == nil {
		peer.timeout = time.AfterFunc(peerTimeout, peer.onTimeout)
	} else {
		peer.timeout.Reset(peerTimeout)
	}
}

func (peer *bpPeer) incrPending() {
	if peer.numPending == 0 {
		peer.resetMonitor()
		peer.resetTimeout()
	}
	peer.numPending++
}

func (peer *bpPeer) decrPending(recvSize int) {
	peer.numPending--
	if peer.numPending == 0 {
		peer.timeout.Stop()
	} else {
		peer.recvMonitor.Update(recvSize)
		peer.resetTimeout()
	}
}

func (peer *bpPeer) onTimeout() {
	peer.pool.mtx.Lock()
	defer peer.pool.mtx.Unlock()

	err := errors.New("peer did not send us anything")
	peer.pool.sendError(err, peer.id)
	peer.logger.Error("SendTimeout", "reason", err, "timeout", peerTimeout)
	peer.didTimeout = true
}

//-------------------------------------

// bpRequester requests a block from a peer.
//
// If the height is within minBlocksForSingleRequest blocks of the pool's
// height, it will send an additional request to another peer. This is to avoid
// a situation where blocksync is stuck because of a single slow peer. Note
// that it's okay to send a single request when the requested height is far
// from the pool's height. If the peer is slow, it will timeout and be replaced
// with another peer.
type bpRequester struct {
	service.BaseService

	pool               *BlockPool
	height             int64
	gotBlockCh         chan struct{}
	redoCh             chan p2p.ID         // redo may got multiple messages, add peerId to identify repeat
	peerID             p2p.ID              // peerID is the peer currently being requested from ("" if no active request)
	requestedFromPeers map[p2p.ID]struct{} // requestedFromPeers are all peers used by this requester

	mtx          cmtsync.Mutex
	gotBlockFrom p2p.ID
	block        *types.Block
	extCommit    *types.ExtendedCommit
}

func newBPRequester(pool *BlockPool, height int64) *bpRequester {
	bpr := &bpRequester{
		pool:               pool,
		height:             height,
		gotBlockCh:         make(chan struct{}, 1),
		redoCh:             make(chan p2p.ID, 1),
		peerID:             "",
		requestedFromPeers: make(map[p2p.ID]struct{}),

		block: nil,
	}
	bpr.BaseService = *service.NewBaseService(nil, "bpRequester", bpr)
	return bpr
}

func (bpr *bpRequester) OnStart() error {
	go bpr.requestRoutine()
	return nil
}

// Returns true if the peer(s) match and block doesn't already exist.
func (bpr *bpRequester) setBlock(block *types.Block, extCommit *types.ExtendedCommit, peerID p2p.ID) (bool, error) {
	bpr.mtx.Lock()
	_, isRequested := bpr.requestedFromPeers[peerID]
	if !isRequested {
		bpr.mtx.Unlock()
		return false, fmt.Errorf("block not requested from peer")
	}
	if bpr.peerID == peerID {
		bpr.peerID = ""
	}
	if bpr.block != nil {
		bpr.mtx.Unlock()
		return false, nil // already got a block
	}

	bpr.block = block
	bpr.extCommit = extCommit
	bpr.gotBlockFrom = peerID
	bpr.mtx.Unlock()

	select {
	case bpr.gotBlockCh <- struct{}{}:
	default:
	}
	return true, nil
}

func (bpr *bpRequester) getBlock() *types.Block {
	bpr.mtx.Lock()
	defer bpr.mtx.Unlock()
	return bpr.block
}

func (bpr *bpRequester) getExtendedCommit() *types.ExtendedCommit {
	bpr.mtx.Lock()
	defer bpr.mtx.Unlock()
	return bpr.extCommit
}

// Returns true if we've requested a block from the given peer.
func (bpr *bpRequester) didRequestFrom(peerID p2p.ID) bool {
	bpr.mtx.Lock()
	defer bpr.mtx.Unlock()
	_, ok := bpr.requestedFromPeers[peerID]
	return ok
}

// Returns the ID of the peer who sent us the block.
func (bpr *bpRequester) gotBlockFromPeerID() p2p.ID {
	bpr.mtx.Lock()
	defer bpr.mtx.Unlock()
	return bpr.gotBlockFrom
}

func (bpr *bpRequester) resetAll() p2p.ID {
	bpr.mtx.Lock()
	defer bpr.mtx.Unlock()
	cur := bpr.peerID
	if bpr.peerID != "" {
		bpr.tryRemoveBlock(bpr.peerID)
		bpr.peerID = ""
	}
	return cur
}

func (bpr *bpRequester) tryRemoveBlock(peerID p2p.ID) bool {
	if bpr.gotBlockFrom == peerID {
		bpr.block = nil
		bpr.extCommit = nil
		bpr.gotBlockFrom = ""
		atomic.AddInt32(&bpr.pool.numPending, 1)
		return true
	}
	return false
}

// Removes the block (IF we got it from the given peer) and resets the peer.
func (bpr *bpRequester) reset(peerID p2p.ID) (removedBlock bool) {
	bpr.mtx.Lock()
	defer bpr.mtx.Unlock()

	removedBlock = bpr.tryRemoveBlock(peerID)
	if bpr.peerID == peerID {
		bpr.peerID = ""
	}
	return removedBlock
}

// Tells bpRequester to pick another peer and try again.
// NOTE: Nonblocking, and does nothing if another redo
// was already requested.
func (bpr *bpRequester) redo(peerID p2p.ID) {
	select {
	case bpr.redoCh <- peerID:
	default:
	}
}

// pickPeerAndSendRequest picks a peer and sends a block request.
// Only sends a request if there is no active request already.
func (bpr *bpRequester) pickPeerAndSendRequest(ignorePeerID p2p.ID) {
	// Check if there's already an active request - if so, don't make another one
	bpr.mtx.Lock()
	if bpr.peerID != "" {
		bpr.mtx.Unlock()
		return
	}
	bpr.mtx.Unlock()

	// Try to get the peer used for the previous height to promote diversity
	var prevPeerID p2p.ID
	bpr.pool.mtx.Lock()
	if prevRequester := bpr.pool.requesters[bpr.height-1]; prevRequester != nil {
		prevPeerID = prevRequester.gotBlockFromPeerID()
	}
	bpr.pool.mtx.Unlock()

	var peer *bpPeer
PICK_PEER_LOOP:
	for {
		if !bpr.IsRunning() || !bpr.pool.IsRunning() {
			return
		}
		peer = bpr.pool.pickIncrAvailablePeer(bpr.height, ignorePeerID, prevPeerID)
		if peer == nil {
			bpr.Logger.Trace("No peers currently available; will retry shortly", "height", bpr.height)
			time.Sleep(requestIntervalMS * time.Millisecond)
			continue PICK_PEER_LOOP
		}
		break PICK_PEER_LOOP
	}
	bpr.mtx.Lock()
	bpr.peerID = peer.id
	bpr.requestedFromPeers[peer.id] = struct{}{}
	bpr.mtx.Unlock()

	schema.WriteBlocksyncBlockRequested(bpr.pool.traceClient, bpr.height, string(peer.id))
	bpr.pool.sendRequest(bpr.height, peer.id)
}

// Responsible for making more requests as necessary
// Returns only when a block is found (e.g. AddBlock() is called)
func (bpr *bpRequester) requestRoutine() {
	gotBlock := false
	var ignorePeerID p2p.ID
OUTER_LOOP:
	for {
		bpr.pickPeerAndSendRequest(ignorePeerID)
		retryTimeout := bpr.pool.getRetryTimeout()
		retryTimer := time.NewTimer(retryTimeout)
		defer retryTimer.Stop()

		for {
			select {
			case <-bpr.pool.Quit():
				if err := bpr.Stop(); err != nil {
					bpr.Logger.Error("Error stopped requester", "err", err)
				}
				return
			case <-bpr.Quit():
				return
			case <-retryTimer.C:
				if !gotBlock {
					ignorePeerID = bpr.resetAll()
					continue OUTER_LOOP
				}
			case peerID := <-bpr.redoCh:
				removedBlock := bpr.reset(peerID)
				if removedBlock {
					gotBlock = false
				}
				// If peers returned NoBlockResponse or bad block, reschedule requests.
				bpr.mtx.Lock()
				peerIDEmpty := bpr.peerID == ""
				bpr.mtx.Unlock()
				if peerIDEmpty {
					retryTimer.Stop()
					ignorePeerID = peerID
					continue OUTER_LOOP
				}
			case <-bpr.gotBlockCh:
				gotBlock = true
				// We got a block!
				// Continue the for-loop and wait til Quit.
			}
		}
	}
}

// BlockRequest stores a block request identified by the block Height and the PeerID responsible for
// delivering the block
type BlockRequest struct {
	Height int64
	PeerID p2p.ID
}

// interpolate performs an inverse-linear interpolation between two integer
// bounds based on a floating-point input within a numeric range.
//
// Arguments:
//
//	value:      The input value to interpolate over.
//	minValue:   The minimum value of the input range.
//	maxValue:   The maximum value of the input range.
//	maxOut:     The output when value <= minValue.
//	minOut:     The output when value >= maxValue.
//
// Behavior:
//   - Clamps the input into [minValue, maxValue].
//   - Maps the input inversely from maxOut → minOut.
//   - Ensures the output does not fall below minOut.
func interpolate(value, minValue, maxValue int, maxOut, minOut int) int {
	// Clamp to lower bound
	if value <= minValue {
		return maxOut
	}
	// Clamp to upper bound
	if value >= maxValue {
		return minOut
	}

	// Normalize into [0, 1]
	normalized := float64(value-minValue) / float64(maxValue-minValue)

	// Inverse linear interpolation from maxOut → minOut
	raw := maxOut - int(float64(maxOut-minOut)*normalized)

	// Floor to minimum
	if raw < minOut {
		raw = minOut
	}

	return raw
}
