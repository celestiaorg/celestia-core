package types

import (
	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// BlobTxValidationAppVersion is the first app version whose blocks are unwrapped
// with go-square's validating BlobTx decoder. Blocks from earlier app versions
// keep the legacy rules so historical replay is unchanged.
const BlobTxValidationAppVersion uint64 = 11

// UnwrapBlobTx returns the inner transaction of a BlobTx and true. It returns
// tx and false if tx is not a BlobTx under the rules for appVersion.
func UnwrapBlobTx(tx Tx, appVersion uint64) (Tx, bool) {
	if appVersion < BlobTxValidationAppVersion {
		return unwrapLegacyBlobTx(tx)
	}
	bTx, isBlob, err := squaretx.UnmarshalBlobTx(tx)
	if !isBlob || err != nil {
		return tx, false
	}
	return bTx.Tx, true
}

// unwrapLegacyBlobTx extracts the inner transaction using the legacy rules,
// which only check the type ID, the blob count and namespace ID lengths. They
// define Tx.Hash, Tx.Key and the bytes sent to the application before
// BlobTxValidationAppVersion, so they must not change.
func unwrapLegacyBlobTx(tx Tx) (Tx, bool) {
	var bTx cmtproto.BlobTx //nolint:staticcheck // legacy rules need the legacy message
	if err := bTx.Unmarshal(tx); err != nil {
		return tx, false
	}
	if bTx.TypeId != squaretx.ProtoBlobTxTypeID || len(bTx.Blobs) == 0 {
		return tx, false
	}
	for _, b := range bTx.Blobs {
		if len(b.NamespaceId) != share.NamespaceIDSize {
			return tx, false
		}
	}
	return bTx.Tx, true
}
