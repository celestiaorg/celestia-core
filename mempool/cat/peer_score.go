package cat

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Score weights for the composite peer score. Successful work raises the score;
// failures lower it, with invalid chunks (a sign of a faulty or malicious peer)
// penalized far more heavily than timeouts (which may just be transient
// congestion), per the plan's "lightly penalize timeouts" guidance.
const (
	scoreWeightGoodChunk      = 1.0
	scoreWeightReconstruction = 3.0
	scoreWeightTimeout        = -0.5
	scoreWeightQueueFull      = -0.5
	scoreWeightInvalidChunk   = -25.0

	// ewmaAlpha is the smoothing factor for latency/throughput EWMAs.
	ewmaAlpha = 0.3
)

// peerScore holds decayed performance counters and EWMA latency/throughput for
// a single peer. All accumulators are decayed toward zero with a configurable
// halflife so recent behavior dominates ranking.
type peerScore struct {
	goodChunks      float64
	reconstructions float64
	timeouts        float64
	queueFull       float64
	invalidChunks   float64

	avgChunkLatency float64 // seconds, EWMA
	avgThroughput   float64 // bytes/sec, EWMA
	avgRTT          float64 // seconds, EWMA

	lastUpdate time.Time
}

// peerScoreTable tracks per-peer chunk-transfer performance and ranks peers for
// manifest advertisement and chunk-source selection. It is safe for concurrent
// use.
type peerScoreTable struct {
	mtx      sync.Mutex
	scores   map[uint16]*peerScore
	halflife time.Duration
	now      func() time.Time
}

func newPeerScoreTable(halflife time.Duration) *peerScoreTable {
	if halflife <= 0 {
		halflife = DefaultLargeTxPeerScoreHalflife
	}
	return &peerScoreTable{
		scores:   make(map[uint16]*peerScore),
		halflife: halflife,
		now:      time.Now,
	}
}

// decayFactor returns 0.5^(dt/halflife).
func (t *peerScoreTable) decayFactor(dt time.Duration) float64 {
	if dt <= 0 {
		return 1
	}
	return math.Pow(0.5, dt.Seconds()/t.halflife.Seconds())
}

// entry returns the peer's score, decaying its accumulators to the current time.
// Callers must hold t.mtx.
func (t *peerScoreTable) entry(peer uint16) *peerScore {
	ps, ok := t.scores[peer]
	now := t.now()
	if !ok {
		ps = &peerScore{lastUpdate: now}
		t.scores[peer] = ps
		return ps
	}
	f := t.decayFactor(now.Sub(ps.lastUpdate))
	ps.goodChunks *= f
	ps.reconstructions *= f
	ps.timeouts *= f
	ps.queueFull *= f
	ps.invalidChunks *= f
	ps.lastUpdate = now
	return ps
}

func ewma(prev, sample float64) float64 {
	if prev == 0 {
		return sample
	}
	return ewmaAlpha*sample + (1-ewmaAlpha)*prev
}

// RecordChunkSuccess records a chunk delivered successfully with the given
// round-trip latency and byte count.
func (t *peerScoreTable) RecordChunkSuccess(peer uint16, latency time.Duration, bytes int) {
	if peer == 0 {
		return
	}
	t.mtx.Lock()
	defer t.mtx.Unlock()
	ps := t.entry(peer)
	ps.goodChunks++
	if latency > 0 {
		ps.avgChunkLatency = ewma(ps.avgChunkLatency, latency.Seconds())
		if bytes > 0 {
			ps.avgThroughput = ewma(ps.avgThroughput, float64(bytes)/latency.Seconds())
		}
	}
}

// RecordChunkTimeout lightly penalizes a peer for an unanswered chunk request.
func (t *peerScoreTable) RecordChunkTimeout(peer uint16) {
	t.bump(peer, func(ps *peerScore) { ps.timeouts++ })
}

// RecordInvalidChunk heavily penalizes a peer for serving a chunk that failed
// hash verification.
func (t *peerScoreTable) RecordInvalidChunk(peer uint16) {
	t.bump(peer, func(ps *peerScore) { ps.invalidChunks++ })
}

// RecordQueueFull records that a send to the peer failed because its send queue
// was full.
func (t *peerScoreTable) RecordQueueFull(peer uint16) {
	t.bump(peer, func(ps *peerScore) { ps.queueFull++ })
}

// RecordReconstruction credits a peer that sourced chunks for a successful
// reconstruction.
func (t *peerScoreTable) RecordReconstruction(peer uint16) {
	t.bump(peer, func(ps *peerScore) { ps.reconstructions++ })
}

// RecordRTT updates the EWMA round-trip time observed for metadata messages.
func (t *peerScoreTable) RecordRTT(peer uint16, rtt time.Duration) {
	if peer == 0 || rtt <= 0 {
		return
	}
	t.mtx.Lock()
	defer t.mtx.Unlock()
	ps := t.entry(peer)
	ps.avgRTT = ewma(ps.avgRTT, rtt.Seconds())
}

func (t *peerScoreTable) bump(peer uint16, fn func(*peerScore)) {
	if peer == 0 {
		return
	}
	t.mtx.Lock()
	defer t.mtx.Unlock()
	fn(t.entry(peer))
}

// score computes the composite ranking score for a peer (higher is better).
// Callers must hold t.mtx.
func (ps *peerScore) score() float64 {
	s := scoreWeightGoodChunk*ps.goodChunks +
		scoreWeightReconstruction*ps.reconstructions +
		scoreWeightTimeout*ps.timeouts +
		scoreWeightQueueFull*ps.queueFull +
		scoreWeightInvalidChunk*ps.invalidChunks
	// Prefer lower latency as a tiebreaker without letting it dominate the
	// behavioral signals above.
	if ps.avgChunkLatency > 0 {
		s -= ps.avgChunkLatency
	}
	return s
}

// Score returns the current decayed composite score for a peer. Unknown peers
// score 0 (neutral).
func (t *peerScoreTable) Score(peer uint16) float64 {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	if _, ok := t.scores[peer]; !ok {
		return 0
	}
	return t.entry(peer).score()
}

// Rank orders peers best-first by score. Ties are broken by peer id for
// deterministic behavior.
func (t *peerScoreTable) Rank(peers []uint16) []uint16 {
	t.mtx.Lock()
	scores := make(map[uint16]float64, len(peers))
	for _, p := range peers {
		if _, ok := t.scores[p]; ok {
			scores[p] = t.entry(p).score()
		} else {
			scores[p] = 0
		}
	}
	t.mtx.Unlock()

	out := append([]uint16(nil), peers...)
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := scores[out[i]], scores[out[j]]
		if si == sj {
			return out[i] < out[j]
		}
		return si > sj
	})
	return out
}

// RemovePeer drops a disconnected peer's score.
func (t *peerScoreTable) RemovePeer(peer uint16) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	delete(t.scores, peer)
}
