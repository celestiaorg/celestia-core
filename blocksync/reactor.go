package blocksync

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

const (
	// BlocksyncChannel is a channel for blocks and status updates (`BlockStore` height)
	BlocksyncChannel = byte(0x40)

	trySyncIntervalMS = 10

	// stop syncing when last block's time is
	// within this much of the system time.
	// stopSyncingDurationMinutes = 10

	// ask for best height every 10s
	statusUpdateIntervalSeconds = 10
	// check if we should switch to consensus reactor
	switchToConsensusIntervalSeconds = 1

	// ReactorIncomingMessageQueueSize the size of the reactor's message queue.
	ReactorIncomingMessageQueueSize = 100

	// maxBlockBatchSize is the maximum number of blocks to accumulate before flushing
	maxBlockBatchSize    = 40
	targetBatchSizeBytes = 2 * 1024 * 1024 * 1024
)

type consensusReactor interface {
	// for when we switch from blocksync reactor and block sync to
	// the consensus machine
	SwitchToConsensus(state sm.State, skipWAL bool)
}

type peerError struct {
	err    error
	peerID p2p.ID
}

func (e peerError) Error() string {
	return fmt.Sprintf("error with peer %v: %s", e.peerID, e.err.Error())
}

// blockSaveData holds block data and timing information for batch saving
type blockSaveData struct {
	block              *types.Block
	blockParts         *types.PartSet
	seenCommit         *types.Commit
	seenExtendedCommit *types.ExtendedCommit
	blockSize          int
	validationDuration int64     // milliseconds
	saveDuration       int64     // milliseconds (will be set during batch flush)
	totalDuration      int64     // milliseconds (will be set during batch flush)
	batchStartTime     time.Time // time when this batch was started
}

// Reactor handles long-term catchup syncing.
type Reactor struct {
	p2p.BaseReactor

	// immutable
	initialState sm.State

	blockExec     *sm.BlockExecutor
	store         sm.BlockStore
	pool          *BlockPool
	traceClient   trace.Tracer
	blockSync     bool
	localAddr     crypto.Address
	poolRoutineWg sync.WaitGroup

	requestsCh <-chan BlockRequest
	errorsCh   <-chan peerError

	switchToConsensusMs int

	metrics *Metrics

	// Batch saving
	blockBatchMtx       sync.Mutex
	blockBatch          []blockSaveData
	blockBatchTotalSize int       // total size in bytes of current batch
	batchStartTime      time.Time // time when first block was added to batch
	batchSaveCh         chan []blockSaveData
	batchSaveChClosed   bool           // tracks if channel is already closed
	batchSaveWg         sync.WaitGroup // tracks the background goroutine lifecycle
}

// NewReactor returns new reactor instance.
func NewReactor(state sm.State, blockExec *sm.BlockExecutor, store *store.BlockStore,
	blockSync bool, metrics *Metrics, offlineStateSyncHeight int64,
) *Reactor {
	return NewReactorWithAddr(state, blockExec, store, blockSync, nil, metrics, offlineStateSyncHeight, trace.NoOpTracer())
}

// Function added to keep existing API.
func NewReactorWithAddr(state sm.State, blockExec *sm.BlockExecutor, store *store.BlockStore,
	blockSync bool, localAddr crypto.Address, metrics *Metrics, offlineStateSyncHeight int64, traceClient trace.Tracer,
) *Reactor {

	storeHeight := store.Height()
	if storeHeight == 0 {
		// If state sync was performed offline and the stores were bootstrapped to height H
		// the state store's lastHeight will be H while blockstore's Height and Base are still 0
		// 1. This scenario should not lead to a panic in this case, which is indicated by
		// having a OfflineStateSyncHeight > 0
		// 2. We need to instruct the blocksync reactor to start fetching blocks from H+1
		// instead of 0.
		storeHeight = offlineStateSyncHeight
	}
	if state.LastBlockHeight != storeHeight {
		panic(fmt.Sprintf("state (%v) and store (%v) height mismatch, stores were left in an inconsistent state", state.LastBlockHeight,
			storeHeight))
	}

	// It's okay to block since sendRequest is called from a separate goroutine
	// (bpRequester#requestRoutine; 1 per each peer).
	requestsCh := make(chan BlockRequest)

	const capacity = 1000                      // must be bigger than peers count
	errorsCh := make(chan peerError, capacity) // so we don't block in #Receive#pool.AddBlock

	startHeight := storeHeight + 1
	if startHeight == 1 {
		startHeight = state.InitialHeight
	}
	pool := NewBlockPool(startHeight, defaultMaxRequesters, requestsCh, errorsCh, traceClient)

	bcR := &Reactor{
		initialState: state,
		blockExec:    blockExec,
		store:        store,
		pool:         pool,
		blockSync:    blockSync,
		localAddr:    localAddr,
		requestsCh:   requestsCh,
		errorsCh:     errorsCh,
		metrics:      metrics,
		traceClient:  traceClient,
		batchSaveCh:  make(chan []blockSaveData, 10), // buffer 10 batches
	}
	bcR.BaseReactor = *p2p.NewBaseReactor("BlockSync", bcR, p2p.WithIncomingQueueSize(ReactorIncomingMessageQueueSize))
	return bcR
}

// SetLogger implements service.Service by setting the logger on reactor and pool.
func (bcR *Reactor) SetLogger(l log.Logger) {
	bcR.BaseService.Logger = l //nolint:staticcheck
	bcR.pool.Logger = l
}

// OnStart implements service.Service.
func (bcR *Reactor) OnStart() error {
	if bcR.blockSync {
		// Enable batched commit mode for the application
		// This prevents the app from persisting commits immediately during blocksync
		if err := bcR.blockExec.ProxyApp().SetCommitMode(proxy.CommitModeBatched); err != nil {
			return fmt.Errorf("failed to set batched commit mode: %w", err)
		}
		bcR.Logger.Info("Enabled batched commit mode for blocksync")

		err := bcR.pool.Start()
		if err != nil {
			return err
		}
		bcR.poolRoutineWg.Add(1)
		go func() {
			defer bcR.poolRoutineWg.Done()
			bcR.poolRoutine(false)
		}()
		// Start background batch save goroutine
		bcR.batchSaveWg.Add(1)
		go bcR.batchSaveRoutine()
	}
	return nil
}

// SwitchToBlockSync is called by the state sync reactor when switching to block sync.
func (bcR *Reactor) SwitchToBlockSync(state sm.State) error {
	bcR.blockSync = true
	bcR.initialState = state

	// Enable batched commit mode for the application
	if err := bcR.blockExec.ProxyApp().SetCommitMode(proxy.CommitModeBatched); err != nil {
		return fmt.Errorf("failed to set batched commit mode: %w", err)
	}
	bcR.Logger.Info("Enabled batched commit mode for blocksync")

	bcR.pool.height = state.LastBlockHeight + 1
	err := bcR.pool.Start()
	if err != nil {
		return err
	}
	bcR.poolRoutineWg.Add(1)
	go func() {
		defer bcR.poolRoutineWg.Done()
		bcR.poolRoutine(true)
	}()
	// Start background batch save goroutine
	bcR.batchSaveWg.Add(1)
	go bcR.batchSaveRoutine()
	return nil
}

// OnStop implements service.Service.
func (bcR *Reactor) OnStop() {
	if bcR.blockSync {
		if err := bcR.pool.Stop(); err != nil {
			bcR.Logger.Error("Error stopping pool", "err", err)
		}
		bcR.poolRoutineWg.Wait()

		// Flush any pending batch before stopping
		bcR.flushBlockBatch()

		// Stop background batch save goroutine
		// Close the channel to signal no more batches, goroutine will drain and exit
		bcR.blockBatchMtx.Lock()
		if !bcR.batchSaveChClosed {
			close(bcR.batchSaveCh)
			bcR.batchSaveChClosed = true
		}
		bcR.blockBatchMtx.Unlock()

		// Wait for the background goroutine to finish processing all batches
		bcR.batchSaveWg.Wait()

		// Switch back to immediate commit mode
		if err := bcR.blockExec.ProxyApp().SetCommitMode(proxy.CommitModeImmediate); err != nil {
			bcR.Logger.Error("Failed to switch back to immediate commit mode", "err", err)
		} else {
			bcR.Logger.Info("Switched back to immediate commit mode")
		}
	}
}

// GetChannels implements Reactor
func (bcR *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:                  BlocksyncChannel,
			Priority:            30,
			SendQueueCapacity:   1000,
			RecvBufferCapacity:  50 * 4096,
			RecvMessageCapacity: MaxMsgSize,
			MessageType:         &bcproto.Message{},
		},
	}
}

// AddPeer implements Reactor by sending our state to peer.
func (bcR *Reactor) AddPeer(peer p2p.Peer) {
	peer.Send(p2p.Envelope{
		ChannelID: BlocksyncChannel,
		Message: &bcproto.StatusResponse{
			Base:   bcR.store.Base(),
			Height: bcR.store.Height(),
		},
	})
	// it's OK if send fails. will try later in poolRoutine

	// peer is added to the pool once we receive the first
	// bcStatusResponseMessage from the peer and call pool.SetPeerRange
}

// RemovePeer implements Reactor by removing peer from the pool.
func (bcR *Reactor) RemovePeer(peer p2p.Peer, _ interface{}) {
	bcR.pool.RemovePeer(peer.ID())
}

// respondToPeer loads a block and sends it to the requesting peer,
// if we have it. Otherwise, we'll respond saying we don't have it.
func (bcR *Reactor) respondToPeer(msg *bcproto.BlockRequest, src p2p.Peer) (queued bool) {
	block := bcR.store.LoadBlock(msg.Height)
	if block == nil {
		bcR.Logger.Info("Peer asking for a block we don't have", "src", src, "height", msg.Height)
		return src.TrySend(p2p.Envelope{
			ChannelID: BlocksyncChannel,
			Message:   &bcproto.NoBlockResponse{Height: msg.Height},
		})
	}

	state, err := bcR.blockExec.Store().Load()
	if err != nil {
		bcR.Logger.Error("loading state", "err", err)
		return false
	}
	var extCommit *types.ExtendedCommit
	if state.ConsensusParams.ABCI.VoteExtensionsEnabled(msg.Height) {
		extCommit = bcR.store.LoadBlockExtendedCommit(msg.Height)
		if extCommit == nil {
			bcR.Logger.Error("found block in store with no extended commit", "block", block)
			return false
		}
	}

	bl, err := block.ToProto()
	if err != nil {
		bcR.Logger.Error("could not convert msg to protobuf", "err", err)
		return false
	}

	return src.TrySend(p2p.Envelope{
		ChannelID: BlocksyncChannel,
		Message: &bcproto.BlockResponse{
			Block:     bl,
			ExtCommit: extCommit.ToProto(),
		},
	})
}

// Receive implements Reactor by handling 4 types of messages (look below).
func (bcR *Reactor) Receive(e p2p.Envelope) {
	if err := ValidateMsg(e.Message); err != nil {
		bcR.Logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		bcR.Switch.StopPeerForError(e.Src, err, bcR.String())
		return
	}

	bcR.Logger.Trace("Receive", "e.Src", e.Src, "chID", e.ChannelID, "msg", e.Message)

	switch msg := e.Message.(type) { //nolint:dupl // recreated in a test
	case *bcproto.BlockRequest:
		bcR.respondToPeer(msg, e.Src)
	case *bcproto.BlockResponse:
		bi, err := types.BlockFromProto(msg.Block)
		if err != nil {
			bcR.Logger.Error("Peer sent us invalid block", "peer", e.Src, "msg", e.Message, "err", err)
			bcR.Switch.StopPeerForError(e.Src, err, bcR.String())
			return
		}
		var extCommit *types.ExtendedCommit
		if msg.ExtCommit != nil {
			var err error
			extCommit, err = types.ExtendedCommitFromProto(msg.ExtCommit)
			if err != nil {
				bcR.Logger.Error("failed to convert extended commit from proto",
					"peer", e.Src,
					"err", err)
				bcR.Switch.StopPeerForError(e.Src, err, bcR.String())
				return
			}
		}

		schema.WriteBlocksyncBlockReceived(bcR.traceClient, bi.Height, string(e.Src.ID()), msg.Block.Size())

		if err := bcR.pool.AddBlock(e.Src.ID(), bi, extCommit, msg.Block.Size()); err != nil {
			bcR.Logger.Error("failed to add block", "peer", e.Src, "err", err)
		}
	case *bcproto.StatusRequest:
		// Send peer our state.
		e.Src.TrySend(p2p.Envelope{
			ChannelID: BlocksyncChannel,
			Message: &bcproto.StatusResponse{
				Height: bcR.store.Height(),
				Base:   bcR.store.Base(),
			},
		})
	case *bcproto.StatusResponse:
		// Got a peer status. Unverified.
		bcR.pool.SetPeerRange(e.Src.ID(), msg.Base, msg.Height)
	case *bcproto.NoBlockResponse:
		bcR.Logger.Trace("Peer does not have requested block", "peer", e.Src, "height", msg.Height)
		bcR.pool.RedoRequestFrom(msg.Height, e.Src.ID())
	default:
		bcR.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
	}
}

func (bcR *Reactor) localNodeBlocksTheChain(state sm.State) bool {
	_, val := state.Validators.GetByAddress(bcR.localAddr)
	if val == nil {
		return false
	}
	total := state.Validators.TotalVotingPower()
	return val.VotingPower >= total/3
}

// addBlockToBatch adds a block to the save batch and flushes if the batch is full.
// Batch is flushed when either:
// - Number of blocks reaches maxBlockBatchSize (20 blocks), OR
// - Total batch size reaches targetBatchSizeBytes (5MB)
func (bcR *Reactor) addBlockToBatch(data blockSaveData) {
	bcR.blockBatchMtx.Lock()
	defer bcR.blockBatchMtx.Unlock()

	if len(bcR.blockBatch) == 0 {
		bcR.batchStartTime = time.Now()
		bcR.blockBatchTotalSize = 0
	}

	bcR.blockBatch = append(bcR.blockBatch, data)
	bcR.blockBatchTotalSize += data.blockSize

	// Flush if batch reached max blocks or target size
	shouldFlush := len(bcR.blockBatch) >= maxBlockBatchSize ||
		bcR.blockBatchTotalSize >= targetBatchSizeBytes

	if shouldFlush {
		bcR.flushBlockBatchLocked()
	}
}

// batchSaveRoutine runs in the background and saves batches to disk.
func (bcR *Reactor) batchSaveRoutine() {
	defer bcR.batchSaveWg.Done()

	// Process batches until channel is closed
	for batch := range bcR.batchSaveCh {
		if err := bcR.saveBatchToDisk(batch); err != nil {
			bcR.Logger.Error("Failed to save batch to disk", "err", err)
			// TODO: Consider more sophisticated error handling (retry, halt, etc.)
		}
	}
}

// saveBatchToDisk performs the actual disk save operation.
// It flushes app commits, saves blocks to blockstore, and ensures all databases are synchronized.
func (bcR *Reactor) saveBatchToDisk(batch []blockSaveData) error {
	if len(batch) == 0 {
		return nil
	}

	// Start timing the batch save
	saveStart := time.Now()
	batchStartTime := batch[0].batchStartTime // Use the actual batch start time

	// Step 1: Flush all pending app commits
	// This ensures application.db is synchronized with the blocks we're about to save
	ctx := context.Background()
	if err := bcR.blockExec.ProxyApp().FlushCommitBatch(ctx); err != nil {
		return fmt.Errorf("failed to flush commit batch: %w", err)
	}

	// Step 2: Convert to types.BlockBatchEntry
	entries := make([]types.BlockBatchEntry, len(batch))
	for i, data := range batch {
		entries[i] = types.BlockBatchEntry{
			Block:              data.block,
			BlockParts:         data.blockParts,
			SeenCommit:         data.seenCommit,
			SeenExtendedCommit: data.seenExtendedCommit,
		}
	}

	// Step 3: Save blocks to blockstore (single fsync)
	if err := bcR.store.SaveBlockBatch(entries); err != nil {
		return fmt.Errorf("failed to save block batch: %w", err)
	}

	// Calculate timing
	saveDuration := time.Since(saveStart).Milliseconds()

	// Calculate batch statistics
	startHeight := batch[0].block.Height
	endHeight := batch[len(batch)-1].block.Height
	totalSize := 0
	for _, data := range batch {
		totalSize += data.blockSize
	}

	// Log batch save - all 3 databases (application.db, blockstore.db, state.db) are now synchronized
	bcR.Logger.Info("Saved block batch (all databases synchronized)",
		"start_height", startHeight,
		"end_height", endHeight,
		"num_blocks", len(batch),
		"total_size_kb", totalSize/1024,
		"save_duration_ms", saveDuration)

	// Trace the batch save operation
	schema.WriteBlocksyncBatchSaved(bcR.traceClient, startHeight, endHeight, len(batch), totalSize, saveDuration)

	// Write traces for all blocks in the batch
	for _, data := range batch {
		totalDuration := time.Since(batchStartTime).Milliseconds()
		schema.WriteBlocksyncBlockSaved(bcR.traceClient, data.block.Height, data.blockSize,
			data.validationDuration, saveDuration, totalDuration)
	}

	return nil
}

// flushBlockBatch flushes any pending blocks in the batch to disk.
// Must be called with blockBatchMtx held.
func (bcR *Reactor) flushBlockBatchLocked() {
	if len(bcR.blockBatch) == 0 {
		return
	}

	// Set batchStartTime for all blocks in this batch
	batchStartTime := bcR.batchStartTime
	for i := range bcR.blockBatch {
		bcR.blockBatch[i].batchStartTime = batchStartTime
	}

	// Make a copy of the batch to send to background goroutine
	batchCopy := make([]blockSaveData, len(bcR.blockBatch))
	copy(batchCopy, bcR.blockBatch)

	// Send batch to background goroutine for async save (if channel is still open)
	// Blocking send - provides natural backpressure if disk can't keep up
	if !bcR.batchSaveChClosed {
		bcR.batchSaveCh <- batchCopy
	}

	// Clear the batch so we can start accumulating the next one
	bcR.blockBatch = nil
	bcR.blockBatchTotalSize = 0
}

// flushBlockBatch is the public version that acquires the lock.
func (bcR *Reactor) flushBlockBatch() {
	bcR.blockBatchMtx.Lock()
	defer bcR.blockBatchMtx.Unlock()
	bcR.flushBlockBatchLocked()
}

// Handle messages from the poolReactor telling the reactor what to do.
// NOTE: Don't sleep in the FOR_LOOP or otherwise slow it down!
func (bcR *Reactor) poolRoutine(stateSynced bool) {
	bcR.metrics.Syncing.Set(1)
	defer bcR.metrics.Syncing.Set(0)

	trySyncTicker := time.NewTicker(trySyncIntervalMS * time.Millisecond)
	defer trySyncTicker.Stop()

	statusUpdateTicker := time.NewTicker(statusUpdateIntervalSeconds * time.Second)
	defer statusUpdateTicker.Stop()

	if bcR.switchToConsensusMs == 0 {
		bcR.switchToConsensusMs = switchToConsensusIntervalSeconds * 1000
	}
	switchToConsensusTicker := time.NewTicker(time.Duration(bcR.switchToConsensusMs) * time.Millisecond)
	defer switchToConsensusTicker.Stop()

	blocksSynced := uint64(0)

	chainID := bcR.initialState.ChainID
	state := bcR.initialState

	lastHundred := time.Now()
	lastRate := 0.0

	didProcessCh := make(chan struct{}, 1)

	initialCommitHasExtensions := (bcR.initialState.LastBlockHeight > 0 && bcR.store.LoadBlockExtendedCommit(bcR.initialState.LastBlockHeight) != nil)

	go func() {
		for {
			select {
			case <-bcR.Quit():
				return
			case <-bcR.pool.Quit():
				return
			case request := <-bcR.requestsCh:
				peer := bcR.Switch.Peers().Get(request.PeerID)
				if peer == nil {
					continue
				}
				queued := peer.TrySend(p2p.Envelope{
					ChannelID: BlocksyncChannel,
					Message:   &bcproto.BlockRequest{Height: request.Height},
				})
				if !queued {
					bcR.Logger.Debug("Send queue is full, drop block request", "peer", peer.ID(), "height", request.Height)
				}
			case err := <-bcR.errorsCh:
				peer := bcR.Switch.Peers().Get(err.peerID)
				if peer != nil {
					bcR.Switch.StopPeerForError(peer, err, bcR.String())
				}

			case <-statusUpdateTicker.C:
				// ask for status updates
				go bcR.BroadcastStatusRequest()

			}
		}
	}()

FOR_LOOP:
	for {
		select {
		case <-switchToConsensusTicker.C:
			height, numPending, lenRequesters := bcR.pool.GetStatus()
			outbound, inbound, _ := bcR.Switch.NumPeers()
			bcR.Logger.Trace("Consensus ticker", "numPending", numPending, "total", lenRequesters,
				"outbound", outbound, "inbound", inbound, "lastHeight", state.LastBlockHeight)

			// The "if" statement below is a bit confusing, so here is a breakdown
			// of its logic and purpose:
			//
			// If we are at genesis (no block in the chain), we don't need VoteExtensions
			// because the first block's LastCommit is empty anyway.
			//
			// If VoteExtensions were disabled for the previous height then we don't need
			// VoteExtensions.
			//
			// If we have sync'd at least one block, then we are guaranteed to have extensions
			// if we need them by the logic inside loop FOR_LOOP: it requires that the blocks
			// it fetches have extensions if extensions were enabled during the height.
			//
			// If we already had extensions for the initial height (e.g. we are recovering),
			// then we are guaranteed to have extensions for the last block (if required) even
			// if we did not blocksync any block.
			//
			missingExtension := true
			if state.LastBlockHeight == 0 ||
				!state.ConsensusParams.ABCI.VoteExtensionsEnabled(state.LastBlockHeight) ||
				blocksSynced > 0 ||
				initialCommitHasExtensions {
				missingExtension = false
			}

			// If require extensions, but since we don't have them yet, then we cannot switch to consensus yet.
			if missingExtension {
				bcR.Logger.Info(
					"no extended commit yet",
					"height", height,
					"last_block_height", state.LastBlockHeight,
					"initial_height", state.InitialHeight,
					"max_peer_height", bcR.pool.MaxPeerHeight(),
				)
				continue FOR_LOOP
			}
			if bcR.pool.IsCaughtUp() || bcR.localNodeBlocksTheChain(state) {
				bcR.Logger.Info("Time to switch to consensus reactor!", "height", height)
				// Flush any remaining blocks
				bcR.flushBlockBatch()
				// Close channel to signal no more batches, then wait for goroutine to finish
				bcR.blockBatchMtx.Lock()
				if !bcR.batchSaveChClosed {
					close(bcR.batchSaveCh)
					bcR.batchSaveChClosed = true
				}
				bcR.blockBatchMtx.Unlock()
				// Wait for goroutine to drain all batches and exit
				bcR.Logger.Info("Waiting for batch saves to complete before switching to consensus")
				bcR.batchSaveWg.Wait()
				bcR.Logger.Info("All batches saved, switching to consensus")

				// Switch back to immediate commit mode before switching to consensus
				if err := bcR.blockExec.ProxyApp().SetCommitMode(proxy.CommitModeImmediate); err != nil {
					bcR.Logger.Error("Failed to switch back to immediate commit mode", "err", err)
				} else {
					bcR.Logger.Info("Switched back to immediate commit mode")
				}

				if err := bcR.pool.Stop(); err != nil {
					bcR.Logger.Error("Error stopping pool", "err", err)
				}
				conR, ok := bcR.Switch.Reactor("CONSENSUS").(consensusReactor)
				if ok {
					conR.SwitchToConsensus(state, blocksSynced > 0 || stateSynced)
				}
				// else {
				// should only happen during testing
				// }

				break FOR_LOOP
			}

		case <-trySyncTicker.C: // chan time
			select {
			case didProcessCh <- struct{}{}:
			default:
			}

		case <-didProcessCh:
			// NOTE: It is a subtle mistake to process more than a single block
			// at a time (e.g. 10) here, because we only TrySend 1 request per
			// loop.  The ratio mismatch can result in starving of blocks, a
			// sudden burst of requests and responses, and repeat.
			// Consequently, it is better to split these routines rather than
			// coupling them as it's written here.  TODO uncouple from request
			// routine.

			// See if there are any blocks to sync.
			first, second, extCommit := bcR.pool.PeekTwoBlocks()
			if first == nil || second == nil {
				// we need to have fetched two consecutive blocks in order to
				// perform blocksync verification
				continue FOR_LOOP
			}
			// Some sanity checks on heights
			if state.LastBlockHeight > 0 && state.LastBlockHeight+1 != first.Height {
				// Panicking because the block pool's height  MUST keep consistent with the state; the block pool is totally under our control
				panic(fmt.Errorf("peeked first block has unexpected height; expected %d, got %d", state.LastBlockHeight+1, first.Height))
			}
			if first.Height+1 != second.Height {
				// Panicking because this is an obvious bug in the block pool, which is totally under our control
				panic(fmt.Errorf("heights of first and second block are not consecutive; expected %d, got %d", state.LastBlockHeight, first.Height))
			}

			// Before priming didProcessCh for another check on the next
			// iteration, break the loop if the BlockPool or the Reactor itself
			// has quit. This avoids case ambiguity of the outer select when two
			// channels are ready.
			if !bcR.IsRunning() || !bcR.pool.IsRunning() {
				break FOR_LOOP
			}
			// Try again quickly next loop.
			didProcessCh <- struct{}{}

			firstParts, err := first.MakePartSet(types.BlockPartSizeBytes)
			if err != nil {
				bcR.Logger.Error("failed to make ",
					"height", first.Height,
					"err", err.Error())
				break FOR_LOOP
			}
			firstPartSetHeader := firstParts.Header()
			firstID := types.BlockID{Hash: first.Hash(), PartSetHeader: firstPartSetHeader}

			// Start timing validation
			validationStart := time.Now()

			// Finally, verify the first block using the second's commit
			// NOTE: we can probably make this more efficient, but note that calling
			// first.Hash() doesn't verify the tx contents, so MakePartSet() is
			// currently necessary.
			// TODO(sergio): Should we also validate against the extended commit?
			err = state.Validators.VerifyCommitLight(
				chainID, firstID, first.Height, second.LastCommit)

			if err == nil {
				// validate the block before we persist it
				err = bcR.blockExec.ValidateBlock(state, first)
			}

			if err == nil {
				var stateMachineValid bool
				// Block sync doesn't check that the `Data` in a block is valid.
				// Since celestia-core can't determine if the `Data` in a block
				// is valid, the next line asks celestia-app to check if the
				// block is valid via ProcessProposal. If this step wasn't
				// performed, a malicious node could fabricate an alternative
				// set of transactions that would cause a different app hash and
				// thus cause this node to panic.
				stateMachineValid, err = bcR.blockExec.ProcessProposal(first, state.InitialHeight)
				if !stateMachineValid {
					err = fmt.Errorf("application has rejected syncing block (%X) at height %d, %w", first.Hash(), first.Height, err)
				}
			}

			// Calculate validation duration
			validationDuration := time.Since(validationStart)

			// Trace block validation timing
			var blockSize int
			if firstParts != nil {
				blockSize = int(firstParts.ByteSize())
			}
			schema.WriteBlocksyncBlockValidated(bcR.traceClient, first.Height, blockSize, validationDuration.Milliseconds())

			presentExtCommit := extCommit != nil
			extensionsEnabled := state.ConsensusParams.ABCI.VoteExtensionsEnabled(first.Height)
			if presentExtCommit != extensionsEnabled {
				err = fmt.Errorf("non-nil extended commit must be received iff vote extensions are enabled for its height "+
					"(height %d, non-nil extended commit %t, extensions enabled %t)",
					first.Height, presentExtCommit, extensionsEnabled,
				)
			}
			if err == nil && extensionsEnabled {
				// if vote extensions were required at this height, ensure they exist.
				err = extCommit.EnsureExtensions(true)
			}
			if err != nil {
				bcR.Logger.Error("Error in validation", "err", err)
				peerID := bcR.pool.RemovePeerAndRedoAllPeerRequests(first.Height)
				peer := bcR.Switch.Peers().Get(peerID)
				if peer != nil {
					// NOTE: we've already removed the peer's request, but we
					// still need to clean up the rest.
					bcR.Switch.StopPeerForError(peer, ErrReactorValidation{Err: err}, bcR.String())
				}
				peerID2 := bcR.pool.RemovePeerAndRedoAllPeerRequests(second.Height)
				peer2 := bcR.Switch.Peers().Get(peerID2)
				if peer2 != nil && peer2 != peer {
					// NOTE: we've already removed the peer's request, but we
					// still need to clean up the rest.
					bcR.Switch.StopPeerForError(peer2, ErrReactorValidation{Err: err}, bcR.String())
				}
				continue FOR_LOOP
			}

			bcR.pool.PopRequest()

			// Apply block to update state BEFORE batching the save
			// This must happen immediately so the next block can be validated
			state, err = bcR.blockExec.ApplyVerifiedBlock(state, firstID, first, second.LastCommit)
			if err != nil {
				// TODO This is bad, are we zombie?
				panic(fmt.Sprintf("Failed to process committed block (%d:%X): %v", first.Height, first.Hash(), err))
			}

			// Add block to save batch (will flush when batch reaches 10 blocks)
			var commit *types.Commit
			var extendedCommit *types.ExtendedCommit
			if extensionsEnabled {
				extendedCommit = extCommit
			} else {
				// We use LastCommit here instead of extCommit. extCommit is not
				// guaranteed to be populated by the peer if extensions are not enabled.
				commit = second.LastCommit
			}
			bcR.addBlockToBatch(blockSaveData{
				block:              first,
				blockParts:         firstParts,
				seenCommit:         commit,
				seenExtendedCommit: extendedCommit,
				blockSize:          blockSize,
				validationDuration: validationDuration.Milliseconds(),
			})

			// Flush batch immediately if we're caught up or close to it
			// This prevents race conditions in tests where store height is checked
			// while blocks are still in the batch buffer
			if bcR.pool.IsCaughtUp() || bcR.pool.height >= bcR.pool.MaxPeerHeight()-1 {
				bcR.flushBlockBatch()
			}

			bcR.metrics.recordBlockMetrics(first)
			blocksSynced++

			if blocksSynced%100 == 0 {
				lastRate = 0.9*lastRate + 0.1*(100/time.Since(lastHundred).Seconds())
				bcR.Logger.Info("Block Sync Rate", "height", bcR.pool.height,
					"max_peer_height", bcR.pool.MaxPeerHeight(), "blocks/s", lastRate)
				lastHundred = time.Now()
			}

			continue FOR_LOOP

		case <-bcR.Quit():
			break FOR_LOOP
		case <-bcR.pool.Quit():
			break FOR_LOOP
		}
	}

	// Flush any remaining blocks in the batch before exiting
	bcR.flushBlockBatch()
}

// BroadcastStatusRequest broadcasts `BlockStore` base and height.
func (bcR *Reactor) BroadcastStatusRequest() {
	bcR.Switch.Broadcast(p2p.Envelope{
		ChannelID: BlocksyncChannel,
		Message:   &bcproto.StatusRequest{},
	})
}
