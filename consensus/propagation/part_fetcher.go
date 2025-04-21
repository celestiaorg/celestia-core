package propagation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/proto/tendermint/propagation"
)

const (
	// concurrentPerPeerRequestLimit the maximum number of requests to send a peer.
	concurrentPerPeerRequestLimit = 300

	// maxRequestsPerPart the maximum number of requests per parts.
	maxRequestsPerPart = 1

	// maxRequestRetry the maximum number of times to try to send a request
	maxRequestRetry = 3
)

type request struct {
	want       *proptypes.WantParts
	targetPeer p2p.Peer
}

// partFetcher manages want parts:
// - limits the number of concurrent requests per peer
// - limits the number of requests per part
type partFetcher struct {
	sync.Mutex
	perPeerRequests map[p2p.ID]int
	// perPartRequests a map of [height][round][partIndex]numberOfRequests
	perPartRequests map[int64]map[int32]map[int]int
	logger          log.Logger
	ctx             context.Context
}

func newPartFetcher(logger log.Logger) *partFetcher {
	return &partFetcher{
		perPeerRequests: make(map[p2p.ID]int),
		perPartRequests: make(map[int64]map[int32]map[int]int),
		logger:          logger,
		ctx:             context.Background(),
	}
}

func (r *partFetcher) sendRequest(targetPeer p2p.Peer, want *proptypes.WantParts) (*bits.BitArray, error) {
	r.Lock()
	defer r.Unlock()
	r.logger.Info("sending request", "peer", targetPeer, "height", want.Height, "round", want.Round, "part_count", want.Parts.Size(), "prove", want.Prove)
	// check per peer requests
	perPeerRequestCount, has := r.perPeerRequests[targetPeer.ID()]
	if !has {
		r.perPeerRequests[targetPeer.ID()] = 0
		perPeerRequestCount = 0
	}
	if perPeerRequestCount == concurrentPerPeerRequestLimit {
		r.logger.Info("concurrent request limit reached for peer", "peer", targetPeer, "count", perPeerRequestCount, "limit", concurrentPerPeerRequestLimit, "height", want.Height, "round", want.Round)
		return nil, fmt.Errorf("concurrent request limit reached for peer %s", targetPeer.ID())
	}
	// check per part requests
	toRequest := bits.NewBitArray(want.Parts.Size())
	toPostpone := bits.NewBitArray(want.Parts.Size())
	for _, indice := range want.Parts.GetTrueIndices() {
		perPartCount, has := r.perPartRequests[want.Height][want.Round][indice]
		if !has {
			r.initialisePerPartRequestsMap(want.Height, want.Round)
			r.perPartRequests[want.Height][want.Round][indice]++
			toRequest.SetIndex(indice, true)
			continue
		}
		if perPartCount == maxRequestsPerPart {
			toPostpone.SetIndex(indice, true)
			continue
		}
		r.perPartRequests[want.Height][want.Round][indice]++
		toRequest.SetIndex(indice, true)
	}

	if !toRequest.IsEmpty() {
		e := p2p.Envelope{
			ChannelID: WantChannel,
			Message: &propagation.WantParts{
				Parts:  *toRequest.ToProto(),
				Height: want.Height,
				Round:  want.Round,
				Prove:  want.Prove,
			},
		}
		r.logger.Info("sending want part", "peer", targetPeer, "height", want.Height, "round", want.Round, "part_count", toRequest.Size())
		if !p2p.TrySendEnvelopeShim(targetPeer, e, r.logger) { //nolint:staticcheck
			r.logger.Error("failed to send want part. retrying", "peer", targetPeer, "height", want.Height, "round", want.Round)
			err := r.retry(&e, targetPeer)
			if err != nil {
				return nil, err
			}
		}
		r.perPeerRequests[targetPeer.ID()]++
	}
	return toPostpone, nil
}

func (r *partFetcher) initialisePerPartRequestsMap(height int64, round int32) {
	if _, has := r.perPartRequests[height]; !has {
		r.perPartRequests[height] = make(map[int32]map[int]int)
		r.perPartRequests[height][round] = make(map[int]int)
		return
	}

	if _, has := r.perPartRequests[height][round]; !has {
		r.perPartRequests[height][round] = make(map[int]int)
		return
	}
}

// retry takes an envelope and a target peer and retries to send it.
// TODO check whether we want to retry other messages to put this method in a separate place.
func (r *partFetcher) retry(e *p2p.Envelope, targetPeer p2p.Peer) error {
	for i := 1; i <= maxRequestRetry; i++ {
		sleepTime := rand.Uint64()%500 + 100
		ticker := time.NewTicker(time.Duration(sleepTime) * time.Millisecond)
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-ticker.C:
			if p2p.TrySendEnvelopeShim(targetPeer, *e, r.logger) { //nolint:staticcheck
				return nil
			}
			r.logger.Error("failed to send want part. retrying", "peer", targetPeer, "retry_count", i)
		}
	}
	return errors.New("max retry exceeded for part")
}

// pruneRound removes a round from the per part requests map.
// TODO call from requests manager
func (r *partFetcher) pruneRound(height int64, round int32) {
	r.Lock()
	defer r.Unlock()
	delete(r.perPartRequests[height], round)
}

// pruneHeight removes a height from the per part requests map.
// TODO call from requests manager
func (r *partFetcher) pruneHeight(height int64) {
	r.Lock()
	defer r.Unlock()
	delete(r.perPartRequests, height)
}
