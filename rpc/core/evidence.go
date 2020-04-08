package core

import (
	ctypes "github.com/lazyledger/lazyledger-core/rpc/core/types"
	rpctypes "github.com/lazyledger/lazyledger-core/rpc/lib/types"
	"github.com/lazyledger/lazyledger-core/types"
)

// BroadcastEvidence broadcasts evidence of the misbehavior.
// More: https://docs.tendermint.com/master/rpc/#/Info/broadcast_evidence
func BroadcastEvidence(ctx *rpctypes.Context, ev types.Evidence) (*ctypes.ResultBroadcastEvidence, error) {
	err := evidencePool.AddEvidence(ev)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultBroadcastEvidence{Hash: ev.Hash()}, nil
}
