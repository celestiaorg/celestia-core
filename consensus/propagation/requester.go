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
	concurrentPerPeerRequestLimit = 40

	// maxRequestsPerPart the maximum number of requests per parts.
	maxRequestsPerPart = 40

	// maxNumberOfPendingRequests the maximum number of pending requests.
	maxNumberOfPendingRequests = 50_000

	// maxRequestRetry the maximum number of times to try to send a request
	maxRequestRetry = 10

	// requestTimeout request timeout after it's pending to be sent
	requestTimeout = 10 * time.Second
)

type request struct {
	want       *proptypes.WantParts
	targetPeer p2p.Peer
	timestamp  time.Time
}

// requester manages want parts:
// - limits the number of concurrent requests per peer
// - limits the number of requests per part
// - tracks expired requests
// TODO maybe rename to requestManager?
type requester struct {
	sync.Mutex
	pendingRequests []*request
	sentRequests    []*request
	perPeerRequests map[p2p.ID]int
	// perPartRequests a map of [height][round][partIndex]numberOfRequests
	perPartRequests map[int64]map[int32]map[int]int
	logger          log.Logger
	ctx             context.Context
}

func newRequester(logger log.Logger) *requester {
	requester := requester{
		perPeerRequests: make(map[p2p.ID]int),
		perPartRequests: make(map[int64]map[int32]map[int]int),
		pendingRequests: make([]*request, 0),
		sentRequests:    make([]*request, 0),
		logger:          logger,
		ctx:             context.Background(),
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		for {
			select {
			case <-requester.ctx.Done():
				return
			case <-ticker.C:
				requester.Lock()
				for i, req := range requester.sentRequests {
					if req.timestamp.Add(requestTimeout).Before(time.Now()) {
						err := requester.removeSentRequest(i)
						if err != nil {
							requester.logger.With("err", err).Error("failed to remove sent request")
							continue
						}
						requester.perPeerRequests[req.targetPeer.ID()]--
						for index := range req.want.Parts.GetTrueIndices() {
							requester.perPartRequests[req.want.Height][req.want.Round][index]--
						}
					}
				}
				requester.Unlock()
			}
		}
	}()
	return &requester
}

func (r *requester) sendRequest(targetPeer p2p.Peer, want *proptypes.WantParts) (bool, error) {
	r.Lock()
	defer r.Unlock()
	req := &request{
		want:       want,
		targetPeer: targetPeer,
		timestamp:  time.Now(),
	}
	// check per peer requests
	perPeerRequestCount, has := r.perPeerRequests[targetPeer.ID()]
	if !has {
		r.perPeerRequests[targetPeer.ID()] = 0
		perPeerRequestCount = 0
	}
	if perPeerRequestCount == concurrentPerPeerRequestLimit {
		return r.addPendingRequest(req)
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
		if perPartCount == -1 {
			// part already received, no need to re-request it
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
		if !p2p.TrySendEnvelopeShim(targetPeer, e, r.logger) { //nolint:staticcheck
			r.logger.Error("failed to send want part. retrying", "peer", targetPeer, "height", want.Height, "round", want.Round)
			err := r.retry(&e, targetPeer)
			if err != nil {
				return false, err
			}
		}
		r.sentRequests = append(r.sentRequests, req)
		r.perPeerRequests[targetPeer.ID()]++
	}
	if !toPostpone.IsEmpty() {
		added, err := r.addPendingRequest(&request{
			want: &proptypes.WantParts{
				Parts:  toPostpone,
				Height: want.Height,
				Round:  want.Round,
				Prove:  want.Prove,
			},
			targetPeer: targetPeer,
			timestamp:  time.Now(),
		})
		if err != nil {
			return false, err
		}
		if !added {
			return false, errors.New("failed to add pending request")
		}
	}
	return true, nil
}

func (r *requester) initialisePerPartRequestsMap(height int64, round int32) {
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

func (r *requester) receivedResponse(from p2p.Peer, part *proptypes.RecoveryPart) error {
	r.Lock()
	defer r.Unlock()
	perPeerRequestCount, has := r.perPeerRequests[from.ID()]
	if !has || perPeerRequestCount == 0 {
		// case where we received data we didn't request
		return fmt.Errorf("peer request count %v not found in requester", from)
	}
	r.perPeerRequests[from.ID()]--

	perPartCount, has := r.perPartRequests[part.Height][part.Round][int(part.Index)]
	if !has || perPartCount == 0 {
		// case where we received data we didn't request
		return fmt.Errorf("peer part request count %v not found in requester", from)
	}
	// set the per part request to -1 not to request it again
	r.perPartRequests[part.Height][part.Round][int(part.Index)] = -1

	go r.sendNextRequest(from)
	return nil
}

// retry takes an envelope and a target peer and retries to send it.
// TODO check whether we want to retry other messages to put this method in a separate place.
func (r *requester) retry(e *p2p.Envelope, targetPeer p2p.Peer) error {
	for i := 1; i <= maxRequestRetry; i++ {
		sleepTime := rand.Uint64()%5000 + 100
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

func (r *requester) addPendingRequest(req *request) (bool, error) {
	if len(r.pendingRequests) == maxNumberOfPendingRequests {
		return false, errors.New("too many pending requests")
	}
	r.pendingRequests = append(r.pendingRequests, req)
	return true, nil
}

// sendNextRequest sends the next pending request to that peer and also prunes expiring
// requests if it encounters one.
func (r *requester) sendNextRequest(from p2p.Peer) {
	r.Lock()
	defer r.Unlock()
	index := 0
	for index < len(r.pendingRequests) {
		req := r.pendingRequests[index]
		if !req.timestamp.Add(requestTimeout).After(time.Now()) {
			// remove expired request
			err := r.removePendingRequest(index)
			if err != nil {
				r.logger.Error("failed to remove expired request", "peer", from, "index", index, "err", err)
			}
			continue
		}
		if req.targetPeer.ID() == from.ID() {
			go func() {
				_, err := r.sendRequest(req.targetPeer, req.want)
				if err != nil {
					r.logger.Error("failed to send want part", "peer", req.targetPeer, "err", err)
				}
			}()
			// remove pending request from pending requests list
			err := r.removePendingRequest(index)
			if err != nil {
				r.logger.Error("failed to remove expired request", "peer", from, "index", index, "err", err)
			}
			return
		}
		index++
	}
}

func (r *requester) removePendingRequest(index int) error {
	if index >= len(r.pendingRequests) {
		return errors.New("request index out of pending requests range")
	}
	if index+1 == len(r.pendingRequests) {
		r.pendingRequests = r.pendingRequests[:index]
		return nil
	}
	r.pendingRequests = append(r.pendingRequests[:index], r.pendingRequests[index+1:]...)
	return nil
}

func (r *requester) removeSentRequest(index int) error {
	if index >= len(r.sentRequests) {
		return errors.New("request index out of pending requests range")
	}
	if index+1 == len(r.sentRequests) {
		r.sentRequests = r.sentRequests[:index]
		return nil
	}
	r.sentRequests = append(r.sentRequests[:index], r.sentRequests[index+1:]...)
	return nil
}
