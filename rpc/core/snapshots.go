package core

import (
	"errors"
	"math"
	"sort"

	abci "github.com/cometbft/cometbft/abci/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// Snapshots lists recent state-sync snapshots for clients without a P2P slot.
func (env *Environment) Snapshots(ctx *rpctypes.Context) (*abci.ResponseListSnapshots, error) {
	resp, err := env.ProxyAppSnapshot.ListSnapshots(ctx.Context(), &abci.RequestListSnapshots{})
	if err != nil {
		return nil, err
	}
	snaps := append([]*abci.Snapshot(nil), resp.Snapshots...)
	sort.SliceStable(snaps, func(i, j int) bool {
		if snaps[i].Height == snaps[j].Height {
			return snaps[i].Format > snaps[j].Format
		}
		return snaps[i].Height > snaps[j].Height
	})
	if len(snaps) > 10 {
		snaps = snaps[:10]
	}
	return &abci.ResponseListSnapshots{Snapshots: snaps}, nil
}

// SnapshotChunk reads one application snapshot chunk. A pruned chunk is empty.
func (env *Environment) SnapshotChunk(ctx *rpctypes.Context, height, format, index uint64) (*abci.ResponseLoadSnapshotChunk, error) {
	if height == 0 {
		return nil, errors.New("snapshot height must be positive")
	}
	if format > math.MaxUint32 || index > math.MaxUint32 {
		return nil, errors.New("snapshot format or index exceeds uint32")
	}
	return env.ProxyAppSnapshot.LoadSnapshotChunk(ctx.Context(), &abci.RequestLoadSnapshotChunk{
		Height: height, Format: uint32(format), Chunk: uint32(index),
	})
}
