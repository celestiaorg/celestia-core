package types

import (
	"context"
)

// MempoolSequenceApp defines the interface for sequence-aware transaction gossip.
type MempoolSequenceApp interface {
	// GetMempoolSequence returns the current mempool sequence for a signer.
	GetMempoolSequence(ctx context.Context, req *RequestGetMempoolSequence) (*ResponseGetMempoolSequence, error)
}
