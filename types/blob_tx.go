package types

import (
	squaretx "github.com/celestiaorg/go-square/v4/tx"
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

// unwrapLegacyBlobTx extracts the inner transaction using the legacy decoder,
// which only checks the type ID, the blob count and namespace ID lengths. It
// defines Tx.Hash, Tx.Key and the bytes sent to the application before
// BlobTxValidationAppVersion, so it must not change.
func unwrapLegacyBlobTx(tx Tx) (Tx, bool) {
	bTx, isBlob := UnmarshalBlobTx(tx)
	if !isBlob {
		return tx, false
	}
	return bTx.Tx, true
}
