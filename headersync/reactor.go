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
	cmtmath "github.com/cometbft/cometbft/libs/math"
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
	// Protected by validatorsMtx for concurrent access from consensus handoff.
	validatorsMtx     sync.RWMutex
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

		sh := &hsproto.SignedHeader{
			Header: headerProto,
			Commit: commitProto,
		}

		// Attach validator set if it changed at this height.
		// The validator set changed if the previous header's NextValidatorsHash
		// differs from this header's ValidatorsHash.
		if h > 1 {
			prevMeta := r.blockStore.LoadBlockMeta(h - 1)
			if prevMeta != nil && !bytes.Equal(prevMeta.Header.NextValidatorsHash, meta.Header.ValidatorsHash) {
				// Validator set changed - attach the new validator set.
				vals, err := r.stateStore.LoadValidators(h)
				if err == nil {
					valsProto, err := vals.ToProto()
					if err == nil {
						sh.ValidatorSet = valsProto
						r.Logger.Debug("Attaching validator set to header", "height", h)
					} else {
						r.Logger.Error("Failed to convert validators to proto", "height", h, "err", err)
					}
				} else {
					r.Logger.Error("Failed to load validators for header", "height", h, "err", err)
				}
			}
		}

		headers = append(headers, sh)
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

		sh := &SignedHeader{
			Header: &header,
			Commit: commit,
		}

		// Decode validator set if present.
		if protoSH.ValidatorSet != nil {
			valSet, err := types.ValidatorSetFromProto(protoSH.ValidatorSet)
			if err != nil {
				r.Logger.Error("Failed to convert validator set from proto", "peer", src.ID(), "err", err)
				r.Switch.StopPeerForError(src, err, r.String())
				return
			}
			sh.ValidatorSet = valSet
		}

		signedHeaders = append(signedHeaders, sh)
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

// tryProcessHeaders processes all available batches in order.
func (r *Reactor) tryProcessHeaders() {
	for {
		batch, peerID := r.pool.PeekCompletedBatch()
		if batch == nil {
			return
		}

		if !r.processBatch(batch.headers, peerID, batch.startHeight) {
			return
		}

		r.pool.PopBatch(batch.startHeight)
		r.headersSynced += int64(len(batch.headers))

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

// processBatch verifies and stores a batch of headers.
// Returns true if processing succeeded, false if it failed.
func (r *Reactor) processBatch(headers []*SignedHeader, peerID p2p.ID, batchStartHeight int64) bool {
	if len(headers) == 0 {
		return true
	}

	// Verify the batch using skip verification.
	if err := r.verifyBatch(headers); err != nil {
		r.Logger.Error("Batch verification failed",
			"startHeight", batchStartHeight,
			"count", len(headers),
			"peer", peerID,
			"err", err)
		r.pool.RedoBatch(batchStartHeight)
		// Disconnect the peer that sent invalid headers.
		peer := r.Switch.Peers().Get(peerID)
		if peer != nil {
			r.Switch.StopPeerForError(peer, err, r.String())
		}
		r.metrics.VerificationFailures.Add(1)
		return false
	}

	// Store all headers in the batch.
	for _, sh := range headers {
		if err := r.blockStore.SaveHeader(sh.Header, sh.Commit); err != nil {
			r.Logger.Error("Failed to save header", "height", sh.Header.Height, "err", err)
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

		r.metrics.HeadersSynced.Add(1)
	}

	// Update lastHeader to the last header in the batch.
	r.lastHeader = headers[len(headers)-1].Header

	return true
}

// verifyBatch verifies a batch of headers using skip verification.
// This verifies that 1/3+ of the trusted validator set signed the last header,
// then verifies chain linkage backwards from last to first.
func (r *Reactor) verifyBatch(batch []*SignedHeader) error {
	if len(batch) == 0 {
		return errors.New("empty batch")
	}

	// Get current validators under read lock.
	r.validatorsMtx.RLock()
	validators := r.currentValidators
	r.validatorsMtx.RUnlock()

	// Step 1: Skip verify last header (1/3+ of trusted validators must have signed).
	lastHeader := batch[len(batch)-1]
	if err := r.skipVerifyHeader(lastHeader, validators); err != nil {
		// <1/3 overlap - try to find an intermediate trust anchor.
		r.Logger.Debug("Skip verification failed, trying intermediate anchors",
			"height", lastHeader.Header.Height, "err", err)
		return r.verifyBatchWithIntermediateTrust(batch, validators)
	}

	// Step 2: Verify chain linkage backwards from last to first.
	if err := r.verifyChainLinkageBackward(batch); err != nil {
		return err
	}

	// Step 3: Update validator set from batch if it contains a change.
	r.updateValidatorSetFromBatch(batch)

	return nil
}

// skipVerifyHeader verifies that 1/3+ of the trusted validators signed the header.
func (r *Reactor) skipVerifyHeader(sh *SignedHeader, validators *types.ValidatorSet) error {
	// Use 1/3 trust threshold.
	trustLevel := cmtmath.Fraction{Numerator: 1, Denominator: 3}
	return types.VerifyCommitLightTrusting(
		r.chainID,
		validators,
		sh.Commit,
		trustLevel,
	)
}

// verifyChainLinkageBackward verifies hash chain from last header back to first.
func (r *Reactor) verifyChainLinkageBackward(batch []*SignedHeader) error {
	// First, verify linkage to our last known header.
	if r.lastHeader != nil {
		firstInBatch := batch[0]
		expectedHash := r.lastHeader.Hash()
		if !bytes.Equal(firstInBatch.Header.LastBlockID.Hash, expectedHash) {
			return fmt.Errorf("first header LastBlockID.Hash %X doesn't match previous header hash %X",
				firstInBatch.Header.LastBlockID.Hash, expectedHash)
		}
		// Verify ValidatorsHash continuity from our last header.
		if !bytes.Equal(firstInBatch.Header.ValidatorsHash, r.lastHeader.NextValidatorsHash) {
			return fmt.Errorf("first header ValidatorsHash %X doesn't match previous NextValidatorsHash %X",
				firstInBatch.Header.ValidatorsHash, r.lastHeader.NextValidatorsHash)
		}
	}

	// Verify internal chain linkage.
	for i := len(batch) - 1; i > 0; i-- {
		current := batch[i]
		prev := batch[i-1]
		expectedHash := prev.Header.Hash()
		if !bytes.Equal(current.Header.LastBlockID.Hash, expectedHash) {
			return fmt.Errorf("header %d LastBlockID.Hash %X doesn't match header %d hash %X",
				current.Header.Height, current.Header.LastBlockID.Hash,
				prev.Header.Height, expectedHash)
		}
		// Verify ValidatorsHash continuity within batch.
		if !bytes.Equal(current.Header.ValidatorsHash, prev.Header.NextValidatorsHash) {
			return fmt.Errorf("header %d ValidatorsHash %X doesn't match header %d NextValidatorsHash %X",
				current.Header.Height, current.Header.ValidatorsHash,
				prev.Header.Height, prev.Header.NextValidatorsHash)
		}
	}

	return nil
}

// updateValidatorSetFromBatch updates currentValidators if the batch contains
// a validator set change. Takes the last validator set in the batch.
func (r *Reactor) updateValidatorSetFromBatch(batch []*SignedHeader) {
	// Scan backwards to find the last header with an attached validator set.
	for i := len(batch) - 1; i >= 0; i-- {
		if batch[i].ValidatorSet != nil {
			// Validate that the attached validator set matches the header's hash.
			expectedHash := batch[i].Header.ValidatorsHash
			actualHash := batch[i].ValidatorSet.Hash()
			if !bytes.Equal(expectedHash, actualHash) {
				r.Logger.Error("Validator set hash mismatch, ignoring",
					"height", batch[i].Header.Height,
					"expected", expectedHash,
					"actual", actualHash)
				continue
			}

			r.validatorsMtx.Lock()
			r.currentValidators = batch[i].ValidatorSet
			r.validatorsMtx.Unlock()

			r.Logger.Info("Updated validator set from batch",
				"height", batch[i].Header.Height,
				"validatorsHash", actualHash)
			return
		}
	}
}

// verifyBatchWithIntermediateTrust handles the rare case where <1/3 of trusted
// validators signed the last header. Searches for an intermediate header where
// we still have 1/3+ overlap.
func (r *Reactor) verifyBatchWithIntermediateTrust(batch []*SignedHeader, validators *types.ValidatorSet) error {
	// Linear scan to find a header we can trust.
	for i := len(batch) - 2; i >= 0; i-- {
		if err := r.skipVerifyHeader(batch[i], validators); err == nil {
			// Found a trustable header - verify linkage from start to this point.
			if err := r.verifyChainLinkageBackward(batch[:i+1]); err != nil {
				return err
			}

			// Update validator set from this header if it has one.
			if batch[i].ValidatorSet != nil {
				expectedHash := batch[i].Header.ValidatorsHash
				actualHash := batch[i].ValidatorSet.Hash()
				if bytes.Equal(expectedHash, actualHash) {
					r.validatorsMtx.Lock()
					validators = batch[i].ValidatorSet
					r.currentValidators = validators
					r.validatorsMtx.Unlock()
				}
			}

			// Recursively verify the rest of the batch with updated validators.
			return r.verifyBatchRecursive(batch[i+1:], validators)
		}
	}
	return fmt.Errorf("no header in batch has 1/3+ overlap with trusted validators")
}

// verifyBatchRecursive is a helper for verifyBatchWithIntermediateTrust.
func (r *Reactor) verifyBatchRecursive(batch []*SignedHeader, validators *types.ValidatorSet) error {
	if len(batch) == 0 {
		return nil
	}

	// Try to skip verify the last header.
	lastHeader := batch[len(batch)-1]
	if err := r.skipVerifyHeader(lastHeader, validators); err != nil {
		// Need to find another intermediate anchor.
		return r.verifyBatchWithIntermediateTrust(batch, validators)
	}

	// Verify chain linkage for this sub-batch.
	for i := len(batch) - 1; i > 0; i-- {
		current := batch[i]
		prev := batch[i-1]
		expectedHash := prev.Header.Hash()
		if !bytes.Equal(current.Header.LastBlockID.Hash, expectedHash) {
			return fmt.Errorf("header %d LastBlockID.Hash %X doesn't match header %d hash %X",
				current.Header.Height, current.Header.LastBlockID.Hash,
				prev.Header.Height, expectedHash)
		}
		if !bytes.Equal(current.Header.ValidatorsHash, prev.Header.NextValidatorsHash) {
			return fmt.Errorf("header %d ValidatorsHash %X doesn't match header %d NextValidatorsHash %X",
				current.Header.Height, current.Header.ValidatorsHash,
				prev.Header.Height, prev.Header.NextValidatorsHash)
		}
	}

	// Update validator set from this sub-batch.
	for i := len(batch) - 1; i >= 0; i-- {
		if batch[i].ValidatorSet != nil {
			expectedHash := batch[i].Header.ValidatorsHash
			actualHash := batch[i].ValidatorSet.Hash()
			if bytes.Equal(expectedHash, actualHash) {
				r.validatorsMtx.Lock()
				r.currentValidators = batch[i].ValidatorSet
				r.validatorsMtx.Unlock()
			}
			break
		}
	}

	return nil
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

// UpdateValidatorSet updates the reactor's current validator set.
// Called by consensus when a new block is committed that changes the validator set.
// This ensures headersync has the correct validator set when catching up to tip.
func (r *Reactor) UpdateValidatorSet(validators *types.ValidatorSet) {
	r.validatorsMtx.Lock()
	defer r.validatorsMtx.Unlock()
	r.currentValidators = validators
	r.Logger.Info("Validator set updated by consensus", "hash", validators.Hash())
}
