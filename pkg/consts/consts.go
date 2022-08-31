package consts

import (
	"crypto/sha256"

	"github.com/celestiaorg/nmt/namespace"
	"github.com/celestiaorg/rsmt2d"
)

// This contains all constants of:
// https://github.com/celestiaorg/celestia-specs/blob/master/src/specs/consensus.md#constants
const (
	// ShareSize is the size of a share (in bytes).
	// see: https://github.com/celestiaorg/celestia-specs/blob/master/src/specs/consensus.md#constants
	ShareSize = 256

	// NamespaceSize is the namespace size in bytes.
	NamespaceSize = 8

	// ShareInfoBytes is the number of bytes reserved for information. This info
	// byte contains the share version and a message start idicator.
	ShareInfoBytes = 1

	// ShareReservedBytes is the number of bytes reserved for the length
	// delimeter in a compact share (transactions, ISRs, evidence).
	ShareReservedBytes = 1

	// TxShareSize is the number of bytes usable for data in a compact share
	// (transactions, ISRs, evidence).
	TxShareSize = ShareSize - NamespaceSize - ShareInfoBytes - ShareReservedBytes
	// MsgShareSize is the number of bytes usable for data in a sparse share
	// (messages) share.
	MsgShareSize = ShareSize - NamespaceSize - ShareInfoBytes

	// MaxSquareSize is the maximum number of
	// rows/columns of the original data shares in square layout.
	// Corresponds to AVAILABLE_DATA_ORIGINAL_SQUARE_MAX in the spec.
	// 128*128*256 = 4 Megabytes
	// TODO(ismail): settle on a proper max square
	// if the square size is larger than this, the block producer will panic
	MaxSquareSize = 128
	// MaxShareCount is the maximum number of shares allowed in the original data square.
	// if there are more shares than this, the block producer will panic.
	MaxShareCount = MaxSquareSize * MaxSquareSize

	// MinSquareSize depicts the smallest original square width. A square size smaller than this will
	// cause block producer to panic
	MinSquareSize = 1
	// MinshareCount is the minimum shares required in an original data square.
	MinSharecount = MinSquareSize * MinSquareSize
)

var (
	// See spec for further details on the types of available data
	// https://github.com/celestiaorg/celestia-specs/blob/master/src/specs/consensus.md#reserved-namespace-ids
	// https://github.com/celestiaorg/celestia-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/data_structures.md#availabledata

	// TxNamespaceID is the namespace reserved for transaction data
	TxNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 1}
	// IntermediateStateRootsNamespaceID is the namespace reserved for
	// intermediate state root data
	// TODO(liamsi): code commented out but kept intentionally.
	// IntermediateStateRootsNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 2}

	// EvidenceNamespaceID is the namespace reserved for evidence
	EvidenceNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 3}

	// MaxReservedNamespace is the lexicographically largest namespace that is
	// reserved for protocol use. It is derived from NAMESPACE_ID_MAX_RESERVED
	// https://github.com/celestiaorg/celestia-specs/blob/master/src/specs/consensus.md#constants
	MaxReservedNamespace = namespace.ID{0, 0, 0, 0, 0, 0, 0, 255}
	// TailPaddingNamespaceID is the namespace ID for tail padding. All data
	// with this namespace will be ignored
	TailPaddingNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE}
	// ParitySharesNamespaceID indicates that share contains erasure data
	ParitySharesNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	// NewBaseHashFunc change accordingly if another hash.Hash should be used as a base hasher in the NMT:
	NewBaseHashFunc = sha256.New

	// DefaultCodec is the default codec creator used for data erasure
	// TODO(ismail): for better efficiency and a larger number shares
	// we should switch to the rsmt2d.LeopardFF16 codec:
	DefaultCodec = rsmt2d.NewRSGF8Codec

	// DataCommitmentBlocksLimit is the limit to the number of blocks we can generate a data commitment for.
	DataCommitmentBlocksLimit = 1000
)
