package ed25519

import (
	"crypto/sha256"
	"sync"
)

// Successful ed25519 verifications are remembered by sha256(pubkey || sig ||
// msg). Verification is deterministic, so a remembered success for the exact
// same triple is the result a fresh verification would return. Failures are
// never cached. pubkey (32 bytes) and sig (64 bytes) have fixed sizes when the
// key is built, so the concatenation is unambiguous.
//
// The IBC 07-tendermint client checks the same commit signatures twice per
// MsgUpdateClient (trusting-period 1/3 check against the trusted validator set,
// then the 2/3 check against the new set), and relayers frequently submit the
// same header more than once, so many verifications are repeats.

// verifyCacheEnabled is only switched off by tests, to compare with stock.
var verifyCacheEnabled = true

// verifyCacheMax bounds each of the two generations, so the cache holds at most
// 2*verifyCacheMax keys (~25 MB in total).
const verifyCacheMax = 1 << 18

type verifyCache struct {
	mu        sync.Mutex
	limit     int
	cur, prev map[[32]byte]struct{}
}

func newVerifyCache(limit int) *verifyCache {
	return &verifyCache{limit: limit, cur: make(map[[32]byte]struct{})}
}

var vcache = newVerifyCache(verifyCacheMax)

func verifyCacheKey(pub, msg, sig []byte) [32]byte {
	h := sha256.New()
	h.Write(pub)
	h.Write(sig)
	h.Write(msg)
	var k [32]byte
	h.Sum(k[:0])
	return k
}

func (c *verifyCache) has(k [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.cur[k]; ok {
		return true
	}
	if _, ok := c.prev[k]; ok {
		c.insert(k)
		return true
	}
	return false
}

func (c *verifyCache) add(k [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.insert(k)
}

// insert adds k to the current generation, rotating first if it is full, so
// neither generation ever exceeds c.limit entries. Callers must hold c.mu.
func (c *verifyCache) insert(k [32]byte) {
	if len(c.cur) >= c.limit {
		c.prev, c.cur = c.cur, make(map[[32]byte]struct{})
	}
	c.cur[k] = struct{}{}
}
