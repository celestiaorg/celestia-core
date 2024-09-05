package cat

import (
	"container/heap"

	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/types"
)

type priorityBroadcastQueue struct {
	queue      *txHeap
	incomingCh chan txTags
	readyCh    chan struct{}
}

type txTags struct {
	priority int64
	key      types.TxKey
	// if peer is nil, the transaction should be broadcast to all peers
	peer p2p.Peer
}

type txHeap []txTags

func (h txHeap) Len() int           { return len(h) }
func (h txHeap) Less(i, j int) bool { return h[i].priority > h[j].priority }
func (h txHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *txHeap) Push(x interface{}) {
	*h = append(*h, x.(txTags))
}

func (h *txHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func newPriorityBroadcastQueue() *priorityBroadcastQueue {
	q := &priorityBroadcastQueue{
		queue:      &txHeap{},
		incomingCh: make(chan txTags, 10),
		readyCh:    make(chan struct{}, 1),
	}
	heap.Init(q.queue)
	return q
}

// Add adds a new transaction to the priority broadcast queue. If the
// buffer is full, this will block but given that these transactions are
// constantly being looped over, this should not be a problem
func (q *priorityBroadcastQueue) BroadcastToPeer(priority int64, key types.TxKey, peer p2p.Peer) {
	q.incomingCh <- txTags{priority: priority, key: key, peer: peer}
	select {
	case q.readyCh <- struct{}{}:
	default:
	}
}

// BroadcastToAll adds a new transaction to the priority broadcast queue. If the
// buffer is full, this will block but given that these transactions are
// constantly being looped over, this should not be a problem
func (q *priorityBroadcastQueue) BroadcastToAll(priority int64, key types.TxKey) {
	q.incomingCh <- txTags{priority: priority, key: key, peer: nil}
	select {
	case q.readyCh <- struct{}{}:
	default:
	}
}

func (q *priorityBroadcastQueue) isReady() <-chan struct{} {
	return q.readyCh
}

func (q *priorityBroadcastQueue) processIncomingTxs() {
	for {
		select {
		case txTag, ok := <-q.incomingCh:
			if !ok {
				// Channel is closed, exit the loop
				return
			}
			heap.Push(q.queue, txTag)
		default:
			// No transactions available, exit the loop
			return
		}
	}
}

func (q *priorityBroadcastQueue) Pop() (types.TxKey, p2p.Peer) {
	if q.queue.Len() == 0 {
		return types.TxKey{}, nil
	}
	txTag := heap.Pop(q.queue).(txTags)
	return txTag.key, txTag.peer
}

// Reset clears the priority broadcast queue
func (q *priorityBroadcastQueue) Reset() {
	q.queue = &txHeap{}
	heap.Init(q.queue)

	// Clear any pending transactions in the incoming channel
	for len(q.incomingCh) > 0 {
		<-q.incomingCh
	}
}
