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
	// TxSharesQueryPath is the path used to query the application for the shares range
	// where the transaction live via the ABCI query method.
	TxSharesQueryPath = "custom/txShares/%d"
	// ShareInclusionProofQueryPath is the path used to query the application for the
	// shares to rows NMT inclusion proof via the ABCI query method.
	ShareInclusionProofQueryPath = "custom/shareInclusionProof/%d/%d"
	// RowsInclusionProofQueryPath is the path used to query the application for the
	// rows, defined by start row and end row, to data root merkle inclusion proof via
	// the ABCI query method.
	RowsInclusionProofQueryPath = "custom/rowsInclusionProof/%d/%d"
	// RowsInclusionProofBySharesQueryPath is the path used to query the application for the
	// rows, defined by a set of shares, to data root merkle inclusion proof via
	// the ABCI query method.
	RowsInclusionProofBySharesQueryPath = "custom/rowsInclusionProofByShares/%d/%d"
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
