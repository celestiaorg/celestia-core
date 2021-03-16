package types

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

	// MaxSquareSize is the maximum number of
	// rows/columns of the original data shares in square layout.
	// Corresponds to AVAILABLE_DATA_ORIGINAL_SQUARE_MAX in the spec.
	// 128*128*256 = 4 Megabytes
	// TODO(ismail): settle on a proper max square
	MaxSquareSize = 128
)

var (
	TxNamespaceID                     = namespace.ID{0, 0, 0, 0, 0, 0, 0, 1}
	IntermediateStateRootsNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 2}
	EvidenceNamespaceID               = namespace.ID{0, 0, 0, 0, 0, 0, 0, 3}

	TailPaddingNamespaceID  = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE}
	ParitySharesNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	// change accordingly if another hash.Hash should be used as a base hasher in the NMT:
	newBaseHashFunc = sha256.New
)
