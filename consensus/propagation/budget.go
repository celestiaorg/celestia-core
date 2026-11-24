package propagation

import (
	"sync"
	"time"

	"github.com/cometbft/cometbft/p2p"
)

// tokenBucket is a thread-safe token bucket rate limiter. It holds at most
// burst tokens and refills at rate tokens per second.
type tokenBucket struct {
	mtx    sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{
		rate:   rate,
		burst:  burst,
		tokens: burst,
		last:   time.Now(),
		now:    time.Now,
	}
}

// newTokenBucketWithTokens returns a token bucket that starts with tokens
// tokens, at most burst.
func newTokenBucketWithTokens(rate, burst, tokens float64) *tokenBucket {
	b := newTokenBucket(rate, burst)
	b.tokens = min(tokens, burst)
	return b
}

// allow takes one token and reports whether one was available.
func (b *tokenBucket) allow() bool {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	now := b.now()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// recentPeers records which peers asked for something within window.
type recentPeers struct {
	mtx    sync.Mutex
	window time.Duration
	seen   map[p2p.ID]time.Time
	now    func() time.Time
}

func newRecentPeers(window time.Duration) *recentPeers {
	return &recentPeers{window: window, seen: make(map[p2p.ID]time.Time), now: time.Now}
}

// touch records that peer asked now and reports whether another peer asked
// within the window. Entries older than the window are dropped, so the map
// holds at most the peers that asked within the window.
func (r *recentPeers) touch(peer p2p.ID) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	now := r.now()
	r.seen[peer] = now
	for id, at := range r.seen {
		if now.Sub(at) > r.window {
			delete(r.seen, id)
		}
	}
	return len(r.seen) > 1
}
