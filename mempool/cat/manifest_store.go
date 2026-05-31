package cat

import (
	"sync"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// defaultMaxStoredManifests bounds how many large-tx manifests a node keeps so
// it can serve chunks and re-advertise. It caps memory from manifest metadata
// (chunk hashes), independent of the tx bytes themselves which live in the pool.
const defaultMaxStoredManifests = 1024

// manifestStore holds the manifests of large txs that this node can serve.
// A manifest describes the chunk layout (size + per-chunk hashes); the chunk
// bytes are sliced on demand from the full tx held in the pool. The store is a
// simple FIFO-evicting cache and is safe for concurrent use.
type manifestStore struct {
	mtx       sync.RWMutex
	manifests map[types.TxKey]*protomem.TxManifest
	order     []types.TxKey // insertion order for FIFO eviction
	limit     int
}

func newManifestStore(limit int) *manifestStore {
	if limit <= 0 {
		limit = defaultMaxStoredManifests
	}
	return &manifestStore{
		manifests: make(map[types.TxKey]*protomem.TxManifest),
		limit:     limit,
	}
}

// set stores a manifest, evicting the oldest entry if at capacity. Re-setting an
// existing key replaces the manifest without changing its eviction order.
func (ms *manifestStore) set(key types.TxKey, m *protomem.TxManifest) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	if _, ok := ms.manifests[key]; ok {
		ms.manifests[key] = m
		return
	}
	if len(ms.order) >= ms.limit {
		oldest := ms.order[0]
		ms.order = ms.order[1:]
		delete(ms.manifests, oldest)
	}
	ms.manifests[key] = m
	ms.order = append(ms.order, key)
}

func (ms *manifestStore) get(key types.TxKey) (*protomem.TxManifest, bool) {
	ms.mtx.RLock()
	defer ms.mtx.RUnlock()
	m, ok := ms.manifests[key]
	return m, ok
}

func (ms *manifestStore) has(key types.TxKey) bool {
	ms.mtx.RLock()
	defer ms.mtx.RUnlock()
	_, ok := ms.manifests[key]
	return ok
}

func (ms *manifestStore) remove(key types.TxKey) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	if _, ok := ms.manifests[key]; !ok {
		return
	}
	delete(ms.manifests, key)
	for i, k := range ms.order {
		if k == key {
			ms.order = append(ms.order[:i], ms.order[i+1:]...)
			break
		}
	}
}

func (ms *manifestStore) len() int {
	ms.mtx.RLock()
	defer ms.mtx.RUnlock()
	return len(ms.manifests)
}
