package cat

import (
	"context"
	"fmt"

	"github.com/tendermint/tendermint/types"
)

type blockKey struct {
}

func (memR *Reactor) FetchTxsFromKeys(ctx context.Context, height int64, round int32, compactData [][]byte) ([][]byte, error) {
	txs := make([][]byte, len(compactData))
	missingKeys := make([]types.TxKey, 0, len(compactData))
	hasBitArray := NewBitArray(uint64(len(compactData)))
	for i, key := range compactData {
		txKey, err := types.TxKeyFromBytes(key)
		if err != nil {
			return nil, fmt.Errorf("incorrect compact blocks format: %w", err)
		}
		wtx := memR.mempool.store.get(txKey)
		if wtx != nil {
			txs[i] = wtx.tx
			hasBitArray.Set(uint64(i))
		} else {
			missingKeys = append(missingKeys, txKey)
		}
	}
	memR.broadcastHasBlockTxs(height, round, hasBitArray)
	return txs, nil

}

func (memR *Reactor) broadcastHasBlockTxs(height int64, round int32, bitArray *BitArray) {
	
}
