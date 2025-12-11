package propagation

import (
	"fmt"
	"time"

	sm "github.com/cometbft/cometbft/state"
)

const (
	// syncCheckInterval is how often to check if we're caught up.
	syncCheckInterval = 1 * time.Second

	// minPeersForCatchup is the minimum number of peers needed to consider ourselves caught up.
	minPeersForCatchup = 1
)

// ConsensusReactor is the interface for switching to consensus mode.
// Exported for use by other packages.
type ConsensusReactor interface {
	SwitchToConsensus(state sm.State, skipWAL bool)
}

// SwitchToCatchup starts the catchup sync process on the propagation reactor.
// This replaces blocksync's SwitchToBlockSync method.
//
// The propagation reactor already handles block downloads via:
// - loadUnprocessedHeaders() on startup loads headers without block data
// - handleVerifiedHeaders() processes new headers from headersync
// - retryWants() requests missing parts on a timer
// - onBlockAdded() triggers immediate catchup for new blocks
//
// This method just needs to monitor progress and switch to consensus when caught up.
func (r *Reactor) SwitchToCatchup(state sm.State, blockExec *sm.BlockExecutor, conR ConsensusReactor) error {
	r.Logger.Info("Starting catchup sync",
		"height", state.LastBlockHeight,
		"from_state_sync", true)

	// Update the pending blocks manager with the new height from state sync.
	// This ensures we start downloading from the correct height (H+1).
	r.pendingBlocks.Prune(state.LastBlockHeight)

	// If headersync is enabled, we need to reset it to the new height as well.
	if r.headerSyncReactor != nil {
		r.headerSyncReactor.ResetHeight(state.LastBlockHeight)
	}

	// Load any unprocessed headers that might be available now.
	r.loadUnprocessedHeaders()

	// Enable processing.
	r.StartProcessing()

	// Start the sync monitoring routine.
	go r.catchupRoutine(state, conR)

	return nil
}

// catchupRoutine monitors sync progress and switches to consensus when caught up.
func (r *Reactor) catchupRoutine(state sm.State, conR ConsensusReactor) {
	syncCheckTicker := time.NewTicker(syncCheckInterval)
	defer syncCheckTicker.Stop()

	startHeight := state.LastBlockHeight
	startTime := time.Now()
	blocksSynced := int64(0)

	r.Logger.Info("Catchup routine started", "start_height", startHeight)

	for {
		select {
		case <-r.ctx.Done():
			return

		case <-syncCheckTicker.C:
			// Update blocks synced count.
			if r.pendingBlocks.Store() != nil {
				currentHeight := r.pendingBlocks.Store().Height()
				blocksSynced = currentHeight - startHeight
			}

			// Check if we're caught up.
			if r.IsCaughtUp() {
				r.Logger.Info("Caught up! Switching to consensus",
					"height", r.pendingBlocks.Store().Height(),
					"blocks_synced", blocksSynced,
					"duration", time.Since(startTime))

				// Update state to current height before handoff.
				// The state passed in is from state sync, we need current state.
				// However, consensus reactor will load the correct state.
				if conR != nil {
					conR.SwitchToConsensus(state, blocksSynced > 0)
				}
				return
			}

			// Log progress periodically.
			r.Logger.Debug("Catchup progress",
				"store_height", r.pendingBlocks.Store().Height(),
				"max_peer_height", r.GetMaxPeerHeight(),
				"pending_blocks", r.pendingBlocks.Len(),
				"blocks_synced", blocksSynced)
		}
	}
}

// GetMaxPeerHeight returns the maximum height reported by peers.
// Uses headersync if available for the most accurate height.
func (r *Reactor) GetMaxPeerHeight() int64 {
	if r.headerSyncReactor != nil {
		return r.headerSyncReactor.MaxPeerHeight()
	}

	// Fall back to querying peer consensus state.
	maxHeight := int64(0)
	peers := r.getPeers()
	for _, peer := range peers {
		if peer.consensusPeerState != nil {
			height := peer.consensusPeerState.GetHeight()
			if height > maxHeight {
				maxHeight = height
			}
		}
	}
	return maxHeight
}

// IsCaughtUp returns true if the reactor has caught up to the network.
func (r *Reactor) IsCaughtUp() bool {
	store := r.pendingBlocks.Store()
	if store == nil {
		return false
	}

	storeHeight := store.Height()
	maxPeerHeight := r.GetMaxPeerHeight()

	// Need at least 1 peer.
	peers := r.getPeers()
	if len(peers) < minPeersForCatchup {
		return false
	}

	// If we have headersync, ensure it has discovered peers before we trust maxPeerHeight.
	// After a reset (e.g. state sync), headersync might need time to receive status updates
	// from peers, even if p2p connections are established.
	if r.headerSyncReactor != nil && r.headerSyncReactor.PeersCount() == 0 {
		return false
	}

	// Similar logic to blocksync: we're caught up if our height >= maxPeerHeight - 1
	// (because we need the next block's commit to verify).
	receivedBlockOrTimedOut := storeHeight > 0
	ourChainIsLongestAmongPeers := maxPeerHeight == 0 || storeHeight >= (maxPeerHeight-1)

	return receivedBlockOrTimedOut && ourChainIsLongestAmongPeers
}

// localNodeBlocksTheChain returns true if this node has enough voting power
// to block the chain (1/3+). In this case, we should switch to consensus
// even if not fully caught up.
func (r *Reactor) localNodeBlocksTheChain(state sm.State) bool {
	if r.privval == nil {
		return false
	}
	pubKey, err := r.privval.GetPubKey()
	if err != nil {
		return false
	}
	localAddr := pubKey.Address()

	_, val := state.Validators.GetByAddress(localAddr)
	if val == nil {
		return false
	}
	total := state.Validators.TotalVotingPower()
	return val.VotingPower >= total/3
}

// Ensure Reactor implements the catchup interface.
var _ interface {
	SwitchToCatchup(sm.State, *sm.BlockExecutor, ConsensusReactor) error
} = (*Reactor)(nil)

// Legacy compatibility - IsSyncing can be used to check sync status.
func (r *Reactor) IsSyncing() bool {
	// We're syncing if we have pending blocks and aren't caught up.
	return r.pendingBlocks.Len() > 0 && !r.IsCaughtUp()
}

// GetSyncStatus returns current sync status information.
func (r *Reactor) GetSyncStatus() (storeHeight, maxPeerHeight int64, pendingBlocks int, caughtUp bool) {
	store := r.pendingBlocks.Store()
	if store != nil {
		storeHeight = store.Height()
	}
	maxPeerHeight = r.GetMaxPeerHeight()
	pendingBlocks = r.pendingBlocks.Len()
	caughtUp = r.IsCaughtUp()
	return
}

// EnsureCatchupStarted can be called to ensure catchup is running after state sync.
// This is idempotent - if already started, it does nothing.
func (r *Reactor) EnsureCatchupStarted() {
	// The reactor already runs retryWants on a timer and handles headers.
	// This method exists for explicit startup if needed.
	if !r.started.Load() {
		r.StartProcessing()
	}
}

// WaitForCatchup blocks until caught up or context is cancelled.
// This is useful for tests.
func (r *Reactor) WaitForCatchup(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-ticker.C:
			if r.IsCaughtUp() {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for catchup")
			}
		}
	}
}
