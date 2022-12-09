package consts

import (
	"crypto/sha256"

	"github.com/celestiaorg/nmt/namespace"
)

const (
	// TxInclusionProofQueryPath is the path used to query the application for a
	// tx inclusion proof via the ABCI Query method. The desired transaction
	// index must be formatted into the path.
	TxInclusionProofQueryPath = "custom/txInclusionProof/%d"

	// ProtoBlobTxTypeID is included in each encoded BlobTx to help prevent
	// decoding binaries that are not actually BlobTxs.
	ProtoBlobTxTypeID = "BLOB"

	// ProtoIndexWrapperTypeID is included in each encoded IndexWrapper to help prevent
	// decoding binaries that are not actually IndexWrappers.
	ProtoIndexWrapperTypeID = "INDX"
)

var (
	// See spec for further details on the types of available data
	// https://github.com/celestiaorg/celestia-specs/blob/master/src/specs/consensus.md#reserved-namespace-ids
	// https://github.com/celestiaorg/celestia-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/data_structures.md#availabledata

	// TxNamespaceID is the namespace reserved for transaction data
	TxNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 1}

	// NewBaseHashFunc change accordingly if another hash.Hash should be used as a base hasher in the NMT:
	NewBaseHashFunc = sha256.New

	// DataCommitmentBlocksLimit is the limit to the number of blocks we can generate a data commitment for.
	DataCommitmentBlocksLimit = 1000
)
