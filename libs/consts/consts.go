package consts

import (
	"crypto/sha256"
)

const (
	// TxInclusionProofQueryPath is the path used to query the application for a
	// tx inclusion proof via the ABCI Query method. The desired transaction
	// index must be formatted into the path.
	TxInclusionProofQueryPath = "custom/txInclusionProof/%d"

	// ShareInclusionProofQueryPath is the path used to query the application for the
	// shares to data root inclusion proofs via the ABCI query method.
	ShareInclusionProofQueryPath = "custom/shareInclusionProof/%d/%d"
)

var (
	// TxNamespaceID is the namespace ID reserved for transaction data. It does
	// not contain a leading version byte.
	TxNamespaceID = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}

	// NewBaseHashFunc change accordingly if another hash.Hash should be used as a base hasher in the NMT.
	NewBaseHashFunc = sha256.New

	// DataCommitmentBlocksLimit is the limit to the number of blocks we can generate a data commitment for.
	// Deprecated: this is no longer used as we're moving towards Blobstream X. However, we're leaving it
	// here for backwards compatibility purpose until it's removed in the next breaking release.
	DataCommitmentBlocksLimit = 1000
)
