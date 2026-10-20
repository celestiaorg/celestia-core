package types

import (
	squaretx "github.com/celestiaorg/go-square/v4/tx"
)

// UnwrapBlobTx returns the inner transaction of a valid BlobTx and true, or tx
// and false otherwise. It uses go-square's decoder, the same one celestia-app
// uses to validate BlobTxs.
func UnwrapBlobTx(tx Tx) (Tx, bool) {
	bTx, isBlob, err := squaretx.UnmarshalBlobTx(tx)
	if !isBlob || err != nil {
		return tx, false
	}
	return bTx.Tx, true
}
