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
	symbols  [][]byte
}

type reconstructionSession struct {
	manifest              *protomem.TxManifest
	chunks                [][]byte
	received              []bool
	symbols               [][]byte
	symbolReceived        []bool
	sourcePeer            []uint16
	sourceSymbolPeer      []uint16
	sources               map[uint16]struct{}
	symbolSources         map[uint16]map[uint32]struct{}
	inflight              map[uint32]*chunkRequest
	symbolInflight        map[uint32]*chunkRequest
	perPeerInflight       map[uint16]map[uint32]struct{}
	perPeerSymbolInflight map[uint16]map[uint32]struct{}
	waitingForSequence    bool
	waitingForAcceptance  bool
	deadline              *time.Timer
	createdAt             time.Time
}

type chunkRequest struct {
	peerID uint16
	sentAt time.Time
	timer  *time.Timer
	hedged bool
}

func buildLocalLargeTx(tx types.Tx, chunkSize, parityCount int, enableFountain bool, signer []byte, sequence uint64, priority int64) (*localLargeTx, error) {
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

	symbols := buildFountainSymbols(txKey, chunks, chunkSize, fountainParityCount(len(chunks), parityCount, enableFountain))
	if len(symbols) > 0 {
		manifest.FountainDataCount = uint32(len(chunks))
		manifest.FountainSymbolCount = uint32(len(symbols))
		manifest.FountainSymbolHashes = make([][]byte, len(symbols))
		for i, symbol := range symbols {
			manifest.FountainSymbolHashes[i] = tmhash.Sum(symbol)
		}
	}

	return &localLargeTx{
		manifest: manifest,
		chunks:   chunks,
		symbols:  symbols,
	}, nil
}

func fountainParityCount(dataCount, configuredParity int, enabled bool) int {
	if !enabled || dataCount == 0 || dataCount > maxFountainDataSymbols {
		return 0
	}
	if configuredParity <= 0 {
		return 0
	}
	if configuredParity > dataCount {
		return dataCount
	}
	return configuredParity
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
	clone.OptimisticIndexes = append([]uint32(nil), manifest.OptimisticIndexes...)
	clone.OptimisticSymbolIndexes = append([]uint32(nil), manifest.OptimisticSymbolIndexes...)
	if len(manifest.ChunkHashes) > 0 {
		clone.ChunkHashes = make([][]byte, len(manifest.ChunkHashes))
		for i, hash := range manifest.ChunkHashes {
			clone.ChunkHashes[i] = append([]byte(nil), hash...)
		}
	}
	if len(manifest.FountainSymbolHashes) > 0 {
		clone.FountainSymbolHashes = make([][]byte, len(manifest.FountainSymbolHashes))
		for i, hash := range manifest.FountainSymbolHashes {
			clone.FountainSymbolHashes[i] = append([]byte(nil), hash...)
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
		len(a.ChunkHashes) != len(b.ChunkHashes) ||
		a.FountainDataCount != b.FountainDataCount ||
		a.FountainSymbolCount != b.FountainSymbolCount ||
		len(a.FountainSymbolHashes) != len(b.FountainSymbolHashes) {
		return false
	}
	for i := range a.ChunkHashes {
		if !bytes.Equal(a.ChunkHashes[i], b.ChunkHashes[i]) {
			return false
		}
	}
	for i := range a.FountainSymbolHashes {
		if !bytes.Equal(a.FountainSymbolHashes[i], b.FountainSymbolHashes[i]) {
			return false
		}
	}
	return true
}

func newReconstructionSession(manifest *protomem.TxManifest, peerID uint16, deadline *time.Timer) *reconstructionSession {
	chunkCount := int(manifest.ChunkCount)
	symbolCount := int(manifest.FountainSymbolCount)
	session := &reconstructionSession{
		manifest:              cloneTxManifest(manifest),
		chunks:                make([][]byte, chunkCount),
		received:              make([]bool, chunkCount),
		symbols:               make([][]byte, symbolCount),
		symbolReceived:        make([]bool, symbolCount),
		sourcePeer:            make([]uint16, chunkCount),
		sourceSymbolPeer:      make([]uint16, symbolCount),
		sources:               make(map[uint16]struct{}),
		symbolSources:         make(map[uint16]map[uint32]struct{}),
		inflight:              make(map[uint32]*chunkRequest),
		symbolInflight:        make(map[uint32]*chunkRequest),
		perPeerInflight:       make(map[uint16]map[uint32]struct{}),
		perPeerSymbolInflight: make(map[uint16]map[uint32]struct{}),
		deadline:              deadline,
		createdAt:             time.Now().UTC(),
		waitingForSequence:    false,
		waitingForAcceptance:  manifest.Speculative,
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

func (s *reconstructionSession) addSymbolSource(peerID uint16, indexes []uint32) {
	if peerID == 0 || len(indexes) == 0 {
		return
	}
	if _, ok := s.symbolSources[peerID]; !ok {
		s.symbolSources[peerID] = make(map[uint32]struct{})
	}
	for _, index := range indexes {
		if int(index) >= len(s.symbolReceived) {
			continue
		}
		s.symbolSources[peerID][index] = struct{}{}
	}
}

func (s *reconstructionSession) removeSymbolSource(peerID uint16) {
	if peerID == 0 {
		return
	}
	delete(s.symbolSources, peerID)
	if indexes := s.perPeerSymbolInflight[peerID]; len(indexes) > 0 {
		for index := range indexes {
			if req := s.symbolInflight[index]; req != nil {
				req.timer.Stop()
			}
			delete(s.symbolInflight, index)
		}
	}
	delete(s.perPeerSymbolInflight, peerID)
}

func (s *reconstructionSession) sourceIDs() []uint16 {
	ids := make([]uint16, 0, len(s.sources))
	for id := range s.sources {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *reconstructionSession) symbolSourceIDs() []uint16 {
	ids := make([]uint16, 0, len(s.symbolSources))
	for id, indexes := range s.symbolSources {
		if len(indexes) > 0 {
			ids = append(ids, id)
		}
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

func (s *reconstructionSession) symbolInflightForPeer(peerID uint16) int {
	return len(s.perPeerSymbolInflight[peerID])
}

func (s *reconstructionSession) activeSymbolInflightPeerCount() int {
	count := 0
	for _, indexes := range s.perPeerSymbolInflight {
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

func (s *reconstructionSession) reassignInflight(index uint32, peerID uint16, sentAt time.Time, timer *time.Timer) {
	if req := s.inflight[index]; req != nil {
		if req.timer != nil {
			req.timer.Stop()
		}
		if indexes := s.perPeerInflight[req.peerID]; indexes != nil {
			delete(indexes, index)
			if len(indexes) == 0 {
				delete(s.perPeerInflight, req.peerID)
			}
		}
	}
	s.inflight[index] = &chunkRequest{peerID: peerID, sentAt: sentAt, timer: timer, hedged: true}
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

func (s *reconstructionSession) markSymbolInflight(index uint32, peerID uint16, sentAt time.Time, timer *time.Timer) {
	s.symbolInflight[index] = &chunkRequest{peerID: peerID, sentAt: sentAt, timer: timer}
	if _, ok := s.perPeerSymbolInflight[peerID]; !ok {
		s.perPeerSymbolInflight[peerID] = make(map[uint32]struct{})
	}
	s.perPeerSymbolInflight[peerID][index] = struct{}{}
}

func (s *reconstructionSession) clearSymbolInflight(index uint32) (uint16, time.Time, bool) {
	req, ok := s.symbolInflight[index]
	if !ok {
		return 0, time.Time{}, false
	}
	if req.timer != nil {
		req.timer.Stop()
	}
	delete(s.symbolInflight, index)
	if indexes := s.perPeerSymbolInflight[req.peerID]; indexes != nil {
		delete(indexes, index)
		if len(indexes) == 0 {
			delete(s.perPeerSymbolInflight, req.peerID)
		}
	}
	return req.peerID, req.sentAt, true
}

func (s *reconstructionSession) timeoutSymbolInflight(index uint32, peerID uint16) bool {
	req, ok := s.symbolInflight[index]
	if !ok || req.peerID != peerID {
		return false
	}
	delete(s.symbolInflight, index)
	if indexes := s.perPeerSymbolInflight[peerID]; indexes != nil {
		delete(indexes, index)
		if len(indexes) == 0 {
			delete(s.perPeerSymbolInflight, peerID)
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

func (s *reconstructionSession) missingSymbolIndexes(peerID uint16, limit int) []uint32 {
	if limit <= 0 {
		return nil
	}
	available := s.symbolSources[peerID]
	if len(available) == 0 {
		return nil
	}
	missing := make([]uint32, 0, limit)
	for index := range available {
		if int(index) >= len(s.symbolReceived) || s.symbolReceived[index] {
			continue
		}
		if _, ok := s.symbolInflight[index]; ok {
			continue
		}
		missing = append(missing, index)
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	if len(missing) > limit {
		missing = missing[:limit]
	}
	return missing
}

func (s *reconstructionSession) hedgeableInflightIndexes(peerID uint16, limit int, minAge time.Duration, now time.Time) []uint32 {
	if limit <= 0 {
		return nil
	}
	type candidate struct {
		index  uint32
		sentAt time.Time
	}
	candidates := make([]candidate, 0, len(s.inflight))
	for index, req := range s.inflight {
		if req == nil || req.peerID == peerID || req.hedged || s.received[index] {
			continue
		}
		if minAge > 0 && now.Sub(req.sentAt) < minAge {
			continue
		}
		candidates = append(candidates, candidate{index: index, sentAt: req.sentAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].sentAt.Equal(candidates[j].sentAt) {
			return candidates[i].index > candidates[j].index
		}
		return candidates[i].sentAt.Before(candidates[j].sentAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	indexes := make([]uint32, 0, len(candidates))
	for _, candidate := range candidates {
		indexes = append(indexes, candidate.index)
	}
	return indexes
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

func (s *reconstructionSession) markSymbolReceived(index uint32, data []byte, peerID uint16) bool {
	if int(index) >= len(s.symbolReceived) {
		return false
	}
	if s.symbolReceived[index] {
		return false
	}
	s.symbols[index] = append([]byte(nil), data...)
	s.symbolReceived[index] = true
	s.sourceSymbolPeer[index] = peerID
	return true
}

func (s *reconstructionSession) receivedSymbolIndexes() []uint32 {
	indexes := make([]uint32, 0, len(s.symbolReceived))
	for i, received := range s.symbolReceived {
		if received {
			indexes = append(indexes, uint32(i))
		}
	}
	return indexes
}

func (s *reconstructionSession) receivedSymbolCount() int {
	count := 0
	for _, received := range s.symbolReceived {
		if received {
			count++
		}
	}
	return count
}

func (s *reconstructionSession) tryCompleteFromSymbols(txKey types.TxKey) bool {
	if s.complete() || s.manifest.FountainDataCount == 0 || s.manifest.FountainSymbolCount == 0 {
		return s.complete()
	}
	dataCount := int(s.manifest.FountainDataCount)
	if s.receivedSymbolCount() < dataCount {
		return false
	}
	decoded, ok := tryDecodeFountain(txKey, s.symbols, s.symbolReceived, int(s.manifest.ChunkSize), dataCount)
	if !ok {
		return false
	}
	for i := 0; i < dataCount; i++ {
		chunkLen := expectedChunkLength(s.manifest, uint32(i))
		if chunkLen <= 0 || chunkLen > len(decoded[i]) {
			return false
		}
		s.markReceived(uint32(i), decoded[i][:chunkLen], s.sourceSymbolPeerForDecodedChunk(uint32(i)))
	}
	return s.complete()
}

func (s *reconstructionSession) sourceSymbolPeerForDecodedChunk(index uint32) uint16 {
	if int(index) < len(s.sourceSymbolPeer) && s.sourceSymbolPeer[index] != 0 {
		return s.sourceSymbolPeer[index]
	}
	for _, peerID := range s.sourceSymbolPeer {
		if peerID != 0 {
			return peerID
		}
	}
	return 0
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
	for _, req := range s.symbolInflight {
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
	localSymbols := make([][]byte, len(s.symbols))
	copy(localSymbols, s.symbols)
	return &localLargeTx{
		manifest: cloneTxManifest(s.manifest),
		chunks:   localChunks,
		symbols:  localSymbols,
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
	for _, peerID := range s.sourceSymbolPeer {
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
