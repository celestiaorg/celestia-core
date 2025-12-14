package headersync

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	hsproto "github.com/cometbft/cometbft/proto/tendermint/headersync"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

const (
	// HeaderSyncChannel is the channel ID for header sync messages.
	HeaderSyncChannel = byte(0x69)

	// ReactorIncomingMessageQueueSize is the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 100

	// maxPeerGetHeadersPerSecond is the maximum number of GetHeaders requests
	// a peer can make per second before being rate-limited.
	maxPeerGetHeadersPerSecond = 5

	// peerRequestWindowSeconds is the time window for rate limiting.
	peerRequestWindowSeconds = 1

	// timeoutCheckInterval is how often to check for timed out requests.
	timeoutCheckInterval = time.Second * 2
)

// peerRequestTracker tracks request timestamps for rate limiting.
type peerRequestTracker struct {
	timestamps []time.Time
}

// Reactor handles header synchronization.
// It uses an event-driven architecture: headers are processed immediately
// when they arrive, not on a timer.
type Reactor struct {
	p2p.BaseReactor

	pool       *HeaderPool
	stateStore sm.Store
	blockStore *store.BlockStore
	chainID    string

	// currentValidators is the validator set used to verify incoming headers.
	// Updated when NextValidatorsHash changes (indicating a validator set change).
	currentValidators *types.ValidatorSet

	// Last verified header for chain linkage verification.
	lastHeader *types.Header

	// Subscriber management.
	subscribersMtx sync.RWMutex
	subscribers    []chan<- *VerifiedHeader

	// Rate limiting for incoming GetHeaders requests.
	peerRequestsMtx sync.Mutex
	peerRequests    map[p2p.ID]*peerRequestTracker

	// Metrics tracking.
	metrics             *Metrics
	headersSynced       int64
	lastHundredTime     time.Time
	lastBroadcastHeight atomic.Int64
}

// ReactorOption defines a function argument for Reactor.
type ReactorOption func(*Reactor)

// NewReactor returns a new header sync reactor.
// initialHeight is the chain's initial height (from genesis), used when the
// blockstore is empty to avoid requesting from height 1 on chains that start higher.
// validators is the current validator set for verifying headers - this is required
// and should be loaded from state (either from executed blocks or genesis).
func NewReactor(
	stateStore sm.Store,
	blockStore *store.BlockStore,
	chainID string,
	batchSize int64,
	initialHeight int64,
	validators *types.ValidatorSet,
	metrics *Metrics,
	options ...ReactorOption,
) *Reactor {
	if validators == nil {
		panic("headersync: validators are required")
	}
	if metrics == nil {
		metrics = NopMetrics()
	}

	// Start syncing from one past the current header height.
	// Use the maximum of HeaderHeight and Height for backwards compatibility
	// with existing blockstores that may not have HeaderHeight set.
	headerHeight := blockStore.HeaderHeight()
	blockHeight := blockStore.Height()
	baseHeight := blockStore.Base()
	startHeight := headerHeight + 1
	if blockHeight > headerHeight {
		// Blocks exist but HeaderHeight wasn't tracked - use block height
		startHeight = blockHeight + 1
	}
	// If blockstore is empty, use the chain's initial height instead of 1
	if startHeight == 1 && initialHeight > 1 {
		startHeight = initialHeight
	}
	fmt.Printf("headersync NEW REACTOR: headerHeight=%d blockHeight=%d baseHeight=%d initialHeight=%d startHeight=%d validators=%X\n",
		headerHeight, blockHeight, baseHeight, initialHeight, startHeight, validators.Hash())

	r := &Reactor{
		pool:              NewHeaderPool(startHeight, batchSize),
		stateStore:        stateStore,
		blockStore:        blockStore,
		chainID:           chainID,
		currentValidators: validators,
		subscribers:       make([]chan<- *VerifiedHeader, 0),
		peerRequests:      make(map[p2p.ID]*peerRequestTracker),
		metrics:           metrics,
	}
	r.BaseReactor = *p2p.NewBaseReactor("HeaderSync", r, p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize))

	for _, option := range options {
		option(r)
	}

	return r
}

// SetLogger implements service.Service by setting the logger on reactor and pool.
func (r *Reactor) SetLogger(l log.Logger) {
	r.Logger = l
	r.pool.Logger = l
}

// OnStart implements service.Service.
func (r *Reactor) OnStart() error {
	// Load last header for chain linkage verification.
	lastHeight := r.blockStore.HeaderHeight()
	if lastHeight > 0 {
		r.lastHeader = r.blockStore.LoadHeader(lastHeight)
	}

	r.lastHundredTime = time.Now()
	r.lastBroadcastHeight.Store(r.blockStore.HeaderHeight())
	r.metrics.Syncing.Set(1)

	// Start a goroutine to periodically check for timed out requests.
	go r.timeoutRoutine()

	return nil
}

// OnStop implements service.Service.
func (r *Reactor) OnStop() {
	r.metrics.Syncing.Set(0)
}

// GetChannels implements Reactor.
func (r *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:                  HeaderSyncChannel,
			Priority:            25, // Slightly lower than blocksync
			SendQueueCapacity:   1000,
			RecvBufferCapacity:  50 * 1024, // Headers are small
			RecvMessageCapacity: MaxMsgSize,
			MessageType:         &hsproto.Message{},
		},
	}
}

// AddPeer implements Reactor by sending our status to the peer.
// We send height-1 to be conservative and avoid triggering the DoS protection
// if we send another status update shortly after (e.g., if our height advances
// right after adding the peer). The peer will still know we have headers up to
// that height and can request them.
func (r *Reactor) AddPeer(peer p2p.Peer) {
	height := r.blockStore.HeaderHeight()
	// Send height-1 to be safe; if height is 0, send 0.
	if height > 0 {
		height--
	}
	r.Logger.Info("headersync add peer", "peer", peer.ID(), "advertised_height", height) // TODO(evan): remove
	peer.Send(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.StatusResponse{
			Base:   r.blockStore.Base(),
			Height: height,
		},
	})
}

// RemovePeer implements Reactor by removing the peer from the pool.
func (r *Reactor) RemovePeer(peer p2p.Peer, _ interface{}) {
	r.Logger.Info("headersync remove peer", "peer", peer.ID()) // TODO(evan): remove
	r.pool.RemovePeer(peer.ID())

	// Clean up rate limiting state for this peer.
	r.peerRequestsMtx.Lock()
	delete(r.peerRequests, peer.ID())
	r.peerRequestsMtx.Unlock()
}

// checkPeerRateLimit checks if a peer is within the rate limit for GetHeaders requests.
// Returns true if the request should be processed, false if rate-limited.
func (r *Reactor) checkPeerRateLimit(peerID p2p.ID) bool {
	r.peerRequestsMtx.Lock()
	defer r.peerRequestsMtx.Unlock()

	now := time.Now()
	windowStart := now.Add(-peerRequestWindowSeconds * time.Second)

	tracker := r.peerRequests[peerID]
	if tracker == nil {
		tracker = &peerRequestTracker{
			timestamps: make([]time.Time, 0, maxPeerGetHeadersPerSecond),
		}
		r.peerRequests[peerID] = tracker
	}

	// Remove timestamps outside the window.
	validIdx := 0
	for _, ts := range tracker.timestamps {
		if ts.After(windowStart) {
			tracker.timestamps[validIdx] = ts
			validIdx++
		}
	}
	tracker.timestamps = tracker.timestamps[:validIdx]

	// Check if under limit.
	if len(tracker.timestamps) >= maxPeerGetHeadersPerSecond {
		return false
	}

	// Record this request.
	tracker.timestamps = append(tracker.timestamps, now)
	return true
}

// Receive implements Reactor by handling incoming messages.
func (r *Reactor) Receive(e p2p.Envelope) {
	fmt.Println("receiving headersync")
	if err := ValidateMsg(e.Message); err != nil {
		r.Logger.Error("Peer sent invalid message", "peer", e.Src, "msg", e.Message, "err", err)
		r.Switch.StopPeerForError(e.Src, err, r.String())
		return
	}

	r.Logger.Debug("Received message", "peer", e.Src.ID(), "msg", reflect.TypeOf(e.Message))

	switch msg := e.Message.(type) {
	case *hsproto.StatusResponse:
		fmt.Println("received status response", msg.Height, msg.Base)
		// SetPeerRange returns false if the peer regresses (lower height/base).
		if !r.pool.SetPeerRange(e.Src.ID(), msg.Base, msg.Height) {
			r.Switch.StopPeerForError(e.Src, errors.New("status update with non-increasing height"), r.String())
			return
		}
		// Peer has new headers - try to make requests.
		r.tryMakeRequests()

	case *hsproto.GetHeaders:
		fmt.Println("got request for headers", msg.StartHeight, e.Src.ID())
		if !r.checkPeerRateLimit(e.Src.ID()) {
			r.Logger.Debug("Rate limiting GetHeaders request", "peer", e.Src.ID())
			return
		}
		r.respondGetHeaders(msg, e.Src)

	case *hsproto.HeadersResponse:
		fmt.Println("received header response", len(msg.Headers))
		r.handleHeaders(msg, e.Src)

	default:
		r.Logger.Error("Unknown message type", "msg", reflect.TypeOf(msg))
	}
}

// respondGetHeaders responds to a GetHeaders request.
func (r *Reactor) respondGetHeaders(msg *hsproto.GetHeaders, src p2p.Peer) {
	storeBase := r.blockStore.Base()
	storeHeight := r.blockStore.Height()
	fmt.Printf("respondGetHeaders: start=%d count=%d from=%s (our base=%d height=%d)\n",
		msg.StartHeight, msg.Count, src.ID(), storeBase, storeHeight)

	count := msg.Count
	if count > MaxHeaderBatchSize {
		count = MaxHeaderBatchSize
	}

	// Check if requested range is below our base
	if msg.StartHeight < storeBase {
		fmt.Printf("respondGetHeaders: requested height %d is below our base %d, sending empty\n",
			msg.StartHeight, storeBase)
	}

	headers := make([]*hsproto.SignedHeader, 0, count)
	for h := msg.StartHeight; h < msg.StartHeight+count; h++ {
		meta := r.blockStore.LoadBlockMeta(h)
		if meta == nil {
			fmt.Printf("respondGetHeaders: no block meta at height %d (base=%d), returning %d headers\n",
				h, storeBase, len(headers))
			break // Return what we have
		}

		commit := r.blockStore.LoadSeenCommit(h)
		if commit == nil {
			commit = r.blockStore.LoadBlockCommit(h)
		}
		if commit == nil {
			fmt.Printf("respondGetHeaders: no commit at height %d, returning %d headers\n",
				h, len(headers))
			break
		}

		headerProto := meta.Header.ToProto()
		commitProto := commit.ToProto()

		headers = append(headers, &hsproto.SignedHeader{
			Header: headerProto,
			Commit: commitProto,
		})
	}
	fmt.Printf("respondGetHeaders: sending %d headers starting at %d to %s\n",
		len(headers), msg.StartHeight, src.ID())

	sent := src.TrySend(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.HeadersResponse{
			StartHeight: msg.StartHeight,
			Headers:     headers,
		},
	})

	if !sent {
		fmt.Printf("respondGetHeaders: TrySend FAILED to %s\n", src.ID())
	}
}

// handleHeaders processes a HeadersResponse.
func (r *Reactor) handleHeaders(msg *hsproto.HeadersResponse, src p2p.Peer) {
	if len(msg.Headers) == 0 {
		fmt.Println("handleHeaders: empty response from", src.ID(), "for startHeight", msg.StartHeight)
		r.Logger.Debug("Received empty headers response", "peer", src.ID(), "startHeight", msg.StartHeight)
		// Empty response means peer doesn't have the headers (e.g., pruned).
		// Clear the batch so we can try a different peer.
		r.pool.RedoBatch(msg.StartHeight)
		return
	}

	// Convert proto to domain types.
	signedHeaders := make([]*SignedHeader, 0, len(msg.Headers))
	for _, protoSH := range msg.Headers {
		header, err := types.HeaderFromProto(protoSH.Header)
		if err != nil {
			r.Logger.Error("Failed to convert header from proto", "peer", src.ID(), "err", err)
			r.Switch.StopPeerForError(src, err, r.String())
			return
		}
		commit, err := types.CommitFromProto(protoSH.Commit)
		if err != nil {
			r.Logger.Error("Failed to convert commit from proto", "peer", src.ID(), "err", err)
			r.Switch.StopPeerForError(src, err, r.String())
			return
		}
		signedHeaders = append(signedHeaders, &SignedHeader{
			Header: &header,
			Commit: commit,
		})
	}

	if err := r.pool.AddBatchResponse(src.ID(), msg.StartHeight, signedHeaders); err != nil {
		r.Logger.Debug("Failed to add batch response", "peer", src.ID(), "startHeight", msg.StartHeight, "err", err)
		return
	}

	// Process headers immediately as they arrive.
	r.tryProcessHeaders()

	// After processing, try to make more requests.
	r.tryMakeRequests()
}

// timeoutRoutine periodically checks for timed out requests.
func (r *Reactor) timeoutRoutine() {
	ticker := time.NewTicker(timeoutCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Quit():
			return
		case <-ticker.C:
			r.tryMakeRequests()
		}
	}
}

// tryProcessHeaders processes all available headers in order.
func (r *Reactor) tryProcessHeaders() {
	for {
		sh, peerID, batchStartHeight := r.pool.PeekNextHeader()
		if sh == nil {
			return
		}

		if !r.processNextHeader(sh, peerID, batchStartHeight) {
			return
		}

		r.pool.MarkHeaderProcessed()
		r.headersSynced++

		if r.headersSynced%100 == 0 {
			rate := 100.0 / time.Since(r.lastHundredTime).Seconds()
			r.Logger.Info("Header Sync Rate",
				"height", r.pool.Height(),
				"max_peer_height", r.pool.MaxPeerHeight(),
				"headers/s", rate)
			r.metrics.SyncRate.Set(rate)
			r.lastHundredTime = time.Now()
		}
	}
}

// tryMakeRequests sends batch requests if slots are available.
func (r *Reactor) tryMakeRequests() {
	fmt.Println("tryMakeRequests")
	for {
		req := r.pool.GetNextRequest()
		if req == nil {
			break
		}
		r.sendGetHeaders(*req)
	}

	// Broadcast status if height advanced.
	currentHeight := r.blockStore.HeaderHeight()
	if currentHeight > r.lastBroadcastHeight.Load() {
		r.BroadcastStatus()
		r.lastBroadcastHeight.Store(currentHeight)
	}

	// Update metrics.
	height, numPending, numPeers := r.pool.GetStatus()
	r.metrics.HeaderHeight.Set(float64(height - 1))
	r.metrics.PendingRequests.Set(float64(numPending))
	r.metrics.Peers.Set(float64(numPeers))
}

// processNextHeader verifies and stores a single header.
// Returns true if processing succeeded, false if it failed.
func (r *Reactor) processNextHeader(sh *SignedHeader, peerID p2p.ID, batchStartHeight int64) bool {
	if err := r.verifyHeader(sh.Header, sh.Commit, r.lastHeader); err != nil {
		r.Logger.Error("Header verification failed",
			"height", sh.Header.Height,
			"peer", peerID,
			"err", err)
		r.pool.RedoBatch(batchStartHeight)
		peer := r.Switch.Peers().Get(peerID)
		if peer != nil {
			// TODO: re-enable
			// r.Switch.StopPeerForError(peer, err, r.String())
		}
		r.metrics.VerificationFailures.Add(1)
		return false
	}

	// Store header.
	if err := r.blockStore.SaveHeader(sh.Header, sh.Commit); err != nil {
		r.Logger.Error("Failed to save header", "err", err)
		return false
	}

	// Notify subscribers.
	vh := &VerifiedHeader{
		Header: sh.Header,
		Commit: sh.Commit,
		BlockID: types.BlockID{
			Hash:          sh.Header.Hash(),
			PartSetHeader: sh.Commit.BlockID.PartSetHeader,
		},
	}
	r.notifySubscribers(vh)

	r.lastHeader = sh.Header
	r.metrics.HeadersSynced.Add(1)

	return true
}

// verifyHeader verifies a header against the current validator set and chain linkage.
func (r *Reactor) verifyHeader(
	header *types.Header,
	commit *types.Commit,
	prevHeader *types.Header,
) error {
	// 1. Verify header's ValidatorsHash matches our current validators.
	if !bytes.Equal(header.ValidatorsHash, r.currentValidators.Hash()) {
		return fmt.Errorf("header validators hash mismatch: header has %X, we have %X",
			header.ValidatorsHash, r.currentValidators.Hash())
	}

	// 2. Compute expected BlockID from header.
	blockID := types.BlockID{
		Hash: header.Hash(),
		// PartSetHeader comes from the commit, which was signed by +2/3 validators.
		PartSetHeader: commit.BlockID.PartSetHeader,
	}

	// 3. Verify commit has +2/3 voting power from current validators.
	if err := r.currentValidators.VerifyCommitLight(r.chainID, blockID, header.Height, commit); err != nil {
		return fmt.Errorf("commit verification failed: %w", err)
	}

	// 4. Verify chain linkage (if not first header).
	if err := r.verifyChainLinkage(header, prevHeader); err != nil {
		return err
	}

	// 5. Check if validators will change for the next block.
	// If NextValidatorsHash differs from ValidatorsHash, we need to update.
	// TODO: For now, we require validators to remain unchanged. To support
	// validator changes, we'd need to fetch the new validator set from peers.
	if !bytes.Equal(header.NextValidatorsHash, header.ValidatorsHash) {
		r.Logger.Info("Validator set change detected but not yet supported",
			"height", header.Height,
			"currentHash", header.ValidatorsHash,
			"nextHash", header.NextValidatorsHash)
		// For now, continue - the next header verification will fail if validators changed.
	}

	return nil
}

// verifyChainLinkage verifies that the header correctly links to the previous header.
func (r *Reactor) verifyChainLinkage(header, prevHeader *types.Header) error {
	if prevHeader == nil {
		return nil
	}

	// Verify LastBlockID hash matches previous header.
	prevBlockID := r.loadPrevBlockID(prevHeader)
	if !bytes.Equal(header.LastBlockID.Hash, prevBlockID.Hash) {
		return fmt.Errorf("header LastBlockID hash mismatch: got %X, want %X",
			header.LastBlockID.Hash, prevBlockID.Hash)
	}

	// Verify ValidatorsHash continuity.
	if !bytes.Equal(header.ValidatorsHash, prevHeader.NextValidatorsHash) {
		return fmt.Errorf("validators hash mismatch: got %X, want %X",
			header.ValidatorsHash, prevHeader.NextValidatorsHash)
	}

	return nil
}

// loadPrevBlockID loads the BlockID for the previous header.
// If the commit is available, includes the PartSetHeader; otherwise just the hash.
func (r *Reactor) loadPrevBlockID(prevHeader *types.Header) types.BlockID {
	prevCommit := r.blockStore.LoadSeenCommit(prevHeader.Height)
	if prevCommit == nil {
		prevCommit = r.blockStore.LoadBlockCommit(prevHeader.Height)
	}

	if prevCommit != nil {
		return types.BlockID{
			Hash:          prevHeader.Hash(),
			PartSetHeader: prevCommit.BlockID.PartSetHeader,
		}
	}

	// If we don't have the commit, just use the hash.
	return types.BlockID{Hash: prevHeader.Hash()}
}

// sendGetHeaders sends a GetHeaders request to a peer.
func (r *Reactor) sendGetHeaders(req HeaderBatchRequest) {
	fmt.Println("send get headers!", req.PeerID, "start:", req.StartHeight, "count:", req.Count)
	peer := r.Switch.Peers().Get(req.PeerID)
	if peer == nil {
		fmt.Println("sendGetHeaders: peer not found", req.PeerID)
		return
	}

	r.Logger.Debug("Requesting headers",
		"peer", req.PeerID,
		"start", req.StartHeight,
		"count", req.Count)

	sent := peer.TrySend(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.GetHeaders{
			StartHeight: req.StartHeight,
			Count:       req.Count,
		},
	})
	fmt.Println("sendGetHeaders: TrySend result:", sent, "peer:", req.PeerID)
	if sent {
		fmt.Println("sent headers request", req.StartHeight, req.Count)
	}
}

// BroadcastStatus broadcasts our current status to all peers.
// Peers use push-based status updates (broadcasting when height changes)
// rather than pull-based requests, saving a round trip.
func (r *Reactor) BroadcastStatus() {
	fmt.Println("broadcasting status", r.blockStore.Base(), r.blockStore.HeaderHeight())
	r.Switch.Broadcast(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.StatusResponse{
			Base:   r.blockStore.Base(),
			Height: r.blockStore.HeaderHeight(),
		},
	})
}

// Subscribe registers a channel to receive verified headers.
func (r *Reactor) Subscribe(ch chan<- *VerifiedHeader) {
	r.subscribersMtx.Lock()
	defer r.subscribersMtx.Unlock()
	r.subscribers = append(r.subscribers, ch)
}

// Unsubscribe removes a subscriber channel.
func (r *Reactor) Unsubscribe(ch chan<- *VerifiedHeader) {
	r.subscribersMtx.Lock()
	defer r.subscribersMtx.Unlock()
	for i, sub := range r.subscribers {
		if sub == ch {
			r.subscribers = append(r.subscribers[:i], r.subscribers[i+1:]...)
			return
		}
	}
}

// notifySubscribers sends a verified header to all subscribers.
func (r *Reactor) notifySubscribers(vh *VerifiedHeader) {
	r.subscribersMtx.RLock()
	defer r.subscribersMtx.RUnlock()

	for _, ch := range r.subscribers {
		select {
		case ch <- vh:
		default:
			r.Logger.Info("Subscriber channel full, skipping notification",
				"height", vh.Header.Height)
		}
	}
}

// IsCaughtUp returns true if header sync has caught up to peers.
func (r *Reactor) IsCaughtUp() bool {
	return r.pool.IsCaughtUp()
}

// Height returns the current header sync height.
func (r *Reactor) Height() int64 {
	return r.pool.Height()
}

// MaxPeerHeight returns the highest peer header height.
func (r *Reactor) MaxPeerHeight() int64 {
	return r.pool.MaxPeerHeight()
}

// ResetHeight resets the reactor state to start syncing from the given height.
// This is used when the node state jumps (e.g. after state sync).
func (r *Reactor) ResetHeight(height int64) {
	r.lastHeader = r.blockStore.LoadHeader(height)
	r.pool.ResetHeight(height + 1)
}

// GetVerifiedHeader returns a verified header from the block store if available.
// This is used by the propagation reactor to check if a header has been verified
// before processing a compact block.
func (r *Reactor) GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	header := r.blockStore.LoadHeader(height)
	if header == nil {
		return nil, nil, false
	}

	// Load the commit to get the PartSetHeader for the BlockID.
	commit := r.blockStore.LoadSeenCommit(height)
	if commit == nil {
		commit = r.blockStore.LoadBlockCommit(height)
	}
	if commit == nil {
		return nil, nil, false
	}

	blockID := types.BlockID{
		Hash:          header.Hash(),
		PartSetHeader: commit.BlockID.PartSetHeader,
	}

	return header, &blockID, true
}

// GetCommit returns the commit for a given height.
// The commit for height H is found as the LastCommit in the header at H+1.
// This is used by the propagation reactor to provide commits for catchup blocks.
func (r *Reactor) GetCommit(height int64) *types.Commit {
	// First try to get it from the next block's commit
	nextHeader := r.blockStore.LoadHeader(height + 1)
	if nextHeader != nil {
		// The commit for height is stored as SeenCommit at height
		// or as the LastCommit in the block at height+1
		commit := r.blockStore.LoadSeenCommit(height)
		if commit != nil {
			return commit
		}
		commit = r.blockStore.LoadBlockCommit(height)
		if commit != nil {
			return commit
		}
	}

	// If we have the header but not the next block, check if we stored a SeenCommit
	commit := r.blockStore.LoadSeenCommit(height)
	if commit != nil {
		return commit
	}

	return nil
}

// PeersCount returns the number of connected peers in the pool.
func (r *Reactor) PeersCount() int {
	_, _, numPeers := r.pool.GetStatus()
	return numPeers
}
