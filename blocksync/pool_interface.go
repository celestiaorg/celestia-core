package blocksync

import (
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// BlockPoolInterface defines the interface that both BlockPool and SlidingWindowPool implement.
// This allows the reactor to work with either implementation.
type BlockPoolInterface interface {
	service.Service

	// Block management
	AddBlock(peerID p2p.ID, block *types.Block, extCommit *types.ExtendedCommit, blockSize int) error
	PeekTwoBlocks() (first, second *types.Block, firstExtCommit *types.ExtendedCommit)
	PopRequest()
	RemovePeerAndRedoAllPeerRequests(height int64) p2p.ID

	// Peer management
	SetPeerRange(peerID p2p.ID, base int64, height int64)
	RemovePeer(peerID p2p.ID)
	RedoRequestFrom(height int64, peerID p2p.ID)

	// Status and synchronization
	IsCaughtUp() bool
	Height() int64
	MaxPeerHeight() int64
	GetStatus() (height int64, numPending int32, lenRequesters int)
	SetHeight(height int64)

	// Logger access
	SetLogger(log.Logger)
}

// Ensure BlockPool implements the interface
var _ BlockPoolInterface = (*BlockPool)(nil)

// Ensure SlidingWindowPool implements the interface
var _ BlockPoolInterface = (*SlidingWindowPool)(nil)
