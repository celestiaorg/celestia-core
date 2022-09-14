package consts

import (
	"crypto/sha256"

	"github.com/celestiaorg/nmt/namespace"
	"github.com/celestiaorg/rsmt2d"
)

const (
	// TxInclusionProofQueryPath is the path used to query the application for a
	// tx inclusion proof via the ABCI Query method. The desired transaction
	// index must be formatted into the path.
	TxInclusionProofQueryPath = "custom/txInclusionProof/%d"
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
