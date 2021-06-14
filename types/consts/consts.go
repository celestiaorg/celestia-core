package consts

import (
	"crypto/sha256"

	"github.com/lazyledger/nmt/namespace"
)

// This contains all constants of:
// https://github.com/lazyledger/lazyledger-specs/blob/master/specs/consensus.md#constants
const (
	// ShareSize is the size of a share (in bytes).
	// see: https://github.com/lazyledger/lazyledger-specs/blob/master/specs/consensus.md#constants
	ShareSize = 256

	// NamespaceSize is the namespace size in bytes.
	NamespaceSize = 8

	// ShareReservedBytes is the reserved bytes for contiguous appends.
	ShareReservedBytes = 1

	// TxShareSize is the number of bytes usable for tx/evidence/ISR shares.
	TxShareSize = ShareSize - NamespaceSize - ShareReservedBytes
	// MsgShareSize is the number of bytes usable for message shares.
	MsgShareSize = ShareSize - NamespaceSize

	// MaxSquareSize is the maximum number of
	// rows/columns of the original data shares in square layout.
	// Corresponds to AVAILABLE_DATA_ORIGINAL_SQUARE_MAX in the spec.
	// 128*128*256 = 4 Megabytes
	// TODO(ismail): settle on a proper max square
	MaxSquareSize = 128

	// MinSquareSize depicts the smallest original square width.
	MinSquareSize = 1
	MinSharecount = MinSquareSize * MinSquareSize
)

var (
	// See spec for further details on the types of available data
	// https://github.com/lazyledger/lazyledger-specs/blob/master/specs/consensus.md#reserved-namespace-ids
	// https://github.com/lazyledger/lazyledger-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/data_structures.md#availabledata

	// TxNamespaceID is the namespace reserved for transaction data
	TxNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 1}
	// IntermediateStateRootsNamespaceID is the namespace reserved for
	// intermediate state root data
	IntermediateStateRootsNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 2}
	// EvidenceNamespaceID is the namespace reserved for evidence
	EvidenceNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 3}
	// MaxReservedNamespace is the lexicographically largest namespace that is
	// reserved for protocol use. It is derived from NAMESPACE_ID_MAX_RESERVED
	// https://github.com/lazyledger/lazyledger-specs/blob/master/specs/consensus.md#constants
	MaxReservedNamespace = namespace.ID{0, 0, 0, 0, 0, 0, 0, 255}
	// TailPaddingNamespaceID is the namespace ID for tail padding. All data
	// with this namespace will be ignored
	TailPaddingNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE}

	// ParitySharesNamespaceID indicates that share contains erasure data
	ParitySharesNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	// NewBaseHashFunc change accordingly if another hash.Hash should be used as a base hasher in the NMT:
	NewBaseHashFunc = sha256.New
)
