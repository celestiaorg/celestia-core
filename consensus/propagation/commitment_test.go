package propagation

import (
	"fmt"
	dbm "github.com/cometbft/cometbft-db"
	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/proto/tendermint/mempool"
	"github.com/tendermint/tendermint/store"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	types "github.com/tendermint/tendermint/types"
)

func TestPropose(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	data := types.Data{
		Txs: []types.Tx{
			cmtrand.Bytes(1000),
			cmtrand.Bytes(64000),
			cmtrand.Bytes(2000000),
		},
	}

	block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))
	id := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	prop := types.NewProposal(block.Height, 0, 0, id)
	prop.Signature = cmtrand.Bytes(64)

	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{
			Start: uint32(pos.Start),
			End:   uint32(pos.End),
			Hash:  block.Txs[i].Hash(),
		}
	}

	reactor1.ProposeBlock(prop, partSet, metaData)

	time.Sleep(200 * time.Millisecond)

	// check that the proposal was saved in reactor 1
	_, _, has := reactor1.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check that the proposal was received by the other reactors
	_, _, has = reactor2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	_, _, has = reactor3.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check if the other reactors received the haves
	haves, has := reactor2.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, haves.Size(), int(partSet.Total()*2))

	haves, has = reactor3.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, haves.Size(), int(partSet.Total()*2))
}

// TestRecoverPartsLocally provides a set of transactions to the mempool
// and attempts to build the block parts from them.
func TestRecoverPartsLocally(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	numberOfTxs := 10
	txsMap := make(map[types.TxKey]types.Tx)
	txs := make([]types.Tx, numberOfTxs)
	for i := 0; i < numberOfTxs; i++ {
		tx := types.Tx(cmtrand.Bytes(int(types.BlockPartSizeBytes / 3)))
		txKey, err := types.TxKeyFromBytes(tx.Hash())
		require.NoError(t, err)
		txsMap[txKey] = tx
		txs[i] = tx
	}

	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockPropR := NewReactor("", trace.NoOpTracer(), blockStore, mockMempool{
		txs: txsMap,
	})

	data := types.Data{Txs: txs}

	block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))
	id := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	prop := types.NewProposal(block.Height, 0, 0, id)
	prop.Signature = cmtrand.Bytes(64)

	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{
			Start: uint32(pos.Start),
			End:   uint32(pos.End),
			Hash:  block.Txs[i].Hash(),
		}
	}

	blockPropR.ProposeBlock(prop, partSet, metaData)

	_, actualParts, _ := blockPropR.GetProposal(prop.Height, prop.Round)

	// we should be able to recover all the parts after where the transactions
	// are encoded
	startingPartIndex := metaData[0].Start/types.BlockPartSizeBytes + 1

	for i := startingPartIndex; i < partSet.Total()-1; i++ {
		assert.Equal(t, partSet.GetPart(int(i)).Bytes, actualParts.GetPart(int(i)).Bytes)
	}
}

// TestTxsToParts extensive testing of the txs to parts method
// that recovers the parts from mempool txs.
func TestTxsToParts(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	numberOfTxs := 18 // increasing the number of transactions increases the test time exponentially
	txs := make([]types.Tx, 0, numberOfTxs)
	for i := 0; i < numberOfTxs; i++ {
		txs = append(txs, cmtrand.Bytes(int(types.BlockPartSizeBytes/3)))
	}

	data := types.Data{Txs: txs}
	block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

	txsFound := make([]txFound, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		// calculate the protobuf overhead
		protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
		marshalledTx, err := proto.Marshal(&protoTxs)
		require.NoError(t, err)

		txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
		require.NoError(t, err)
		txsFound[i] = txFound{
			metaData: proptypes.TxMetaData{
				Start: uint32(pos.Start),
				End:   uint32(pos.End),
				Hash:  block.Txs[i].Hash(),
			},
			key:     txKey,
			txBytes: marshalledTx,
		}
	}

	// generate all the possible combinations for the provided number of transactions
	txsCombinations := GenerateTxsCombinations(numberOfTxs)

	for _, combination := range txsCombinations {
		t.Run(fmt.Sprintf("%v", combination), func(t *testing.T) {
			combinationTxs := make([]txFound, 0)
			for index, val := range combination {
				if val == 1 {
					combinationTxs = append(combinationTxs, txsFound[index])
				}
			}

			parts := txsToParts(combinationTxs)

			for _, part := range parts {
				expectedPart := partSet.GetPart(int(part.Index))
				assert.Equal(t, expectedPart.Bytes, part.Bytes)
			}
		})
	}
}

var _ Mempool = &mockMempool{}

type mockMempool struct {
	txs map[types.TxKey]types.Tx
}

func (m mockMempool) GetTxByKey(key types.TxKey) (types.Tx, bool) {
	return m.txs[key], true
}

// GenerateTxsCombinations generates all relevant transaction placements
// in a block given a number of transactions.
func GenerateTxsCombinations(n int) [][]int {
	total := 1 << n // 2^n combinations
	result := make([][]int, 0)

	for i := 0; i < total; i++ {
		bitArray := make([]int, n)
		for j := 0; j < n; j++ {
			// Extract the bit at position j
			bitArray[j] = (i >> j) & 1
		}
		result = append(result, bitArray)
	}
	return result
}

// checkMaxConsecutiveZeros ensures no more than 2 consecutive zeros exist
func checkMaxConsecutiveZeros(bits []int) bool {
	count := 0
	for _, bit := range bits {
		if bit == 0 {
			count++
			if count > 2 {
				return false
			}
		} else {
			count = 0
		}
	}
	return true
}
