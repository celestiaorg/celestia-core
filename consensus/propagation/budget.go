package propagation

import (
	"sync"
	"time"
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
