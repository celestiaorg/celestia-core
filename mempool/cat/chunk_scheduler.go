package cat

import (
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

// chunkRequestKey identifies an outstanding request for a single chunk of a
// single large tx.
type chunkRequestKey struct {
	txKey types.TxKey
	index uint32
}

type chunkRequest struct {
	peer   uint16
	timer  *time.Timer
	sentAt time.Time
}

// chunkRequestScheduler tracks outstanding chunk requests. Unlike
// requestScheduler (one peer per tx), it supports multiple peers per tx and
// multiple chunk indexes per peer, with a short per-chunk timeout that triggers
// a re-request from an alternate peer.
type chunkRequestScheduler struct {
	mtx          sync.Mutex
	chunkTimeout time.Duration
	requests     map[chunkRequestKey]*chunkRequest
	closed       bool
}

func newChunkRequestScheduler(chunkTimeout time.Duration) *chunkRequestScheduler {
	return &chunkRequestScheduler{
		chunkTimeout: chunkTimeout,
		requests:     make(map[chunkRequestKey]*chunkRequest),
	}
}

// Add registers a request for (txKey, index) to peer. It returns false if a
// request for that chunk is already outstanding or the scheduler is closed.
// onTimeout fires if the chunk is not received within chunkTimeout; the entry is
// removed before the callback runs so the callback may re-Add to another peer.
func (c *chunkRequestScheduler) Add(txKey types.TxKey, index uint32, peer uint16, onTimeout func(txKey types.TxKey, index uint32, peer uint16)) bool {
	if peer == 0 {
		return false
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.closed {
		return false
	}
	k := chunkRequestKey{txKey: txKey, index: index}
	if _, ok := c.requests[k]; ok {
		return false
	}
	timer := time.AfterFunc(c.chunkTimeout, func() {
		c.mtx.Lock()
		if cur, ok := c.requests[k]; ok && cur.peer == peer {
			delete(c.requests, k)
		}
		c.mtx.Unlock()
		if onTimeout != nil {
			onTimeout(txKey, index, peer)
		}
	})
	c.requests[k] = &chunkRequest{peer: peer, timer: timer, sentAt: time.Now()}
	return true
}

// MarkReceived stops the timer and clears the request for (txKey, index). It
// returns the peer the chunk was requested from and the time the request was
// sent (for latency measurement), or ok=false if there was no outstanding
// request (e.g. an unsolicited or already-served chunk).
func (c *chunkRequestScheduler) MarkReceived(txKey types.TxKey, index uint32) (peer uint16, sentAt time.Time, ok bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	k := chunkRequestKey{txKey: txKey, index: index}
	req, found := c.requests[k]
	if !found {
		return 0, time.Time{}, false
	}
	req.timer.Stop()
	delete(c.requests, k)
	return req.peer, req.sentAt, true
}

// Has reports whether a request for (txKey, index) is outstanding.
func (c *chunkRequestScheduler) Has(txKey types.TxKey, index uint32) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	_, ok := c.requests[chunkRequestKey{txKey: txKey, index: index}]
	return ok
}

// ClearTx stops and removes all outstanding chunk requests for a tx (on
// completion or abandonment).
func (c *chunkRequestScheduler) ClearTx(txKey types.TxKey) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for k, req := range c.requests {
		if k.txKey == txKey {
			req.timer.Stop()
			delete(c.requests, k)
		}
	}
}

// ClearPeer stops and removes all outstanding chunk requests to a peer (on
// disconnect) and returns the affected chunk keys so they can be re-requested.
func (c *chunkRequestScheduler) ClearPeer(peer uint16) []chunkRequestKey {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	var affected []chunkRequestKey
	for k, req := range c.requests {
		if req.peer == peer {
			req.timer.Stop()
			delete(c.requests, k)
			affected = append(affected, k)
		}
	}
	return affected
}

// Close stops all timers. Add must not be called after Close.
func (c *chunkRequestScheduler) Close() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.closed = true
	for k, req := range c.requests {
		req.timer.Stop()
		delete(c.requests, k)
	}
}
