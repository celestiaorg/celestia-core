package cat

import (
	"container/list"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// servedPerPeerLimit bounds how many served tx keys are remembered per peer.
// It must be at least the data channel's send queue capacity: every reply
// still queued is then among the last servedPerPeerLimit entries and is never
// evicted, so the queue can't hold two copies of one tx.
const servedPerPeerLimit = 1000

// servedTxs tracks, per peer, the txs sent in reply to a WantTx and how many
// messages were queued on the data channel, so a repeat WantTx is answered
// only once the previous copy has left the peer's send queue.
//
// Every data-channel send to a tracked peer must go through Send or Serve.
// They hold the peer's lock across the enqueue and its bookkeeping, so the
// recorded positions match the queue's FIFO order.
type servedTxs struct {
	mtx    tmsync.Mutex // guards byPeer
	byPeer map[uint16]*servedSet
	limit  int
}

// servedSet is one peer's state.
type servedSet struct {
	// mtx serializes enqueues to the peer with the fields below. It is never
	// held together with servedTxs.mtx.
	mtx tmsync.Mutex
	// queued counts messages this reactor queued on the peer's data channel.
	queued uint64
	// order holds *servedEntry, oldest first; keys indexes it by tx key.
	order *list.List
	keys  map[types.TxKey]*list.Element
}

type servedEntry struct {
	key types.TxKey
	// pos is the value of queued right after the reply was queued.
	pos uint64
}

func newServedTxs() *servedTxs {
	return &servedTxs{
		byPeer: make(map[uint16]*servedSet),
		limit:  servedPerPeerLimit,
	}
}

// InitPeer starts tracking peer with an empty send queue, resetting any
// previous state for that ID.
func (s *servedTxs) InitPeer(peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.byPeer[peer] = &servedSet{order: list.New(), keys: make(map[types.TxKey]*list.Element)}
}

// RemovePeer forgets peer.
func (s *servedTxs) RemovePeer(peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.byPeer, peer)
}

func (s *servedTxs) get(peer uint16) *servedSet {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.byPeer[peer]
}

// Send calls send, which must enqueue one message on peer's data channel
// without blocking, and counts the message if send returns true.
func (s *servedTxs) Send(peer uint16, send func() bool) bool {
	set := s.get(peer)
	if set == nil {
		return send()
	}
	set.mtx.Lock()
	defer set.mtx.Unlock()
	if !send() {
		return false
	}
	set.queued++
	return true
}

// Serve is Send for a reply carrying txKey; on success it also records the
// reply's position in the queue.
func (s *servedTxs) Serve(peer uint16, txKey types.TxKey, send func() bool) bool {
	set := s.get(peer)
	if set == nil {
		return send()
	}
	set.mtx.Lock()
	defer set.mtx.Unlock()
	if !send() {
		return false
	}
	set.queued++
	if el, has := set.keys[txKey]; has {
		set.order.Remove(el)
	}
	for set.order.Len() >= s.limit {
		front := set.order.Front()
		delete(set.keys, front.Value.(*servedEntry).key)
		set.order.Remove(front)
	}
	set.keys[txKey] = set.order.PushBack(&servedEntry{key: txKey, pos: set.queued})
	return true
}

// IsQueued reports whether a reply with txKey may still be in peer's send
// queue. queueSizeFn returns the number of messages currently pending on the
// peer's data channel; it is called only if txKey was served to peer.
func (s *servedTxs) IsQueued(peer uint16, txKey types.TxKey, queueSizeFn func() int) bool {
	set := s.get(peer)
	if set == nil {
		return false
	}
	set.mtx.Lock()
	defer set.mtx.Unlock()

	el, has := set.keys[txKey]
	if !has {
		return false
	}
	// Messages leave the queue in FIFO order and positions are recorded in
	// that order, so the first queued-queueSize of them have been sent.
	queueSize := queueSizeFn()
	if queueSize < 0 {
		queueSize = 0
	}
	if uint64(queueSize) >= set.queued {
		return true
	}
	sent := set.queued - uint64(queueSize)
	return el.Value.(*servedEntry).pos > sent
}

// dataQueueSizeFn returns a function reporting the number of messages
// pending on peer's data channel.
func dataQueueSizeFn(peer p2p.Peer) func() int {
	return func() int { return dataQueueSize(peer) }
}

func dataQueueSize(peer p2p.Peer) int {
	for _, ch := range peer.Status().Channels {
		if ch.ID == MempoolDataChannel {
			return ch.SendQueueSize
		}
	}
	return 0
}
