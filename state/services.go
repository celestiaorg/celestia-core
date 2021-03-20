package state

import (
	"github.com/lazyledger/lazyledger-core/types"
)

//------------------------------------------------------
// blockchain services types
// NOTE: Interfaces used by RPC must be thread safe!
//------------------------------------------------------

//------------------------------------------------------
// blockstore

// BlockStore defines the interface used by the ConsensusState.
type BlockStore interface {
	Base() int64
	Height() int64
	Size() int64

	LoadBaseMeta() *types.BlockMeta
	LoadBlockMeta(height int64) *types.BlockMeta
	LoadHeader(height int64) (*types.Header, *types.DataAvailabilityHeader)

	SaveHeader(header *types.Header, blockParts *types.DataAvailabilityHeader, seenCommit *types.Commit)

	PruneHeader(height int64) (uint64, error)

	LoadBlockCommit(height int64) *types.Commit
	LoadSeenCommit(height int64) *types.Commit
}

//-----------------------------------------------------------------------------
// evidence pool

//go:generate mockery --case underscore --name EvidencePool

// EvidencePool defines the EvidencePool interface used by State.
type EvidencePool interface {
	PendingEvidence(maxBytes int64) (ev []types.Evidence, size int64)
	AddEvidence(types.Evidence) error
	Update(State, types.EvidenceList)
	CheckEvidence(types.EvidenceList) error
}

// EmptyEvidencePool is an empty implementation of EvidencePool, useful for testing. It also complies
// to the consensus evidence pool interface
type EmptyEvidencePool struct{}

func (EmptyEvidencePool) PendingEvidence(maxBytes int64) (ev []types.Evidence, size int64) {
	return nil, 0
}
func (EmptyEvidencePool) AddEvidence(types.Evidence) error              { return nil }
func (EmptyEvidencePool) Update(State, types.EvidenceList)              {}
func (EmptyEvidencePool) CheckEvidence(evList types.EvidenceList) error { return nil }
func (EmptyEvidencePool) AddEvidenceFromConsensus(evidence types.Evidence) error {
	return nil
}
