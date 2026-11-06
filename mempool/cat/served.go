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
type servedTxs struct {
	mtx    tmsync.Mutex
	byPeer map[uint16]*servedSet
	limit  int
}

// servedSet is one peer's state.
type servedSet struct {
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

// MarkQueued records that a message was queued on peer's data channel.
func (s *servedTxs) MarkQueued(peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if set, ok := s.byPeer[peer]; ok {
		set.queued++
	}
}

// MarkServed records that txKey was queued for peer in reply to a WantTx.
// It also counts the message as queued.
func (s *servedTxs) MarkServed(peer uint16, txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	set, ok := s.byPeer[peer]
	if !ok {
		return
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
}

// IsQueued reports whether a reply with txKey may still be in peer's send
// queue. queueSizeFn returns the number of messages currently pending on the
// peer's data channel; it is called only if txKey was served to peer.
func (s *servedTxs) IsQueued(peer uint16, txKey types.TxKey, queueSizeFn func() int) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	set, ok := s.byPeer[peer]
	if !ok {
		return false
	}
	el, has := set.keys[txKey]
	if !has {
		return false
	}
	// Messages leave the queue in order, so the first queued-queueSize of
	// them have been sent. Undercounting queued only makes this more
	// conservative.
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
