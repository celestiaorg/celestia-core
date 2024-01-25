package cat

import (
	"context"
	"fmt"
	"math/rand"
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
	_, err := request.WaitForBlock(shortCtx)
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
				_, _ = request.WaitForBlock(ctx)
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
	wg := sync.WaitGroup{}
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
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(time.Duration(rand.Int63n(100)) * time.Millisecond)
				reactor.ReceiveEnvelope(p2p.Envelope{
					Src:       peer,
					Message:   &memproto.Txs{Txs: [][]byte{tx}},
					ChannelID: mempool.MempoolChannel,
				})
			}()
		}
	}

	reactor.InitPeer(peer)

	go func() {
		reactor.ReceiveEnvelope(p2p.Envelope{
			Src:       peer,
			Message:   &memproto.Txs{Txs: txs},
			ChannelID: mempool.MempoolChannel,
		})
	}()

	resultTxs, err := reactor.FetchTxsFromKeys(ctx, blockID, keys)
	require.NoError(t, err)
	require.Equal(t, len(txs), len(resultTxs))
	for idx, tx := range resultTxs {
		require.Equal(t, txs[idx], tx)
	}
	repeatResult, err := reactor.FetchTxsFromKeys(ctx, blockID, keys)
	require.NoError(t, err)
	require.Equal(t, resultTxs, repeatResult)
	wg.Wait()
}
