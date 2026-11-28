package core

import (
	"errors"
	"math"
	"sync"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// Coalesce inventory scans on archival nodes and bound the retained metadata.
type snapshotOfferCache struct {
	sync.Mutex
	until    time.Time
	response *abci.ResponseListSnapshots
	err      error
}

// Snapshots lists recent state-sync snapshots for clients without a P2P slot.
func (env *Environment) Snapshots(ctx *rpctypes.Context) (*abci.ResponseListSnapshots, error) {
	cache := &env.snapshotOffers
	cache.Lock()
	defer cache.Unlock()
	if time.Now().Before(cache.until) {
		return cache.response, cache.err
	}
	resp, err := env.ProxyAppSnapshot.ListSnapshots(ctx.Context(), &abci.RequestListSnapshots{})
	if err == nil && resp == nil {
		err = errors.New("missing ABCI snapshot inventory")
	}
	if err == nil {
		offers := abci.RecentSnapshots(resp.Snapshots, 10)
		resp = &abci.ResponseListSnapshots{}
		budget := 4_000_000
		for _, s := range offers {
			if size := s.Size(); size <= budget {
				resp.Snapshots = append(resp.Snapshots, s)
				budget -= size
			}
		}
	}
	cache.response, cache.err = resp, err
	cache.until = time.Now().Add(5 * time.Second)
	return resp, err
}

// SnapshotChunk reads one application snapshot chunk. A pruned chunk is empty.
func (env *Environment) SnapshotChunk(ctx *rpctypes.Context, height, format, index uint64) (*abci.ResponseLoadSnapshotChunk, error) {
	if height == 0 {
		return nil, errors.New("snapshot height must be positive")
	}
	if format > math.MaxUint32 || index > math.MaxUint32 {
		return nil, errors.New("snapshot format or index exceeds uint32")
	}
	resp, err := env.ProxyAppSnapshot.LoadSnapshotChunk(ctx.Context(), &abci.RequestLoadSnapshotChunk{
		Height: height, Format: uint32(format), Chunk: uint32(index),
	})
	if err == nil && resp == nil {
		return nil, errors.New("missing ABCI snapshot chunk response")
	}
	if err == nil && len(resp.Chunk) > 16_000_000 {
		return nil, errors.New("snapshot chunk exceeds transport limit")
	}
	return resp, err
}
