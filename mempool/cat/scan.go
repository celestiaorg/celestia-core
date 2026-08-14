package cat

import (
	"fmt"

	"github.com/cometbft/cometbft/mempool"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
)

// validateMempoolBytes rejects mempool messages that encode more transactions
// than are ever legal before protobuf unmarshalling can allocate one slice
// header per entry. An empty entry costs ~2 bytes on the wire, so without this
// check a message sized to hold one maximum-size transaction could be packed
// with entries and amplified into a far larger allocation, per message, per
// peer.
func validateMempoolBytes(msgBytes []byte) error {
	// Unmarshal into a stub that mirrors Message but decodes each transaction
	// into a zero-size NoTx, so the entries can be counted without allocating
	// them.
	var stub protomem.TxCountMessage
	if err := stub.Unmarshal(msgBytes); err != nil {
		return fmt.Errorf("malformed mempool message: %w", err)
	}
	if stub.Txs == nil {
		// Not the Txs oneof case (SeenTx, WantTx), nothing more to check.
		return nil
	}
	// errTooManyTxs is shared with the reactor's post-unmarshal check, so the
	// two limits cannot drift apart.
	if n := len(stub.Txs.Txs); n > mempool.MaxTxsPerMessage {
		return fmt.Errorf("%w (got %d, max %d)", errTooManyTxs, n, mempool.MaxTxsPerMessage)
	}
	return nil
}
