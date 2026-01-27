package cat

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

const defaultGlobalRequestTimeout = 1 * time.Hour

// requestScheduler tracks the lifecycle of outbound transaction requests.
type requestScheduler struct {
	mtx sync.Mutex

	// responseTime is the time the scheduler
	// waits for a response from a peer before
	// invoking the callback
	responseTime time.Duration

	// globalTimeout represents the longest duration
	// to wait for any late response (after the reponseTime).
	// After this period the request is garbage collected.
	globalTimeout time.Duration

	// requestsByPeer is a lookup table of requests by peer.
	// Multiple tranasctions can be requested by a single peer at one
	requestsByPeer map[uint16]requestSet

	// requestsByTx is a lookup table for requested txs.
	// There can only be one request per tx.
	requestsByTx map[types.TxKey]uint16

	// requestsBySequence is a lookup table for requested sequences by signer.
	requestsBySequence map[string]map[uint64]uint16
}

type requestSet map[types.TxKey]*requestInfo

type requestInfo struct {
	timer    *time.Timer
	signer   []byte
	sequence uint64
}

func newRequestScheduler(responseTime, globalTimeout time.Duration) *requestScheduler {
	return &requestScheduler{
		responseTime:       responseTime,
		globalTimeout:      globalTimeout,
		requestsByPeer:     make(map[uint16]requestSet),
		requestsByTx:       make(map[types.TxKey]uint16),
		requestsBySequence: make(map[string]map[uint64]uint16),
	}
}

func sequenceRequestKey(signer []byte, sequence uint64) types.TxKey {
	h := sha256.New()
	h.Write([]byte("seqreq:"))
	h.Write(signer)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], sequence)
	h.Write(buf[:])
	sum := h.Sum(nil)
	var key types.TxKey
	copy(key[:], sum)
	return key
}

func (r *requestScheduler) Add(key types.TxKey, signer []byte, sequence uint64, peer uint16, onTimeout func(key types.TxKey, signer []byte, sequence uint64, peer uint16)) bool {
	if peer == 0 {
		return false
	}
	r.mtx.Lock()
	defer r.mtx.Unlock()

	isSequenceRequest := len(signer) > 0 && sequence > 0
	isZeroKey := key == (types.TxKey{})

	// not allowed to have more than one outgoing transaction at once
	if !isZeroKey {
		if _, ok := r.requestsByTx[key]; ok {
			return false
		}
	}
	if isSequenceRequest {
		if _, ok := r.requestsBySequence[string(signer)][sequence]; ok {
			return false
		}
	}

	requestKey := key
	if isZeroKey {
		if !isSequenceRequest {
			return false
		}
		requestKey = sequenceRequestKey(signer, sequence)
	}

	timer := time.AfterFunc(r.responseTime, func() {
		r.mtx.Lock()
		if !isZeroKey {
			delete(r.requestsByTx, key)
		}
		if isSequenceRequest {
			delete(r.requestsBySequence[string(signer)], sequence)
		}
		r.mtx.Unlock()

		// trigger callback. Callback can `Add` the tx back to the scheduler
		if onTimeout != nil {
			onTimeout(key, signer, sequence, peer)
		}

		// We set another timeout because the peer could still send
		// a late response after the first timeout and it's important
		// to recognize that it is a transaction in response to a
		// request and not a new transaction being broadcasted to the entire
		// network. This timer cannot be stopped and is used to ensure
		// garbage collection.
		time.AfterFunc(r.globalTimeout, func() {
			r.mtx.Lock()
			defer r.mtx.Unlock()
			delete(r.requestsByPeer[peer], requestKey)
		})
	})

	info := &requestInfo{
		timer:    timer,
		signer:   append([]byte(nil), signer...),
		sequence: sequence,
	}

	if _, ok := r.requestsByPeer[peer]; !ok {
		r.requestsByPeer[peer] = requestSet{requestKey: info}
	} else {
		r.requestsByPeer[peer][requestKey] = info
	}
	if !isZeroKey {
		r.requestsByTx[key] = peer
	}
	if isSequenceRequest {
		if _, ok := r.requestsBySequence[string(signer)]; !ok {
			r.requestsBySequence[string(signer)] = make(map[uint64]uint16)
		}
		r.requestsBySequence[string(signer)][sequence] = peer
	}
	return true
}

func (r *requestScheduler) ForTx(key types.TxKey) uint16 {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	return r.requestsByTx[key]
}

func (r *requestScheduler) ForSignerSequence(signer []byte, sequence uint64) uint16 {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	return r.requestsBySequence[string(signer)][sequence]
}

// ForTxRoute returns the peer ID for a txKey request or a signer+sequence request.
func (r *requestScheduler) ForTxRoute(isUpgraded bool, key types.TxKey, signer []byte, sequence uint64) uint16 {
	if !isUpgraded {
		return r.ForTx(key)
	}
	return r.ForSignerSequence(signer, sequence)
}

func (r *requestScheduler) Has(peer uint16, key types.TxKey) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	requestSet, ok := r.requestsByPeer[peer]
	if !ok {
		return false
	}
	_, ok = requestSet[key]
	return ok
}

// HasBySequence checks if a request for a specific (peer, signer, sequence) exists.
func (r *requestScheduler) HasBySequence(peer uint16, signer []byte, sequence uint64) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	signerKey := string(signer)
	seqMap, ok := r.requestsBySequence[signerKey]
	if !ok {
		return false
	}
	requestedPeer, ok := seqMap[sequence]
	return ok && requestedPeer == peer
}

func (r *requestScheduler) HasRoute(isUpgraded bool, peer uint16, key types.TxKey, signer []byte, sequence uint64) bool {
	if isUpgraded {
		return r.HasBySequence(peer, signer, sequence)
	}
	return r.Has(peer, key)
}

// CountForPeer returns the number of active requests to a specific peer.
func (r *requestScheduler) CountForPeer(peer uint16) int {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	return len(r.requestsByPeer[peer])
}

func (r *requestScheduler) ClearAllRequestsFrom(peer uint16) requestSet {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	requests, ok := r.requestsByPeer[peer]
	if !ok {
		return requestSet{}
	}
	for tx, info := range requests {
		info.timer.Stop()
		// Clean up sequence tracking
		if len(info.signer) > 0 && info.sequence > 0 {
			delete(r.requestsBySequence[string(info.signer)], info.sequence)
		} else {
			delete(r.requestsByTx, tx)
		}
	}
	delete(r.requestsByPeer, peer)
	return requests
}

func (r *requestScheduler) MarkReceived(peer uint16, key types.TxKey) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	if _, ok := r.requestsByPeer[peer]; !ok {
		return false
	}

	if timer, ok := r.requestsByPeer[peer][key]; ok {
		timer.timer.Stop()
	} else {
		return false
	}

	delete(r.requestsByPeer[peer], key)
	delete(r.requestsByTx, key)
	return true
}

func (r *requestScheduler) MarkReceivedRoute(isUpgraded bool, peer uint16, key types.TxKey, signer []byte, sequence uint64) bool {
	if isUpgraded {
		return r.MarkReceivedBySequence(peer, signer, sequence)
	}
	return r.MarkReceived(peer, key)
}

func (r *requestScheduler) MarkReceivedBySequence(peer uint16, signer []byte, sequence uint64) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	requestSet, ok := r.requestsByPeer[peer]
	if !ok {
		return false
	}
	for reqKey, info := range requestSet {
		if info.sequence == sequence && string(info.signer) == string(signer) {
			info.timer.Stop()
			delete(requestSet, reqKey)
			delete(r.requestsBySequence[string(signer)], sequence)
			return true
		}
	}
	return false
}

// Close stops all timers and clears all requests.
// Add should never be called after `Close`.
func (r *requestScheduler) Close() {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	for _, requestSet := range r.requestsByPeer {
		for _, info := range requestSet {
			info.timer.Stop()
		}
	}
}

// RequestStats holds statistics about active requests
type RequestStats struct {
	TotalByTx       int // requests tracked by txKey
	TotalBySequence int // requests tracked by (signer, sequence)
	ForPeer         int // requests to a specific peer
	ForSigner       int // requests for a specific signer
}

// Stats returns current request statistics
func (r *requestScheduler) Stats(peer uint16, signer []byte) RequestStats {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	stats := RequestStats{
		TotalByTx: len(r.requestsByTx),
	}

	// Count total sequence-based requests
	for _, seqMap := range r.requestsBySequence {
		stats.TotalBySequence += len(seqMap)
	}

	// Count for specific peer
	if peer != 0 {
		stats.ForPeer = len(r.requestsByPeer[peer])
	}

	// Count for specific signer
	if len(signer) > 0 {
		stats.ForSigner = len(r.requestsBySequence[string(signer)])
	}

	return stats
}

// MaxSequenceForSigner returns the maximum requested sequence for a signer.
func (r *requestScheduler) MaxSequenceForSigner(signer []byte) uint64 {
	if len(signer) == 0 {
		return 0
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()

	seqMap := r.requestsBySequence[string(signer)]
	if len(seqMap) == 0 {
		return 0
	}

	var maxSeq uint64
	for seq := range seqMap {
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq
}
