package cat

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/mempool"
	"github.com/tendermint/tendermint/p2p"
	memproto "github.com/tendermint/tendermint/proto/tendermint/mempool"
	"github.com/tendermint/tendermint/types"
)

func TestBlockRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx1, tx2 := types.Tx("hello"), types.Tx("world")
	key1, key2 := tx1.Key(), tx2.Key()
	txs := make([][]byte, 2)
	missingKeys := map[int]types.TxKey{
		0: key1,
		1: key2,
	}

	request := NewBlockRequest(1, missingKeys, txs)

	require.True(t, request.TryAddMissingTx(key1, tx1))
	// cannot add the same missing tx twice
	require.False(t, request.TryAddMissingTx(key1, tx1))
	require.False(t, request.IsDone())

	// test that we adhere to the context deadline
	shortCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	txs, err := request.WaitForBlock(shortCtx)
	require.Error(t, err)

	// test that all txs mean the block `IsDone`
	require.True(t, request.TryAddMissingTx(key2, tx2))
	require.True(t, request.IsDone())

	// waiting for the block should instantly return
	txs, err = request.WaitForBlock(ctx)
	require.NoError(t, err)
	require.Equal(t, txs, [][]byte{tx1, tx2})
}

func TestBlockRequestConcurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	const numTxs = 10
	allTxs := make([][]byte, numTxs)
	txKeys := make([]types.TxKey, numTxs)
	missingKeys := make(map[int]types.TxKey)
	txs := make([][]byte, numTxs)
	for i := 0; i < numTxs; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		allTxs[i] = tx
		txKeys[i] = tx.Key()
		if i%3 == 0 {
			txs[i] = tx
		} else {
			missingKeys[i] = txKeys[i]
		}
	}

	request := NewBlockRequest(1, missingKeys, txs)

	wg := sync.WaitGroup{}
	for i := 0; i < numTxs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request.TryAddMissingTx(txKeys[i], txs[i])
		}(i)
	}

	// wait for the block
	result, err := request.WaitForBlock(ctx)
	require.NoError(t, err)
	require.Len(t, result, numTxs)
	for i := 0; i < numTxs; i++ {
		require.Equal(t, string(result[i]), string(txs[i]))
	}
	wg.Wait()
}

func TestBlockRequestMissingTxs(t *testing.T) {
	tx1, tx2 := types.Tx("hello"), types.Tx("world")
	key1, key2 := tx1.Key(), tx2.Key()
	txs := make([][]byte, 2)
	missingKeys := map[int]types.TxKey{
		0: key1,
		1: key2,
	}

	request := NewBlockRequest(1, missingKeys, txs)
	// test that a bit array returns the right tx key
	ba := NewBitArray(2)
	ba.Set(0)
	key, err := request.GetMissingKeys(ba.Bytes())
	require.NoError(t, err)
	require.Equal(t, key, []types.TxKey{key1})

	// wrong size should return an error
	badBA := NewBitArray(9)
	badBA.Set(1)
	_, err = request.GetMissingKeys(badBA.Bytes())
	require.Error(t, err)

	// should never go outside the index of known missing keys
	badBA = NewBitArray(3)
	badBA.Set(2)
	keys, err := request.GetMissingKeys(badBA.Bytes())
	require.NoError(t, err)
	require.Empty(t, keys)

	// test that get missing keys updates when we add a tx
	request.TryAddMissingTx(key1, tx1)
	ba.Set(1)
	key, err = request.GetMissingKeys(ba.Bytes())
	require.NoError(t, err)
	require.Equal(t, key, []types.TxKey{key2})
}

func TestBlockFetcherSimple(t *testing.T) {
	bf := NewBlockFetcher()
	tx := types.Tx("hello world")
	key := tx.Key()
	missingKeys := map[int]types.TxKey{
		0: key,
	}
	blockID := []byte("blockID")
	req := bf.NewRequest(blockID, 1, missingKeys, make([][]byte, 1))
	req2, ok := bf.GetRequest(blockID)
	require.True(t, ok)
	require.Equal(t, req, req2)
	// a different request for the same blockID should
	// return the same original request object.
	req3 := bf.NewRequest(blockID, 2, missingKeys, make([][]byte, 2))
	require.Equal(t, req, req3)

	req4 := bf.NewRequest([]byte("differentBlockID"), 1, missingKeys, make([][]byte, 1))

	bf.TryAddMissingTx(key, tx)
	require.False(t, req4.TryAddMissingTx(key, tx))
	require.True(t, req.IsDone())

	require.Len(t, bf.requests, 2)
	bf.PruneOldRequests(1)
	require.Len(t, bf.requests, 0)
}

func TestBlockFetcherPendingBitArrays(t *testing.T) {
	bf := NewBlockFetcher()
	blockID := []byte("blockID")

	ba := NewBitArray(2)
	ba.Set(0)
	ba.Set(1)
	_, exists := bf.GetRequest(blockID)
	require.False(t, exists)

	pending, ok := bf.PopPendingBitArrays(blockID)
	require.False(t, ok)
	require.Nil(t, pending)

	// two peers report the same bit array
	bf.AddPendingBitArray(blockID, ba.Bytes(), 1, 1)
	bf.AddPendingBitArray(blockID, ba.Bytes(), 2, 1)

	pending, ok = bf.PopPendingBitArrays(blockID)
	require.True(t, ok)
	require.Len(t, pending, 2)
	require.Equal(t, pending[1], ba.Bytes())

	bf.AddPendingBitArray(blockID, ba.Bytes(), 3, 1)

	require.Len(t, bf.pending, 1)
	bf.PruneOldRequests(1)
	require.Len(t, bf.pending, 0)
	require.Len(t, bf.pendingHeight, 0)
}

func TestBlockFetcherConcurrentRequests(t *testing.T) {
	var (
		bf                  = NewBlockFetcher()
		numBlocks           = 5
		numRequestsPerBlock = 5
		numTxs              = 5
		requestWG           = sync.WaitGroup{}
		goRoutinesWG        = sync.WaitGroup{}
		allTxs              = make([][]byte, numTxs)
		txs                 = make([][]byte, numTxs)
		missingKeys         = make(map[int]types.TxKey)
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < numTxs; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		allTxs[i] = tx
		if i%3 == 0 {
			txs[i] = tx
		} else {
			missingKeys[i] = tx.Key()
		}
	}

	for i := 0; i < numBlocks; i++ {
		requestWG.Add(1)
		for j := 0; j < numRequestsPerBlock; j++ {
			goRoutinesWG.Add(1)
			go func(blockID []byte, routine int) {
				defer goRoutinesWG.Done()
				// create a copy of the missingKeys and txs
				mk := make(map[int]types.TxKey)
				for i, k := range missingKeys {
					mk[i] = k
				}
				txsCopy := make([][]byte, len(txs))
				copy(txsCopy, txs)
				request := bf.NewRequest(blockID, 1, mk, txs)
				if routine == 0 {
					requestWG.Done()
				}
				request.WaitForBlock(ctx)
			}([]byte(fmt.Sprintf("blockID%d", i)), j)
		}
		goRoutinesWG.Add(1)
		go func() {
			defer goRoutinesWG.Done()
			// Wait until all the request have started
			requestWG.Wait()
			for _, tx := range allTxs {
				bf.TryAddMissingTx(types.Tx(tx).Key(), tx)
			}
		}()
	}
	goRoutinesWG.Wait()

	for i := 0; i < numBlocks; i++ {
		blockID := []byte(fmt.Sprintf("blockID%d", i))
		request, ok := bf.GetRequest(blockID)
		require.True(t, ok)
		require.True(t, request.IsDone())
		result, err := request.WaitForBlock(ctx)
		require.NoError(t, err)
		require.Equal(t, result, txs)
	}
}

func TestFetchTxsFromKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reactor, pool := setupReactor(t)

	numTxs := 10
	txs := make([][]byte, numTxs)
	keys := make([][]byte, numTxs)
	peer := genPeer()
	blockID := tmhash.Sum([]byte("blockID"))
	bitArray := NewBitArray(numTxs)
	peerBitArray := NewBitArray(numTxs)
	for i := 0; i < numTxs; i++ {
		tx := newDefaultTx(fmt.Sprintf("tx%d", i))
		txs[i] = tx
		key := tx.Key()
		keys[i] = key[:]
		// every 1 in 3 transactions proposed in the block, the node
		// already has in their mempool and doesn't need to fetch
		if i%3 == 0 {
			t.Log("adding tx to mempool", i)
			err := pool.CheckTx(tx, nil, mempool.TxInfo{})
			require.NoError(t, err)
			bitArray.Set(i)
			if i%6 == 0 {
				peerBitArray.Set(i)
			}
		} else {
			// the rest of the transactions we expect the node to request
			// from the only peer it is connected to
			peer.On("Send", MempoolStateChannel, genMsgWant(t, key)).Return(func(chID byte, msg []byte) bool {
				t.Log("received request for tx", "tx_index", i)
				reactor.ReceiveEnvelope(p2p.Envelope{
					Src:       peer,
					Message:   &memproto.Txs{Txs: [][]byte{tx}},
					ChannelID: mempool.MempoolChannel,
				})
				return true
			})
			peerBitArray.Set(i)
		}
	}
	hasBlockTxsMsg, err := (&memproto.Message{
		Sum: &memproto.Message_HasBlockTxs{
			HasBlockTxs: &memproto.HasBlockTxs{
				BlockId:     blockID,
				HasBitArray: bitArray.Bytes(),
			},
		},
	}).Marshal()
	require.NoError(t, err)
	peer.On("Send", MempoolStateChannel, hasBlockTxsMsg).Return(func(chID byte, hasBlockTxsMsg []byte) bool {
		// only after the node has sent the hasTxMsg from it's peer does the peer
		// broadcast what messages it has.
		t.Log("sending has block tx message to node")
		reactor.ReceiveEnvelope(p2p.Envelope{
			Src: peer,
			Message: &memproto.HasBlockTxs{
				BlockId:     blockID,
				HasBitArray: peerBitArray.Bytes(),
			},
			ChannelID: MempoolStateChannel,
		})
		return true
	})

	reactor.InitPeer(peer)

	resultTxs, err := reactor.FetchTxsFromKeys(ctx, blockID, keys)
	require.NoError(t, err)
	require.Equal(t, len(txs), len(resultTxs))
	for idx, tx := range resultTxs {
		require.Equal(t, txs[idx], tx)
	}
	repeatResult, err := reactor.FetchTxsFromKeys(ctx, blockID, keys)
	require.NoError(t, err)
	require.Equal(t, resultTxs, repeatResult)
	peer.AssertExpectations(t)
}
