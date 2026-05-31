package cat

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

var (
	errInvalidTxManifest = errors.New("invalid tx manifest")
	errInvalidTxChunk    = errors.New("invalid tx chunk")
)

type localLargeTx struct {
	manifest *protomem.TxManifest
	chunks   [][]byte
}

type reconstructionSession struct {
	manifest           *protomem.TxManifest
	chunks             [][]byte
	received           []bool
	sourcePeer         []uint16
	sources            map[uint16]struct{}
	inflight           map[uint32]*chunkRequest
	perPeerInflight    map[uint16]map[uint32]struct{}
	waitingForSequence bool
	deadline           *time.Timer
	createdAt          time.Time
}

type chunkRequest struct {
	peerID uint16
	sentAt time.Time
	timer  *time.Timer
}

func buildLocalLargeTx(tx types.Tx, chunkSize int, signer []byte, sequence uint64, priority int64) (*localLargeTx, error) {
	if len(tx) == 0 {
		return nil, fmt.Errorf("%w: empty tx", errInvalidTxManifest)
	}
	if chunkSize <= 0 {
		return nil, fmt.Errorf("%w: chunk size must be positive", errInvalidTxManifest)
	}
	if len(tx) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: tx size exceeds uint32", errInvalidTxManifest)
	}

	chunks := splitTxChunks(tx, chunkSize)
	chunkHashes := make([][]byte, len(chunks))
	for i, chunk := range chunks {
		chunkHashes[i] = tmhash.Sum(chunk)
	}

	txKey := tx.Key()
	manifest := &protomem.TxManifest{
		TxKey:       append([]byte(nil), txKey[:]...),
		TxSize:      uint32(len(tx)),
		ChunkSize:   uint32(chunkSize),
		ChunkCount:  uint32(len(chunks)),
		ChunkHashes: chunkHashes,
		Sequence:    sequence,
		Signer:      append([]byte(nil), signer...),
	}
	if priority > 0 {
		manifest.Priority = uint64(priority)
	}

	return &localLargeTx{
		manifest: manifest,
		chunks:   chunks,
	}, nil
}

func splitTxChunks(tx types.Tx, chunkSize int) [][]byte {
	chunkCount := (len(tx) + chunkSize - 1) / chunkSize
	chunks := make([][]byte, 0, chunkCount)
	for start := 0; start < len(tx); start += chunkSize {
		end := start + chunkSize
		if end > len(tx) {
			end = len(tx)
		}
		chunks = append(chunks, tx[start:end])
	}
	return chunks
}

func cloneTxManifest(manifest *protomem.TxManifest) *protomem.TxManifest {
	if manifest == nil {
		return nil
	}
	clone := *manifest
	clone.TxKey = append([]byte(nil), manifest.TxKey...)
	clone.Signer = append([]byte(nil), manifest.Signer...)
	if len(manifest.ChunkHashes) > 0 {
		clone.ChunkHashes = make([][]byte, len(manifest.ChunkHashes))
		for i, hash := range manifest.ChunkHashes {
			clone.ChunkHashes[i] = append([]byte(nil), hash...)
		}
	}
	return &clone
}

func manifestsEqual(a, b *protomem.TxManifest) bool {
	if a == nil || b == nil {
		return a == b
	}
	if !bytes.Equal(a.TxKey, b.TxKey) ||
		a.TxSize != b.TxSize ||
		a.ChunkSize != b.ChunkSize ||
		a.ChunkCount != b.ChunkCount ||
		len(a.ChunkHashes) != len(b.ChunkHashes) {
		return false
	}
	for i := range a.ChunkHashes {
		if !bytes.Equal(a.ChunkHashes[i], b.ChunkHashes[i]) {
			return false
		}
	}
	return true
}

func newReconstructionSession(manifest *protomem.TxManifest, peerID uint16, deadline *time.Timer) *reconstructionSession {
	chunkCount := int(manifest.ChunkCount)
	session := &reconstructionSession{
		manifest:           cloneTxManifest(manifest),
		chunks:             make([][]byte, chunkCount),
		received:           make([]bool, chunkCount),
		sourcePeer:         make([]uint16, chunkCount),
		sources:            make(map[uint16]struct{}),
		inflight:           make(map[uint32]*chunkRequest),
		perPeerInflight:    make(map[uint16]map[uint32]struct{}),
		deadline:           deadline,
		createdAt:          time.Now().UTC(),
		waitingForSequence: false,
	}
	session.addSource(peerID)
	return session
}

func (s *reconstructionSession) addSource(peerID uint16) {
	if peerID != 0 {
		s.sources[peerID] = struct{}{}
	}
}

func (s *reconstructionSession) removeSource(peerID uint16) {
	if peerID == 0 {
		return
	}
	delete(s.sources, peerID)
	if indexes := s.perPeerInflight[peerID]; len(indexes) > 0 {
		for index := range indexes {
			if req := s.inflight[index]; req != nil {
				req.timer.Stop()
			}
			delete(s.inflight, index)
		}
	}
	delete(s.perPeerInflight, peerID)
}

func (s *reconstructionSession) sourceIDs() []uint16 {
	ids := make([]uint16, 0, len(s.sources))
	for id := range s.sources {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *reconstructionSession) inflightForPeer(peerID uint16) int {
	return len(s.perPeerInflight[peerID])
}

func (s *reconstructionSession) activeInflightPeerCount() int {
	count := 0
	for _, indexes := range s.perPeerInflight {
		if len(indexes) > 0 {
			count++
		}
	}
	return count
}

func (s *reconstructionSession) markInflight(index uint32, peerID uint16, sentAt time.Time, timer *time.Timer) {
	s.inflight[index] = &chunkRequest{peerID: peerID, sentAt: sentAt, timer: timer}
	if _, ok := s.perPeerInflight[peerID]; !ok {
		s.perPeerInflight[peerID] = make(map[uint32]struct{})
	}
	s.perPeerInflight[peerID][index] = struct{}{}
}

func (s *reconstructionSession) clearInflight(index uint32) (uint16, time.Time, bool) {
	req, ok := s.inflight[index]
	if !ok {
		return 0, time.Time{}, false
	}
	if req.timer != nil {
		req.timer.Stop()
	}
	delete(s.inflight, index)
	if indexes := s.perPeerInflight[req.peerID]; indexes != nil {
		delete(indexes, index)
		if len(indexes) == 0 {
			delete(s.perPeerInflight, req.peerID)
		}
	}
	return req.peerID, req.sentAt, true
}

func (s *reconstructionSession) timeoutInflight(index uint32, peerID uint16) bool {
	req, ok := s.inflight[index]
	if !ok || req.peerID != peerID {
		return false
	}
	delete(s.inflight, index)
	if indexes := s.perPeerInflight[peerID]; indexes != nil {
		delete(indexes, index)
		if len(indexes) == 0 {
			delete(s.perPeerInflight, peerID)
		}
	}
	return true
}

func (s *reconstructionSession) missingIndexes(limit int) []uint32 {
	if limit <= 0 {
		return nil
	}
	missing := make([]uint32, 0, limit)
	for i := range s.received {
		index := uint32(i)
		if s.received[i] {
			continue
		}
		if _, ok := s.inflight[index]; ok {
			continue
		}
		missing = append(missing, index)
		if len(missing) == limit {
			return missing
		}
	}
	return missing
}

func (s *reconstructionSession) markReceived(index uint32, data []byte, peerID uint16) bool {
	if int(index) >= len(s.received) {
		return false
	}
	if s.received[index] {
		return false
	}
	s.chunks[index] = append([]byte(nil), data...)
	s.received[index] = true
	s.sourcePeer[index] = peerID
	return true
}

func (s *reconstructionSession) complete() bool {
	for _, received := range s.received {
		if !received {
			return false
		}
	}
	return true
}

func (s *reconstructionSession) stop() {
	if s.deadline != nil {
		s.deadline.Stop()
	}
	for _, req := range s.inflight {
		if req.timer != nil {
			req.timer.Stop()
		}
	}
}

func (s *reconstructionSession) reconstruct() types.Tx {
	tx := make([]byte, 0, int(s.manifest.TxSize))
	for _, chunk := range s.chunks {
		tx = append(tx, chunk...)
	}
	return types.Tx(tx)
}

func (s *reconstructionSession) toLocalLargeTx() *localLargeTx {
	localChunks := make([][]byte, len(s.chunks))
	copy(localChunks, s.chunks)
	return &localLargeTx{
		manifest: cloneTxManifest(s.manifest),
		chunks:   localChunks,
	}
}

func (s *reconstructionSession) firstSourcePeer() uint16 {
	for _, peerID := range s.sourcePeer {
		if peerID != 0 {
			return peerID
		}
	}
	for peerID := range s.sources {
		if peerID != 0 {
			return peerID
		}
	}
	return 0
}

func expectedChunkLength(manifest *protomem.TxManifest, index uint32) int {
	if manifest == nil || index >= manifest.ChunkCount {
		return -1
	}
	chunkSize := int(manifest.ChunkSize)
	if index < manifest.ChunkCount-1 {
		return chunkSize
	}
	remaining := int(manifest.TxSize) - chunkSize*int(index)
	if remaining < 0 {
		return -1
	}
	return remaining
}
