package propagation

import (
	"context"
	"fmt"
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

type sentWant struct {
	*types.WantParts
	timestamp time.Time
	to        p2p.ID
}

type HaveWithFrom struct {
	*types.HaveParts
	from p2p.ID
}

// TODO set the latest height/round in sync data
type RequestManager struct {
	ctx       context.Context
	mtx       sync.RWMutex
	logger    log.Logger
	peerState map[p2p.ID]*PeerState
	*ProposalCache
	haveChan        <-chan HaveWithFrom
	CommitmentChan  <-chan *types.CompactBlock
	expiredWantChan chan *sentWant
	height          int64
	round           int32
	sentWants       map[p2p.ID][]*sentWant
	fetcher         *partFetcher
	traceClient     trace.Tracer
}

func NewRequestsManager(ctx context.Context, tracer trace.Tracer, peerState map[p2p.ID]*PeerState, proposalCache *ProposalCache, haveChan <-chan HaveWithFrom, compactBlockChan <-chan *types.CompactBlock) *RequestManager {
	return &RequestManager{
		ctx:             ctx,
		mtx:             sync.RWMutex{},
		peerState:       peerState,
		ProposalCache:   proposalCache,
		logger:          log.NewNopLogger(),
		haveChan:        haveChan,
		CommitmentChan:  compactBlockChan,
		sentWants:       make(map[p2p.ID][]*sentWant),
		expiredWantChan: make(chan *sentWant, 100),
		fetcher:         newPartFetcher(log.NewNopLogger()),
		traceClient:     tracer,
	}
}

func (rm *RequestManager) WithLogger(logger log.Logger) {
	rm.logger = logger
	rm.fetcher.logger = logger
}

func (rm *RequestManager) Start() {
	go rm.expireWants()
	tickerDuration := 6 * time.Second
	ticker := time.NewTicker(tickerDuration)
	rm.logger.Info("starting request manager")
	for {
		rm.logger.Info("starting request manager loop")
		select {
		case <-rm.ctx.Done():
			// TODO refactor all the cases into methods
			return
		//case <-ticker.C:
		// This case should help advance the propagation in the following case:
		// - We receive haves for some parts
		// - The peer that sent us the haves is almost saturated with requests
		// - We request some wants, but the remaining are not requested
		// - no other peer send us haves for those parts, and we don't advance to a new height
		// due to multiple rounds of consensus.
		// Note: the ticker is reset with every received have to avoid triggering it
		// unnecessarily.
		// TODO think more about this case
		case expiredWant, has := <-rm.expiredWantChan:
			rm.logger.Info("received expired want", "want", expiredWant)
			if !has {
				return
			}
			rm.handleExpiredWant(expiredWant)
		case have, has := <-rm.haveChan:
			rm.logger.Info("received have", "have", have)
			if !has {
				return
			}
			wantSent := rm.handleHave(&have)
			if wantSent {
				ticker.Reset(tickerDuration)
			}
		case compactBlock, has := <-rm.CommitmentChan:
			rm.logger.Info("received commitment", "commitment", compactBlock)
			if !has {
				return
			}
			rm.handleCommitment(compactBlock)
		}
	}
}

func (rm *RequestManager) Stop() {
	close(rm.expiredWantChan)
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

// expireWants TODO call it from the start function.
func (rm *RequestManager) expireWants() {
	// TODO maybe also expire old unanswered wants if we start receiving data from the peer.
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-rm.ctx.Done():
			return
		case <-ticker.C:
			for peerID, wants := range rm.sentWants {
				for index, want := range wants {
					if time.Now().Sub(want.timestamp) >= 2*time.Second {
						err := rm.removeExpiredWant(peerID, index)
						if err != nil {
							rm.logger.Error("failed to remove expired want", "peer", peerID, "index", index, "err", err)
							continue
						}
						rm.expiredWantChan <- want
					}
				}
			}
		}
	}
}

func (rm *RequestManager) removeExpiredWant(id p2p.ID, index int) error {
	peerRequests, has := rm.sentWants[id]
	if !has {
		return fmt.Errorf("peer %s has no sent wants", id)
	}
	if index >= len(peerRequests) || index < 0 {
		return fmt.Errorf("index %d is out of bounds %d", index, len(peerRequests))
	}
	if index+1 == len(peerRequests) {
		rm.sentWants[id] = rm.sentWants[id][:index]
		return nil
	}
	rm.sentWants[id] = append(rm.sentWants[id][:index], rm.sentWants[id][index+1:]...)
	return nil
}

func (rm *RequestManager) handleExpiredWant(expiredWant *sentWant) {
	rm.mtx.RLock()
	height, round := rm.height, rm.round
	rm.mtx.RUnlock()
	if expiredWant.Height != height || expiredWant.Round != round {
		// this expired want is for a previous height/round, we can ignore it.
		return
	}
	_, parts, _, has := rm.getAllState(expiredWant.Height, expiredWant.Round, false)
	if !has {
		// this shouldn't happen
		rm.logger.Error("couldn't find state for proposal", "height", expiredWant.Height, "round", expiredWant.Round)
		return
	}
	if parts.IsComplete() {
		return
	}
	missing := parts.MissingOriginal()
	notMissed := missing.Not()
	toRequest := expiredWant.Parts.Sub(notMissed)
	for !toRequest.IsEmpty() {
		peers := shuffle(rm.getPeers())
		to := peers[0].peer
		pendingWant := types.WantParts{
			Parts:  toRequest,
			Height: expiredWant.Height,
			Round:  expiredWant.Round,
			Prove:  expiredWant.Prove,
		}
		remaining, err := rm.fetcher.sendRequest(to, &pendingWant)
		if err != nil {
			rm.logger.Error("failed to send want parts", "peer", peers[0].peer.ID(), "height", expiredWant.Height, "round", expiredWant.Round)
			continue
		}
		toRequest = toRequest.Sub(remaining)
		rm.sentWants[to.ID()] = append(rm.sentWants[to.ID()], &sentWant{
			WantParts: &pendingWant,
			timestamp: time.Now(),
			to:        to.ID(),
		})
	}
}

func (rm *RequestManager) handleHave(have *HaveWithFrom) (wantSent bool) {
	rm.mtx.RLock()
	height, round := rm.height, rm.round
	rm.mtx.RUnlock()
	if have.Height != height || have.Round != round {
		// this shouldn't happen as we should only receive have parts for the current height and round
		return
	}
	_, parts, _, has := rm.getAllState(height, round, false)
	if !has {
		// this shouldn't happen
		rm.logger.Error("couldn't find state for proposal", "height", height, "round", round)
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
	remaining, err := rm.fetcher.sendRequest(to, &want)
	if err != nil {
		rm.logger.Error("failed to send want parts", "peer", have.from, "height", have.Height, "round", have.Round)
		return
	}
	sent := hc.Sub(remaining)
	want.Parts = sent
	rm.sentWants[to.ID()] = append(rm.sentWants[to.ID()], &sentWant{
		WantParts: &want,
		timestamp: time.Now(),
		to:        have.from,
	})

	schema.WriteBlockPartState(
		rm.traceClient,
		height,
		round,
		sent.GetTrueIndices(),
		false,
		string(have.from),
		schema.Haves,
	)

	// keep track of the parts that this node has requested.
	peer.AddSentWants(height, round, sent)
	peer.AddReceivedHave(height, round, hc)
	return true
}

func (rm *RequestManager) handleCommitment(compactBlock *types.CompactBlock) {
	rm.mtx.RLock()
	// TODO find a way to increment these once we have all the data for a height/round
	// maybe do it in the data sync routine.
	height, round := rm.height, rm.round
	rm.mtx.RUnlock()
	if height == compactBlock.Proposal.Height && round == compactBlock.Proposal.Round {
		// we're already at the new height/round, we can wait for haves.
		return
	}
	_, parts, _, has := rm.getAllState(height, round, false)
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
			// this peer is at a higher height, we can request everything from them that we didn't request
			want := types.WantParts{
				Parts:  missing,
				Height: height,
				Round:  round,
				Prove:  true,
			}
			remaining, err := rm.fetcher.sendRequest(peer.peer, &want)
			if err != nil {
				rm.logger.Error("failed to send catchup want parts", "peer", peer.peer.ID(), "height", height, "round", round, "err", err)
				continue
			}
			missing = missing.Sub(remaining)
			rm.sentWants[peer.peer.ID()] = append(rm.sentWants[peer.peer.ID()], &sentWant{
				WantParts: &want,
				timestamp: time.Now(),
				to:        peer.peer.ID(),
			})
		case peer.latestHeight == height || peer.latestRound == round:
			// this peer is at the same, we can request only the data they broadcasted available to them.
			remaining := rm.requestUsingHaves(peer, height, round, missing)
			missing = missing.Sub(remaining)
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
	toRequest := peerHaves.Sub(notMissed)
	want := types.WantParts{
		Parts:  toRequest,
		Height: height,
		Round:  round,
		Prove:  true,
	}
	remaining, err := rm.fetcher.sendRequest(peer.peer, &want)
	if err != nil {
		rm.logger.Error("failed to send catchup want parts", "peer", peer.peer.ID(), "height", height, "round", round, "err", err)
		return missing
	}
	missing = missing.Sub(remaining)
	rm.sentWants[peer.peer.ID()] = append(rm.sentWants[peer.peer.ID()], &sentWant{
		WantParts: &want,
		timestamp: time.Now(),
		to:        peer.peer.ID(),
	})
	// TODO udpate also the peerstate.AddSentWants, and AddReceivedHaves
	return missing
}

func (rm *RequestManager) setHeight(height int64) {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	rm.height = height
	// TODO prune previous height/round
}

func (rm *RequestManager) setRound(round int32) {
	rm.mtx.Lock()
	defer rm.mtx.Unlock()
	rm.round = round
	// TODO prune previous height/round
}
