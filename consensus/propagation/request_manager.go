package propagation

import (
	"context"
	"fmt"
	"github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/p2p"
	"time"
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

type RequestManager struct {
	ctx       context.Context
	mtx       sync.RWMutex
	logger    log.Logger
	peerState map[p2p.ID]*PeerState
	*ProposalCache
	haveChan        <-chan HaveWithFrom
	CommitmentChan  <-chan types.CompactBlock
	expiredWantChan chan *sentWant
	height          int64
	round           int32
	sentWants       map[p2p.ID][]*sentWant
	fetcher         *partFetcher
}

func NewRequestsManager(ctx context.Context, peerState map[p2p.ID]*PeerState, proposalCache *ProposalCache, haveChan <-chan HaveWithFrom, compactBlockChan <-chan types.CompactBlock) *RequestManager {
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
	}
}

func (pf *RequestManager) WithLogger(logger log.Logger) {
	pf.logger = logger
	pf.fetcher.logger = logger
}

func (pf *RequestManager) Start() {
	tickerDuration := 6 * time.Second
	ticker := time.NewTicker(tickerDuration)
	for {
		select {
		case <-pf.ctx.Done():
			// TODO refactor all the cases into methods
			return
		case <-ticker.C:
			// This case should help advance the propagation in the following case:
			// - We receive haves for some parts
			// - The peer that sent us the haves is almost saturated with requests
			// - We request some wants, but the remaining are not requested
			// - no other peer send us haves for those parts, and we don't advance to a new height
			// due to multiple rounds of consensus.
			// Note: the ticker is reset with every received have to avoid triggering it
			// unnecessarily.
		case expiredWant, has := <-pf.expiredWantChan:
			if !has {
				// channel was closed
				return
			}
			pf.mtx.RLock()
			height, round := pf.height, pf.round
			pf.mtx.RUnlock()
			if expiredWant.Height != height || expiredWant.Round != round {
				// this expired want is for a previous height/round, we can ignore it.
				continue
			}
			_, parts, _, has := pf.getAllState(expiredWant.Height, expiredWant.Round, false)
			if !has {
				// this shouldn't happen
				pf.logger.Error("couldn't find state for proposal", "height", expiredWant.Height, "round", expiredWant.Round)
				// TODO maybe return?
				continue
			}
			if parts.IsComplete() {
				continue
			}
			missing := parts.MissingOriginal()
			notMissed := missing.Not()
			toRequest := expiredWant.Parts.Sub(notMissed)
			for !toRequest.IsEmpty() {
				peers := shuffle(pf.getPeers())
				to := peers[0].peer
				pendingWant := types.WantParts{
					Parts:  toRequest,
					Height: expiredWant.Height,
					Round:  expiredWant.Round,
					Prove:  expiredWant.Prove,
				}
				remaining, err := pf.fetcher.sendRequest(to, &pendingWant)
				if err != nil {
					pf.logger.Error("failed to send want parts", "peer", peers[0].peer.ID(), "height", expiredWant.Height, "round", expiredWant.Round)
					continue
				}
				toRequest = toRequest.Sub(remaining)
				pf.sentWants[to.ID()] = append(pf.sentWants[to.ID()], &sentWant{
					WantParts: &pendingWant,
					timestamp: time.Now(),
					to:        to.ID(),
				})
			}

		case have, has := <-pf.haveChan:
			if !has {
				// channel was closed
				return
			}
			pf.mtx.RLock()
			height, round := pf.height, pf.round
			pf.mtx.RUnlock()
			if have.Height != height || have.Round != round {
				// this shouldn't happen as we should only receive have parts for the current height and round
				continue
			}
			_, parts, _, has := pf.getAllState(height, round, false)
			if !has {
				// this shouldn't happen
				pf.logger.Error("couldn't find state for proposal", "height", height, "round", round)
				// TODO maybe return?
				continue
			}
			ticker.Reset(tickerDuration)
			hc := have.BitArray(int(parts.Total()))
			hc = hc.Sub(parts.BitArray())
			if hc.IsEmpty() {
				// we have all the parts that this peer has
				continue
			}
			// TODO check for peer remaining available requests
			to := pf.peerState[have.from].peer
			want := types.WantParts{
				Parts:  hc,
				Height: have.Height,
				Round:  have.Round,
				Prove:  false,
			}
			remaining, err := pf.fetcher.sendRequest(to, &want)
			if err != nil {
				pf.logger.Error("failed to send want parts", "peer", have.from, "height", have.Height, "round", have.Round)
				continue
			}
			sent := hc.Sub(remaining)
			want.Parts = sent
			pf.sentWants[to.ID()] = append(pf.sentWants[to.ID()], &sentWant{
				WantParts: &want,
				timestamp: time.Now(),
				to:        have.from,
			})
		case compactBlock, has := <-pf.CommitmentChan:
			if !has {
				// channel was closed
				return
			}
			pf.mtx.RLock()
			// TODO find a way to increment these once we have all the data for a height/round
			// maybe do it in the data sync routine.
			height, round := pf.height, pf.round
			pf.mtx.RUnlock()
			if height == compactBlock.Proposal.Height && round == compactBlock.Proposal.Round {
				// we're already at the new height/round, we can wait for haves.
				continue
			}
			_, parts, _, has := pf.getAllState(height, round, false)
			if !has {
				// this shouldn't happen
				pf.logger.Error("couldn't find state for proposal", "height", height, "round", round)
				// TODO maybe return?
				continue
			}
			if parts.IsComplete() {
				continue
			}
			missing := parts.MissingOriginal()
			// TODO maybe shuffle peers on every run
			for _, peer := range shuffle(pf.getPeers()) {
				if missing.IsEmpty() {
					break
				}

				// if this peer is at a higher height, we can request everything from them that we didn't request
				if peer.latestHeight > height || (peer.latestHeight == height && peer.latestRound > round) {
					// TODO check if we really need to keep track of the sent/recived wants etc
					want := types.WantParts{
						Parts:  missing,
						Height: height,
						Round:  round,
						Prove:  true,
					}
					remaining, err := pf.fetcher.sendRequest(peer.peer, &want)
					if err != nil {
						pf.logger.Error("failed to send catchup want parts", "peer", peer.peer.ID(), "height", height, "round", round, "err", err)
						continue
					}
					missing = missing.Sub(remaining)
					pf.sentWants[peer.peer.ID()] = append(pf.sentWants[peer.peer.ID()], &sentWant{
						WantParts: &want,
						timestamp: time.Now(),
						to:        peer.peer.ID(),
					})
					continue
				}

				peerHaves, has := peer.GetReceivedHaves(height, round)
				if !has {
					// TODO maybe log something
					continue
				}
				notMissed := missing.Not()
				toRequest := peerHaves.Sub(notMissed)
				want := types.WantParts{
					Parts:  toRequest,
					Height: height,
					Round:  round,
					Prove:  true,
				}
				remaining, err := pf.fetcher.sendRequest(peer.peer, &want)
				if err != nil {
					pf.logger.Error("failed to send catchup want parts", "peer", peer.peer.ID(), "height", height, "round", round, "err", err)
					continue
				}
				missing = missing.Sub(remaining)
				pf.sentWants[peer.peer.ID()] = append(pf.sentWants[peer.peer.ID()], &sentWant{
					WantParts: &want,
					timestamp: time.Now(),
					to:        peer.peer.ID(),
				})
			}
		}
	}
}

func (pf *RequestManager) Stop() {

}

// getPeers returns a list of all peers that the requests manager is aware of.
func (pf *RequestManager) getPeers() []*PeerState {
	pf.mtx.RLock()
	defer pf.mtx.RUnlock()
	peers := make([]*PeerState, 0, len(pf.peerState))
	for _, peer := range pf.peerState {
		peers = append(peers, peer)
	}
	return peers
}

// prune takes a height and a round and removes them from the peer state.
// also, removes that height and round from the sent wants and total sent wants.
func (pf *RequestManager) prune(height int64, round int32) {
	// TODO also call the partFetcher.prune
}

// expireWants TODO call it from the start function.
func (pf *RequestManager) expireWants() {
	// TODO maybe also expire old unanswered wants if we start receiving data from the peer.
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-pf.ctx.Done():
			return
		case <-ticker.C:
			for peerID, wants := range pf.sentWants {
				for index, want := range wants {
					if time.Now().Sub(want.timestamp) >= 2*time.Second {
						err := pf.removeExpiredWant(peerID, index)
						if err != nil {
							pf.logger.Error("failed to remove expired want", "peer", peerID, "index", index, "err", err)
							continue
						}
						pf.expiredWantChan <- want
					}
				}
			}
		}
	}
}

func (pf *RequestManager) removeExpiredWant(id p2p.ID, index int) error {
	peerRequests, has := pf.sentWants[id]
	if !has {
		return fmt.Errorf("peer %s has no sent wants", id)
	}
	if index >= len(peerRequests) || index < 0 {
		return fmt.Errorf("index %d is out of bounds %d", index, len(peerRequests))
	}
	if index+1 == len(peerRequests) {
		pf.sentWants[id] = pf.sentWants[id][:index]
		return nil
	}
	pf.sentWants[id] = append(pf.sentWants[id][:index], pf.sentWants[id][index+1:]...)
	return nil
}
