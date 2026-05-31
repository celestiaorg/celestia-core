package cat

import (
	"math"
	"sync"
	"time"
)

const (
	peerScoreChunkSuccessDelta   = 1.0
	peerScoreReconstructionDelta = 3.0
	peerScoreTimeoutPenalty      = -0.25
	peerScoreSendFailurePenalty  = -0.5
	peerScoreInvalidChunkPenalty = -100.0
	peerScoreMin                 = -1000.0
	peerScoreMax                 = 1000.0
)

type peerScoreTable struct {
	mu       sync.Mutex
	halflife time.Duration
	scores   map[uint16]*catPeerScore
}

type catPeerScore struct {
	score                float64
	chunkLatency         time.Duration
	chunkThroughputBytes float64
	failedChunkRequests  uint64
	invalidChunks        uint64
	queueSendFailures    uint64
	successfulChunks     uint64
	reconstructions      uint64
	updatedAt            time.Time
}

func newPeerScoreTable(halflife time.Duration) *peerScoreTable {
	return &peerScoreTable{
		halflife: halflife,
		scores:   make(map[uint16]*catPeerScore),
	}
}

func (pst *peerScoreTable) Score(peerID uint16) float64 {
	if peerID == 0 || pst == nil {
		return 0
	}
	pst.mu.Lock()
	defer pst.mu.Unlock()
	score := pst.scoreForPeer(peerID, time.Now())
	if score == nil {
		return 0
	}
	return score.score
}

func (pst *peerScoreTable) RecordChunk(peerID uint16, bytes int, latency time.Duration) {
	if peerID == 0 || pst == nil {
		return
	}
	pst.mu.Lock()
	defer pst.mu.Unlock()
	score := pst.ensureScore(peerID, time.Now())
	score.successfulChunks++
	if latency > 0 {
		if score.chunkLatency == 0 {
			score.chunkLatency = latency
		} else {
			score.chunkLatency = (score.chunkLatency*7 + latency) / 8
		}
		score.chunkThroughputBytes = 0.875*score.chunkThroughputBytes + 0.125*(float64(bytes)/latency.Seconds())
	}
	pst.add(score, peerScoreChunkSuccessDelta)
}

func (pst *peerScoreTable) RecordReconstruction(peerID uint16) {
	if peerID == 0 || pst == nil {
		return
	}
	pst.mu.Lock()
	defer pst.mu.Unlock()
	score := pst.ensureScore(peerID, time.Now())
	score.reconstructions++
	pst.add(score, peerScoreReconstructionDelta)
}

func (pst *peerScoreTable) RecordTimeout(peerID uint16) {
	if peerID == 0 || pst == nil {
		return
	}
	pst.mu.Lock()
	defer pst.mu.Unlock()
	score := pst.ensureScore(peerID, time.Now())
	score.failedChunkRequests++
	pst.add(score, peerScoreTimeoutPenalty)
}

func (pst *peerScoreTable) RecordSendFailure(peerID uint16) {
	if peerID == 0 || pst == nil {
		return
	}
	pst.mu.Lock()
	defer pst.mu.Unlock()
	score := pst.ensureScore(peerID, time.Now())
	score.queueSendFailures++
	pst.add(score, peerScoreSendFailurePenalty)
}

func (pst *peerScoreTable) RecordInvalidChunk(peerID uint16) {
	if peerID == 0 || pst == nil {
		return
	}
	pst.mu.Lock()
	defer pst.mu.Unlock()
	score := pst.ensureScore(peerID, time.Now())
	score.invalidChunks++
	pst.add(score, peerScoreInvalidChunkPenalty)
}

func (pst *peerScoreTable) scoreForPeer(peerID uint16, now time.Time) *catPeerScore {
	score := pst.scores[peerID]
	if score == nil {
		return nil
	}
	pst.applyDecay(score, now)
	return score
}

func (pst *peerScoreTable) ensureScore(peerID uint16, now time.Time) *catPeerScore {
	score := pst.scores[peerID]
	if score == nil {
		score = &catPeerScore{updatedAt: now}
		pst.scores[peerID] = score
		return score
	}
	pst.applyDecay(score, now)
	return score
}

func (pst *peerScoreTable) applyDecay(score *catPeerScore, now time.Time) {
	if pst.halflife <= 0 || score.updatedAt.IsZero() {
		score.updatedAt = now
		return
	}
	elapsed := now.Sub(score.updatedAt)
	if elapsed <= 0 {
		return
	}
	factor := math.Pow(0.5, elapsed.Seconds()/pst.halflife.Seconds())
	score.score *= factor
	score.updatedAt = now
}

func (pst *peerScoreTable) add(score *catPeerScore, delta float64) {
	score.score += delta
	if score.score < peerScoreMin {
		score.score = peerScoreMin
	}
	if score.score > peerScoreMax {
		score.score = peerScoreMax
	}
}
