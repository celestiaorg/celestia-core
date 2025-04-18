package propagation

import (
	"context"
	"fmt"
	"github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/proto/tendermint/propagation"
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
	haveChan       <-chan HaveWithFrom
	CommitmentChan <-chan types.CompactBlock
	expiredWant    chan *sentWant
	height         int64
	round          int32
	sentWants      map[p2p.ID][]sentWant
	totalSentWants map[int64]map[int32]*bits.BitArray
	fetcher        *partFetcher
}

func NewRequestsManager(ctx context.Context, peerState map[p2p.ID]*PeerState, proposalCache *ProposalCache, haveChan <-chan HaveWithFrom, compactBlockChan <-chan types.CompactBlock) *RequestManager {
	return &RequestManager{
		ctx:            ctx,
		mtx:            sync.RWMutex{},
		peerState:      peerState,
		ProposalCache:  proposalCache,
		logger:         log.NewNopLogger(),
		haveChan:       haveChan,
		CommitmentChan: compactBlockChan,
		sentWants:      make(map[p2p.ID][]sentWant),
		totalSentWants: make(map[int64]map[int32]*bits.BitArray),
		expiredWant:    make(chan *sentWant, 100),
		fetcher:        newPartFetcher(log.NewNopLogger()),
	}
}

func (pf *RequestManager) WithLogger(logger log.Logger) {
	pf.logger = logger
	pf.fetcher.logger = logger
}

func (pf *RequestManager) Start() {
	ticker := time.NewTicker(6 * time.Second)
	for {
		select {
		case <-pf.ctx.Done():
			// TODO refactor all the cases into methods
			return
		case <-ticker.C:
			// TODO I Don't think we need this case: We do since the commitment is only received
			// once we advance to a new height. If we didn't, we need to trigger this. But we need to extend the timer everytime we receive something: Im not sure what it is.
		case want, has := <-pf.expiredWant:
			if !has {
				// channel was closed
				return
			}
			_, parts, _, has := pf.getAllState(want.Height, want.Round, false)
			if !has {
				// this shouldn't happen
				pf.logger.Error("couldn't find state for proposal", "height", want.Height, "round", want.Round)
				// TODO maybe return?
				continue
			}
			if parts.IsComplete() {
				continue
			}
			missing := parts.MissingOriginal()
			notMissed := missing.Not()
			toRequest := want.Parts.Sub(notMissed)
			for !toRequest.IsEmpty() {
				peers := shuffle(pf.getPeers())
				to := peers[0].peer
				pendingWant := types.WantParts{
					Parts:  toRequest,
					Height: want.Height,
					Round:  want.Round,
					Prove:  want.Prove,
				}
				remaining, err := pf.fetcher.sendRequest(to, &pendingWant)
				if err != nil {
					pf.logger.Error("failed to send want parts", "peer", peers[0].peer.ID(), "height", want.Height, "round", want.Round)
					continue
				}
				toRequest = toRequest.Sub(remaining)
				pf.sentWants[to.ID()] = append(pf.sentWants[to.ID()], sentWant{
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
			hc := have.BitArray(int(parts.Total()))
			hc = hc.Sub(parts.BitArray())
			if hc.IsEmpty() {
				// we have all the parts that this peer has
				continue
			}
			// TODO check for peer remaining available requests
			wantsBA = hc
			to = have.from
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
			for _, peer := range pf.peerState {
				if missing.IsEmpty() {
					break
				}

				// if this peer is at a higher height, we can request everything from them that we didn't request
				if peer.latestHeight > height || (peer.latestHeight == height && peer.latestRound > round) {
					remainingRequestsForPeer := pf.peerRemainingRequests(peer.peer.ID())
					if remainingRequestsForPeer == 0 {
						// we already sent the maximum number of requests to this peer, we can't send more.
						continue
					}
					toRequest := missing.Copy()
					// TODO check if we really need to keep track of the sent/recived wants etc
					peerSentWants, has := peer.GetSentWants(height, round)
					if has {
						toRequest = toRequest.Sub(peerSentWants)
					}
					for i := 0; i < len(toRequest.GetTrueIndices())-remainingRequestsForPeer; i++ {
						toRequest.SetIndex(toRequest.GetTrueIndices()[i], false)
					}
					e := p2p.Envelope{
						ChannelID: WantChannel,
						Message: &propagation.WantParts{
							Parts:  *toRequest.ToProto(),
							Height: height,
							Round:  round,
							Prove:  true,
						},
					}
					if !p2p.TrySendEnvelopeShim(peer.peer, e, pf.logger) { //nolint:staticcheck
						pf.logger.Error("failed to send want parts", "peer", peer.peer.ID(), "height", height, "round", round)
					} else {
						missing = missing.Sub(toRequest)
						peerSentWants.AddBitArray(toRequest)
						// TODO add the sent requests to the total sent wants
					}
					continue
				}

				peerHaves, has := peer.GetReceivedHaves(height, round)
				if !has {
					// This shouldn't happen
					// TODO maybe log something
					continue
				}
				notMissed := missing.Not()
				toRequest := peerHaves.Sub(notMissed)
				// TODO the next part can be refactored as it's copy pasted
				remainingRequestsForPeer := pf.peerRemainingRequests(peer.peer.ID())
				if remainingRequestsForPeer == 0 {
					// we already sent the maximum number of requests to this peer, we can't send more.
					continue
				}
				peerSentWants, has := peer.GetSentWants(height, round)
				if has {
					toRequest = toRequest.Sub(peerSentWants)
				}
				for i := 0; i < len(toRequest.GetTrueIndices())-remainingRequestsForPeer; i++ {
					toRequest.SetIndex(toRequest.GetTrueIndices()[i], false)
				}
				e := p2p.Envelope{
					ChannelID: WantChannel,
					Message: &propagation.WantParts{
						Parts:  *toRequest.ToProto(),
						Height: height,
						Round:  round,
						Prove:  true,
					},
				}
				if !p2p.TrySendEnvelopeShim(peer.peer, e, pf.logger) { //nolint:staticcheck
					pf.logger.Error("failed to send want parts", "peer", peer.peer.ID(), "height", height, "round", round)
				} else {
					missing = missing.Sub(toRequest)
					peerSentWants.AddBitArray(toRequest)
					// TODO add the sent requests to the total sent wants
				}
			}
		}
		// TODO move this one to each case. maybe even remove the head declarations
		pf.sendWants(wantsBA, to)
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
						pf.expiredWant <- want.want
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
