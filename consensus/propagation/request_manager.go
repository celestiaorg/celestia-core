package propagation

import (
	"context"
	"time"

	"github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/pkg/trace/schema"
)

type PeersGetterFunc func(context.Context) ([]*PeerState, error)

type HaveWithFrom struct {
	*types.HaveParts
	from p2p.ID
}

var (
	// todo: avoid endin up in programmer hell by not using a global var
	RetryTime = 6 * time.Second
)

type RequestManager struct {
	ctx       context.Context
	mtx       sync.RWMutex
	logger    log.Logger
	peerState map[p2p.ID]*PeerState
	*ProposalCache
	haveChan       <-chan HaveWithFrom
	CommitmentChan <-chan *types.CompactBlock
	fetcher        *partFetcher
	traceClient    trace.Tracer
}

func NewRequestsManager(
	ctx context.Context,
	tracer trace.Tracer,
	peerState map[p2p.ID]*PeerState,
	proposalCache *ProposalCache,
	haveChan <-chan HaveWithFrom,
	compactBlockChan <-chan *types.CompactBlock,
) *RequestManager {
	return &RequestManager{
		ctx:            ctx,
		mtx:            sync.RWMutex{},
		peerState:      peerState,
		ProposalCache:  proposalCache,
		logger:         log.NewNopLogger(),
		haveChan:       haveChan,
		CommitmentChan: compactBlockChan,
		fetcher:        newPartFetcher(log.NewNopLogger()),
		traceClient:    tracer,
	}
}

func (rm *RequestManager) WithLogger(logger log.Logger) {
	rm.logger = logger
	rm.fetcher.logger = logger
}

func (rm *RequestManager) Start() {
	ticker := time.NewTicker(RetryTime)
	rm.logger.Info("starting request manager")
	for {
		select {
		case <-rm.ctx.Done():
			return
		case <-ticker.C:
			rm.retryUnfinishedHeights()
		case have, has := <-rm.haveChan:
			rm.logger.Info("received have", "have", have)
			if !has {
				return
			}
			rm.handleHave(&have)
		case compactBlock, has := <-rm.CommitmentChan:
			rm.logger.Info("received commitment", "height", compactBlock.Proposal.Height, "round", compactBlock.Proposal.Round)
			if !has {
				return
			}
			rm.retryUnfinishedHeights()
			ticker.Reset(RetryTime)
		}
	}
}

func (rm *RequestManager) Stop() {
}

func (rm *RequestManager) ReceivedPart(from p2p.ID, part *types.RecoveryPart) {
	rm.fetcher.ReceivedPart(from, part)
}

// getPeers returns a list of all peers that the requests manager is aware of.
func (rm *RequestManager) getPeers() []*PeerState {
	rm.mtx.RLock()
	defer rm.mtx.RUnlock()
	peers := make([]*PeerState, 0, len(rm.peerState))
	for _, peer := range rm.peerState {
		peers = append(peers, peer)
	}
	return peers
}

// prune takes a height and a round and removes them from the peer state.
// also, removes that height and round from the sent wants and total sent wants.
func (rm *RequestManager) prune(height int64, round int32) {
	// TODO also call the partFetcher.prune
}

func (rm *RequestManager) handleHave(have *HaveWithFrom) (wantSent bool) {
	_, parts, _, has := rm.getAllState(have.Height, have.Round, false)
	if !has {
		// this shouldn't happen
		rm.logger.Error("couldn't find state for proposal", "height", have.Height, "round", have.Round)
		// TODO maybe return?
		return
	}
	hc := have.BitArray(int(parts.Total()))
	hc = hc.Sub(parts.BitArray())
	if hc.IsEmpty() {
		// we have all the parts that this peer has
		return
	}
	peer := rm.peerState[have.from]
	to := peer.peer
	want := types.WantParts{
		Parts:  hc,
		Height: have.Height,
		Round:  have.Round,
		Prove:  false,
	}
	remaining, err := rm.fetcher.sendRequest(to, &want, false)
	if err != nil {
		rm.logger.Error("failed to send want parts", "peer", have.from, "height", have.Height, "round", have.Round)
		return
	}
	sent := hc
	if remaining != nil {
		sent = hc.Sub(remaining)
	}
	// FIXME maybe delete these lines too
	want.Parts = sent

	schema.WriteBlockPartState(
		rm.traceClient,
		have.Height,
		have.Round,
		sent.GetTrueIndices(),
		false,
		string(have.from),
		schema.Haves,
	)

	// keep track of the parts that this node has requested.
	peer.AddSentWants(have.Height, have.Round, sent)
	peer.AddReceivedHave(have.Height, have.Round, hc)
	rm.logger.Info("added sent wants and received haves", "want", want, "to", to.ID())
	return true
}

func (rm *RequestManager) retryUnfinishedHeights() {
	unfinishedHeights := rm.unfinishedHeights()
	rm.logger.Info("unfinished heights", "unfinishedHeights", len(unfinishedHeights))
	for _, unfinishedHeight := range unfinishedHeights {
		rm.retryUnfinishedHeight(
			unfinishedHeight.compactBlock.Proposal.Height,
			unfinishedHeight.compactBlock.Proposal.Round,
		)
	}
}

func (rm *RequestManager) retryUnfinishedHeight(height int64, round int32) {
	_, parts, _, has := rm.getAllState(height, round, true)
	if !has {
		// this shouldn't happen
		rm.logger.Error("couldn't find state for proposal", "height", height, "round", round)
		return
	}
	if parts.IsComplete() {
		return
	}
	missing := parts.MissingOriginal()
	for _, peer := range shuffle(rm.getPeers()) {
		if missing.IsEmpty() {
			break
		}
		switch {
		case peer.latestHeight > height || (peer.latestHeight == height && peer.latestRound > round):
			rm.logger.Info("peer is at a higher height")
			// this peer is at a higher height, we can request everything from them that we didn't request
			want := types.WantParts{
				Parts:  missing,
				Height: height,
				Round:  round,
				Prove:  true,
			}
			remaining, err := rm.fetcher.sendRequest(peer.peer, &want, true)
			if err != nil {
				rm.logger.Error("failed to send catchup want parts", "peer", peer.peer.ID(), "height", height, "round", round, "err", err)
				continue
			}
			if remaining != nil {
				missing = missing.Sub(remaining)
			}
		case peer.latestHeight == height && peer.latestRound == round:
			rm.logger.Info("peer is at the same height/round")
			// this peer is at the same, we can request only the data they broadcasted available to them.
			remaining := rm.requestUsingHaves(peer, height, round, missing)
			if remaining != nil {
				missing = missing.Sub(remaining)
			}
		}
		// TODO check if we really need to keep track of the sent/recived wants etc
	}
}

// requestUsingHaves sends a request for missing parts from a peer based on what the peer has advertised.
// It computes the parts to request by subtracting the known parts from the peer's advertised parts.
// The method updates the sent wants for tracking and returns the updated missing parts after the request.
// Returns the remaining missing parts.
func (rm *RequestManager) requestUsingHaves(peer *PeerState, height int64, round int32, missing *bits.BitArray) *bits.BitArray {
	peerHaves, has := peer.GetReceivedHaves(height, round)
	if !has {
		return missing
	}
	notMissed := missing.Not()
	toRequest := peerHaves
	if notMissed != nil {
		toRequest = peerHaves.Sub(notMissed)
	}
	want := types.WantParts{
		Parts:  toRequest,
		Height: height,
		Round:  round,
		Prove:  true,
	}
	remaining, err := rm.fetcher.sendRequest(peer.peer, &want, true)
	if err != nil {
		rm.logger.Error("failed to send catchup want parts", "peer", peer.peer.ID(), "height", height, "round", round, "err", err)
		return missing
	}
	if remaining != nil {
		missing = missing.Sub(remaining)
	}
	// TODO udpate also the peerstate.AddSentWants, and AddReceivedHaves
	return missing
}
