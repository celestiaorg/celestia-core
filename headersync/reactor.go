package headersync

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"
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
	HeaderSyncChannel = byte(0x60)

	// trySyncIntervalMS is how often to check for completed batches.
	trySyncIntervalMS = 10

	// ReactorIncomingMessageQueueSize is the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 100

	// maxPeerGetHeadersPerSecond is the maximum number of GetHeaders requests
	// a peer can make per second before being rate-limited.
	maxPeerGetHeadersPerSecond = 5

	// peerRequestWindowSeconds is the time window for rate limiting.
	peerRequestWindowSeconds = 1
)

// peerRequestTracker tracks request timestamps for rate limiting.
type peerRequestTracker struct {
	timestamps []time.Time
}

// Reactor handles header synchronization.
type Reactor struct {
	p2p.BaseReactor

	pool       *HeaderPool
	stateStore sm.Store
	blockStore *store.BlockStore
	chainID    string

	// Last verified header for chain linkage verification.
	lastHeader *types.Header

	// Subscriber management.
	subscribersMtx sync.RWMutex
	subscribers    []chan<- *VerifiedHeader

	// Rate limiting for incoming GetHeaders requests.
	peerRequestsMtx sync.Mutex
	peerRequests    map[p2p.ID]*peerRequestTracker

	requestsCh <-chan HeaderBatchRequest
	errorsCh   <-chan peerError

	poolWg sync.WaitGroup

	metrics *Metrics
}

// ReactorOption defines a function argument for Reactor.
type ReactorOption func(*Reactor)

// NewReactor returns a new header sync reactor.
func NewReactor(
	stateStore sm.Store,
	blockStore *store.BlockStore,
	chainID string,
	batchSize int64,
	metrics *Metrics,
	options ...ReactorOption,
) *Reactor {
	if metrics == nil {
		metrics = NopMetrics()
	}

	requestsCh := make(chan HeaderBatchRequest, 100)
	errorsCh := make(chan peerError, 100)

	// Start syncing from one past the current header height.
	startHeight := blockStore.HeaderHeight() + 1
	if startHeight < 1 {
		startHeight = 1
	}

	pool := NewHeaderPool(startHeight, batchSize, requestsCh, errorsCh)

	r := &Reactor{
		pool:         pool,
		stateStore:   stateStore,
		blockStore:   blockStore,
		chainID:      chainID,
		subscribers:  make([]chan<- *VerifiedHeader, 0),
		peerRequests: make(map[p2p.ID]*peerRequestTracker),
		requestsCh:   requestsCh,
		errorsCh:     errorsCh,
		metrics:      metrics,
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

	if err := r.pool.Start(); err != nil {
		return err
	}

	r.poolWg.Add(1)
	go func() {
		defer r.poolWg.Done()
		r.poolRoutine()
	}()

	return nil
}

// OnStop implements service.Service.
func (r *Reactor) OnStop() {
	if err := r.pool.Stop(); err != nil {
		r.Logger.Error("Error stopping pool", "err", err)
	}
	r.poolWg.Wait()
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
	if err := ValidateMsg(e.Message); err != nil {
		r.Logger.Error("Peer sent invalid message", "peer", e.Src, "msg", e.Message, "err", err)
		r.Switch.StopPeerForError(e.Src, err, r.String())
		return
	}

	r.Logger.Debug("Received message", "peer", e.Src.ID(), "msg", reflect.TypeOf(e.Message))

	switch msg := e.Message.(type) {
	case *hsproto.StatusResponse:
		// SetPeerRange returns false if this is a DoS attempt (same or lower height).
		if !r.pool.SetPeerRange(e.Src.ID(), msg.Base, msg.Height) {
			r.Switch.StopPeerForError(e.Src, errors.New("status update with non-increasing height"), r.String())
		}

	case *hsproto.GetHeaders:
		if !r.checkPeerRateLimit(e.Src.ID()) {
			r.Logger.Debug("Rate limiting GetHeaders request", "peer", e.Src.ID())
			return
		}
		r.respondGetHeaders(msg, e.Src)

	case *hsproto.HeadersResponse:
		r.handleHeaders(msg, e.Src)

	default:
		r.Logger.Error("Unknown message type", "msg", reflect.TypeOf(msg))
	}
}

// respondGetHeaders responds to a GetHeaders request.
func (r *Reactor) respondGetHeaders(msg *hsproto.GetHeaders, src p2p.Peer) {
	count := msg.Count
	if count > MaxHeaderBatchSize {
		count = MaxHeaderBatchSize
	}

	headers := make([]*hsproto.SignedHeader, 0, count)
	for h := msg.StartHeight; h < msg.StartHeight+count; h++ {
		meta := r.blockStore.LoadBlockMeta(h)
		if meta == nil {
			break // Return what we have
		}

		commit := r.blockStore.LoadSeenCommit(h)
		if commit == nil {
			commit = r.blockStore.LoadBlockCommit(h)
		}
		if commit == nil {
			break
		}

		headerProto := meta.Header.ToProto()
		commitProto := commit.ToProto()

		headers = append(headers, &hsproto.SignedHeader{
			Header: headerProto,
			Commit: commitProto,
		})
	}

	src.TrySend(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.HeadersResponse{
			StartHeight: msg.StartHeight,
			Headers:     headers,
		},
	})
}

// handleHeaders processes a HeadersResponse.
func (r *Reactor) handleHeaders(msg *hsproto.HeadersResponse, src p2p.Peer) {
	if len(msg.Headers) == 0 {
		r.Logger.Debug("Received empty headers response", "peer", src.ID(), "startHeight", msg.StartHeight)
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
	}
}

// poolRoutine handles header verification and storage.
func (r *Reactor) poolRoutine() {
	r.metrics.Syncing.Set(1)
	defer r.metrics.Syncing.Set(0)

	trySyncTicker := time.NewTicker(trySyncIntervalMS * time.Millisecond)
	defer trySyncTicker.Stop()

	lastHundred := time.Now()
	headersSynced := int64(0)
	lastBroadcastHeight := r.blockStore.HeaderHeight()

	for {
		select {
		case <-r.Quit():
			return

		case <-r.pool.Quit():
			return

		case request := <-r.requestsCh:
			r.sendGetHeaders(request)

		case err := <-r.errorsCh:
			peer := r.Switch.Peers().Get(err.peerID)
			if peer != nil {
				r.Switch.StopPeerForError(peer, err, r.String())
			}

		case <-trySyncTicker.C:
			// Process headers as they arrive (streaming).
			// This allows verification to start immediately when headers arrive in order,
			// rather than waiting for the entire batch to complete.
			for {
				sh, peerID, batchStartHeight := r.pool.PeekNextHeader()
				if sh == nil {
					break
				}

				if !r.processNextHeader(sh, peerID, batchStartHeight) {
					break
				}

				r.pool.MarkHeaderProcessed()
				headersSynced++

				if headersSynced%100 == 0 && headersSynced > 0 {
					rate := 100.0 / time.Since(lastHundred).Seconds()
					r.Logger.Info("Header Sync Rate",
						"height", r.pool.Height(),
						"max_peer_height", r.pool.MaxPeerHeight(),
						"headers/s", rate)
					r.metrics.SyncRate.Set(rate)
					lastHundred = time.Now()
				}
			}

			// Broadcast our new status if height has advanced.
			// Peers use push-based status updates rather than pull-based requests.
			currentHeight := r.blockStore.HeaderHeight()
			if currentHeight > lastBroadcastHeight {
				r.BroadcastStatus()
				lastBroadcastHeight = currentHeight
			}

			// Update metrics.
			height, numPending, numPeers := r.pool.GetStatus()
			r.metrics.HeaderHeight.Set(float64(height - 1))
			r.metrics.PendingRequests.Set(float64(numPending))
			r.metrics.Peers.Set(float64(numPeers))
		}
	}
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
			r.Switch.StopPeerForError(peer, err, r.String())
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

// verifyHeader verifies a header against the validator set and chain linkage.
func (r *Reactor) verifyHeader(
	header *types.Header,
	commit *types.Commit,
	prevHeader *types.Header,
) error {
	// 1. Load validator set for this height.
	vals, err := r.stateStore.LoadValidators(header.Height)
	if err != nil {
		return fmt.Errorf("failed to load validators at height %d: %w", header.Height, err)
	}

	// 2. Compute expected BlockID from header.
	blockID := types.BlockID{
		Hash: header.Hash(),
		// PartSetHeader comes from the commit, which was signed by +2/3 validators.
		PartSetHeader: commit.BlockID.PartSetHeader,
	}

	// 3. Verify commit has +2/3 voting power.
	if err := vals.VerifyCommitLight(r.chainID, blockID, header.Height, commit); err != nil {
		return fmt.Errorf("commit verification failed: %w", err)
	}

	// 4. Verify chain linkage (if not first header).
	if err := r.verifyChainLinkage(header, prevHeader); err != nil {
		return err
	}

	// 5. Verify header.ValidatorsHash matches loaded validators.
	if !bytes.Equal(header.ValidatorsHash, vals.Hash()) {
		return fmt.Errorf("header validators hash does not match state store: got %X, want %X",
			header.ValidatorsHash, vals.Hash())
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
	peer := r.Switch.Peers().Get(req.PeerID)
	if peer == nil {
		return
	}

	r.Logger.Debug("Requesting headers",
		"peer", req.PeerID,
		"start", req.StartHeight,
		"count", req.Count)

	peer.TrySend(p2p.Envelope{
		ChannelID: HeaderSyncChannel,
		Message: &hsproto.GetHeaders{
			StartHeight: req.StartHeight,
			Count:       req.Count,
		},
	})
}

// BroadcastStatus broadcasts our current status to all peers.
// Peers use push-based status updates (broadcasting when height changes)
// rather than pull-based requests, saving a round trip.
func (r *Reactor) BroadcastStatus() {
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
