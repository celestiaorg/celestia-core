package client_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/libs/log"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	mempl "github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	rpclocal "github.com/cometbft/cometbft/rpc/client/local"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpcclient "github.com/cometbft/cometbft/rpc/jsonrpc/client"
	rpctest "github.com/cometbft/cometbft/rpc/test"
	"github.com/cometbft/cometbft/types"
)

var (
	ctx = context.Background()
)

func getHTTPClient() *rpchttp.HTTP {
	rpcAddr := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.New(rpcAddr, "/websocket")
	if err != nil {
		panic(err)
	}
	c.SetLogger(log.TestingLogger())
	return c
}

func getHTTPClientWithTimeout(timeout uint) *rpchttp.HTTP {
	rpcAddr := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.NewWithTimeout(rpcAddr, "/websocket", timeout)
	if err != nil {
		panic(err)
	}
	c.SetLogger(log.TestingLogger())
	return c
}

func getLocalClient() *rpclocal.Local {
	return rpclocal.New(node)
}

// GetClients returns a slice of clients for table-driven tests
func GetClients() []client.Client {
	return []client.Client{
		getHTTPClient(),
		getLocalClient(),
	}
}

func TestNilCustomHTTPClient(t *testing.T) {
	require.Panics(t, func() {
		_, _ = rpchttp.NewWithClient("http://example.com", "/websocket", nil)
	})
	require.Panics(t, func() {
		_, _ = rpcclient.NewWithHTTPClient("http://example.com", nil)
	})
}

func TestCustomHTTPClient(t *testing.T) {
	remote := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.NewWithClient(remote, "/websocket", http.DefaultClient)
	require.Nil(t, err)
	status, err := c.Status(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status)
}

func TestCorsEnabled(t *testing.T) {
	origin := rpctest.GetConfig().RPC.CORSAllowedOrigins[0]
	remote := strings.ReplaceAll(rpctest.GetConfig().RPC.ListenAddress, "tcp", "http")

	req, err := http.NewRequest("GET", remote, nil)
	require.Nil(t, err, "%+v", err)
	req.Header.Set("Origin", origin)
	c := &http.Client{}
	resp, err := c.Do(req)
	require.Nil(t, err, "%+v", err)
	defer resp.Body.Close()

	assert.Equal(t, resp.Header.Get("Access-Control-Allow-Origin"), origin)
}

// Make sure status is correct (we connect properly)
func TestStatus(t *testing.T) {
	for i, c := range GetClients() {
		moniker := rpctest.GetConfig().Moniker
		status, err := c.Status(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.Equal(t, moniker, status.NodeInfo.Moniker)
	}
}

// Make sure info is correct (we connect properly)
func TestInfo(t *testing.T) {
	for i, c := range GetClients() {
		// status, err := c.Status()
		// require.Nil(t, err, "%+v", err)
		info, err := c.ABCIInfo(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		// TODO: this is not correct - fix merkleeyes!
		// assert.EqualValues(t, status.SyncInfo.LatestBlockHeight, info.Response.LastBlockHeight)
		assert.True(t, strings.Contains(info.Response.Data, "size"))
	}
}

func TestNetInfo(t *testing.T) {
	for i, c := range GetClients() {
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		netinfo, err := nc.NetInfo(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.True(t, netinfo.Listening)
		assert.Equal(t, 0, len(netinfo.Peers))
	}
}

func TestDumpConsensusState(t *testing.T) {
	for i, c := range GetClients() {
		// FIXME: fix server so it doesn't panic on invalid input
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		cons, err := nc.DumpConsensusState(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.NotEmpty(t, cons.RoundState)
		assert.Empty(t, cons.Peers)
	}
}

func TestConsensusState(t *testing.T) {
	for i, c := range GetClients() {
		// FIXME: fix server so it doesn't panic on invalid input
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		cons, err := nc.ConsensusState(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.NotEmpty(t, cons.RoundState)
	}
}

func TestHealth(t *testing.T) {
	for i, c := range GetClients() {
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		_, err := nc.Health(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
	}
}

func TestGenesisAndValidators(t *testing.T) {
	for i, c := range GetClients() {

		// make sure this is the right genesis file
		gen, err := c.Genesis(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		// get the genesis validator
		require.Equal(t, 1, len(gen.Genesis.Validators))
		gval := gen.Genesis.Validators[0]

		// get the current validators
		h := int64(1)
		vals, err := c.Validators(context.Background(), &h, nil, nil)
		require.Nil(t, err, "%d: %+v", i, err)
		require.Equal(t, 1, len(vals.Validators))
		require.Equal(t, 1, vals.Count)
		require.Equal(t, 1, vals.Total)
		val := vals.Validators[0]

		// make sure the current set is also the genesis set
		assert.Equal(t, gval.Power, val.VotingPower)
		assert.Equal(t, gval.PubKey, val.PubKey)
	}
}

func TestGenesisChunked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, c := range GetClients() {
		first, err := c.GenesisChunked(ctx, 0)
		require.NoError(t, err)

		decoded := make([]string, 0, first.TotalChunks)
		for i := 0; i < first.TotalChunks; i++ {
			chunk, err := c.GenesisChunked(ctx, uint(i))
			require.NoError(t, err)
			data, err := base64.StdEncoding.DecodeString(chunk.Data)
			require.NoError(t, err)
			decoded = append(decoded, string(data))

		}
		doc := []byte(strings.Join(decoded, ""))

		var out types.GenesisDoc
		require.NoError(t, cmtjson.Unmarshal(doc, &out),
			"first: %+v, doc: %s", first, string(doc))
	}
}

func TestABCIQuery(t *testing.T) {
	for i, c := range GetClients() {
		// write something
		k, v, tx := MakeTxKV()
		bres, err := c.BroadcastTxCommit(context.Background(), tx)
		require.Nil(t, err, "%d: %+v", i, err)
		apph := bres.Height + 1 // this is where the tx will be applied to the state

		// wait before querying
		err = client.WaitForHeight(c, apph, nil)
		require.NoError(t, err)
		res, err := c.ABCIQuery(context.Background(), "/key", k)
		qres := res.Response
		if assert.Nil(t, err) && assert.True(t, qres.IsOK()) {
			assert.EqualValues(t, v, qres.Value)
		}
	}
}

// Make some app checks
func TestAppCalls(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	for i, c := range GetClients() {

		// get an offset of height to avoid racing and guessing
		s, err := c.Status(context.Background())
		require.NoError(err)
		// sh is start height or status height
		sh := s.SyncInfo.LatestBlockHeight

		// look for the future
		h := sh + 20
		_, err = c.Block(context.Background(), &h)
		require.Error(err) // no block yet

		// write something
		k, v, tx := MakeTxKV()
		bres, err := c.BroadcastTxCommit(context.Background(), tx)
		require.NoError(err)
		require.True(bres.TxResult.IsOK())
		txh := bres.Height
		apph := txh + 1 // this is where the tx will be applied to the state

		// wait before querying
		err = client.WaitForHeight(c, apph, nil)
		require.NoError(err)

		_qres, err := c.ABCIQueryWithOptions(context.Background(), "/key", k, client.ABCIQueryOptions{Prove: false})
		require.NoError(err)
		qres := _qres.Response
		if assert.True(qres.IsOK()) {
			assert.Equal(k, qres.Key)
			assert.EqualValues(v, qres.Value)
		}

		// make sure we can lookup the tx with proof
		ptx, err := c.Tx(context.Background(), bres.Hash, true)
		require.NoError(err)
		assert.EqualValues(txh, ptx.Height)
		assert.EqualValues(tx, ptx.Tx)

		// and we can even check the block is added
		block, err := c.Block(context.Background(), &apph)
		require.NoError(err)
		appHash := block.Block.Header.AppHash //nolint:staticcheck
		assert.True(len(appHash) > 0)
		assert.EqualValues(apph, block.Block.Header.Height) //nolint:staticcheck

		blockByHash, err := c.BlockByHash(context.Background(), block.BlockID.Hash)
		require.NoError(err)
		require.Equal(block, blockByHash)

		// check that the header matches the block hash
		header, err := c.Header(context.Background(), &apph)
		require.NoError(err)
		require.Equal(block.Block.Header, *header.Header)

		headerByHash, err := c.HeaderByHash(context.Background(), block.BlockID.Hash)
		require.NoError(err)
		require.Equal(header, headerByHash)

		// now check the results
		blockResults, err := c.BlockResults(context.Background(), &txh)
		require.Nil(err, "%d: %+v", i, err)
		assert.Equal(txh, blockResults.Height)
		if assert.Equal(1, len(blockResults.TxsResults)) {
			// check success code
			assert.EqualValues(0, blockResults.TxsResults[0].Code)
		}

		// check blockchain info, now that we know there is info
		info, err := c.BlockchainInfo(context.Background(), apph, apph)
		require.NoError(err)
		assert.True(info.LastHeight >= apph)
		if assert.Equal(1, len(info.BlockMetas)) {
			lastMeta := info.BlockMetas[0]
			assert.EqualValues(apph, lastMeta.Header.Height)
			blockData := block.Block
			assert.Equal(blockData.Header.AppHash, lastMeta.Header.AppHash) //nolint:staticcheck
			assert.Equal(block.BlockID, lastMeta.BlockID)
		}

		// and get the corresponding commit with the same apphash
		commit, err := c.Commit(context.Background(), &apph)
		require.NoError(err)
		cappHash := commit.Header.AppHash //nolint:staticcheck
		assert.Equal(appHash, cappHash)
		assert.NotNil(commit.Commit)

		// compare the commits (note Commit(2) has commit from Block(3))
		h = apph - 1
		commit2, err := c.Commit(context.Background(), &h)
		require.NoError(err)
		assert.Equal(block.Block.LastCommitHash, commit2.Commit.Hash())

		// and we got a proof that works!
		_pres, err := c.ABCIQueryWithOptions(context.Background(), "/key", k, client.ABCIQueryOptions{Prove: true})
		require.NoError(err)
		pres := _pres.Response
		assert.True(pres.IsOK())

		// XXX Test proof
	}
}

func TestBroadcastTxSync(t *testing.T) {
	require := require.New(t)

	// TODO (melekes): use mempool which is set on RPC rather than getting it from node
	mempool := node.Mempool()
	initMempoolSize := mempool.Size()

	for i, c := range GetClients() {
		_, _, tx := MakeTxKV()
		bres, err := c.BroadcastTxSync(context.Background(), tx)
		require.Nil(err, "%d: %+v", i, err)
		require.Equal(bres.Code, abci.CodeTypeOK) // FIXME

		require.Equal(initMempoolSize+1, mempool.Size())

		txs := mempool.ReapMaxTxs(len(tx))
		require.EqualValues(tx, txs[0].Tx)
		mempool.Flush()
	}
}

func TestBroadcastTxCommit(t *testing.T) {
	require := require.New(t)

	mempool := node.Mempool()
	for i, c := range GetClients() {
		_, _, tx := MakeTxKV()
		bres, err := c.BroadcastTxCommit(context.Background(), tx)
		require.Nil(err, "%d: %+v", i, err)
		require.True(bres.CheckTx.IsOK())
		require.True(bres.TxResult.IsOK())

		require.Equal(0, mempool.Size())
	}
}

func TestUnconfirmedTxs(t *testing.T) {
	_, _, tx := MakeTxKV()

	ch := make(chan *abci.ResponseCheckTx, 1)
	mempool := node.Mempool()
	err := mempool.CheckTx(tx, func(resp *abci.ResponseCheckTx) { ch <- resp }, mempl.TxInfo{})
	require.NoError(t, err)

	// wait for tx to arrive in mempoool.
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Error("Timed out waiting for CheckTx callback")
	}

	for _, c := range GetClients() {
		mc := c.(client.MempoolClient)
		limit := 1
		res, err := mc.UnconfirmedTxs(context.Background(), &limit)
		require.NoError(t, err)

		assert.Equal(t, 1, res.Count)
		assert.Equal(t, 1, res.Total)
		assert.Equal(t, mempool.SizeBytes(), res.TotalBytes)
		assert.Exactly(t, types.Txs{tx}, types.Txs(res.Txs))
	}

	mempool.Flush()
}

func TestUncappedUnconfirmedTxs(t *testing.T) {
	mempool := node.Mempool()
	numberOfTransactions := 120 // needs to be greater than maxPerPage const
	for i := 0; i < numberOfTransactions; i++ {
		_, _, tx := MakeTxKV()

		ch := make(chan *abci.ResponseCheckTx, 1)
		err := mempool.CheckTx(tx, func(resp *abci.ResponseCheckTx) { ch <- resp }, mempl.TxInfo{})
		require.NoError(t, err)

		// wait for tx to arrive in mempoool.
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Error("Timed out waiting for CheckTx callback")
		}
	}

	for _, c := range GetClients() {
		mc := c.(client.MempoolClient)
		limit := -1 // set the limit to -1 to return everything
		res, err := mc.UnconfirmedTxs(context.Background(), &limit)
		require.NoError(t, err)

		assert.Equal(t, numberOfTransactions, res.Count)
		assert.Equal(t, numberOfTransactions, res.Total)
		assert.Equal(t, mempool.SizeBytes(), res.TotalBytes)
	}

	mempool.Flush()
}

func TestNumUnconfirmedTxs(t *testing.T) {
	_, _, tx := MakeTxKV()

	ch := make(chan *abci.ResponseCheckTx, 1)
	mempool := node.Mempool()
	err := mempool.CheckTx(tx, func(resp *abci.ResponseCheckTx) { ch <- resp }, mempl.TxInfo{})
	require.NoError(t, err)

	// wait for tx to arrive in mempoool.
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Error("Timed out waiting for CheckTx callback")
	}

	mempoolSize := mempool.Size()
	for i, c := range GetClients() {
		mc, ok := c.(client.MempoolClient)
		require.True(t, ok, "%d", i)
		res, err := mc.NumUnconfirmedTxs(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)

		assert.Equal(t, mempoolSize, res.Count)
		assert.Equal(t, mempoolSize, res.Total)
		assert.Equal(t, mempool.SizeBytes(), res.TotalBytes)
	}

	mempool.Flush()
}

func TestCheckTx(t *testing.T) {
	mempool := node.Mempool()

	for _, c := range GetClients() {
		_, _, tx := MakeTxKV()

		res, err := c.CheckTx(context.Background(), tx)
		require.NoError(t, err)
		assert.Equal(t, abci.CodeTypeOK, res.Code)

		assert.Equal(t, 0, mempool.Size(), "mempool must be empty")
	}
}

func TestTx(t *testing.T) {
	// first we broadcast a tx
	c := getHTTPClient()
	_, _, tx := MakeTxKV()
	bres, err := c.BroadcastTxCommit(context.Background(), tx)
	require.Nil(t, err, "%+v", err)

	txHeight := bres.Height
	txHash := bres.Hash

	anotherTxHash := types.Tx("a different tx").Hash()

	cases := []struct {
		valid bool
		prove bool
		hash  []byte
	}{
		// only valid if correct hash provided
		{true, false, txHash},
		{true, true, txHash},
		{false, false, anotherTxHash},
		{false, true, anotherTxHash},
		{false, false, nil},
		{false, true, nil},
	}

	for i, c := range GetClients() {
		for j, tc := range cases {
			t.Logf("client %d, case %d", i, j)

			// now we query for the tx.
			// since there's only one tx, we know index=0.
			ptx, err := c.Tx(context.Background(), tc.hash, tc.prove)

			if !tc.valid {
				require.NotNil(t, err)
			} else {
				require.Nil(t, err, "%+v", err)
				assert.EqualValues(t, txHeight, ptx.Height)
				assert.EqualValues(t, tx, ptx.Tx)
				assert.Zero(t, ptx.Index)
				assert.True(t, ptx.TxResult.IsOK())
				assert.EqualValues(t, txHash, ptx.Hash)

			}
		}
	}
}

func TestTxSearchWithTimeout(t *testing.T) {
	// Get a client with a time-out of 10 secs.
	timeoutClient := getHTTPClientWithTimeout(10)

	_, _, tx := MakeTxKV()
	_, err := timeoutClient.BroadcastTxCommit(context.Background(), tx)
	require.NoError(t, err)

	// query using a compositeKey (see kvstore application)
	result, err := timeoutClient.TxSearch(context.Background(), "app.creator='Cosmoshi Netowoko'", false, nil, nil, "asc")
	require.Nil(t, err)
	require.Greater(t, len(result.Txs), 0, "expected a lot of transactions")
}

// This test does nothing if we do not call app.SetGenBlockEvents() within main_test.go
// It will nevertheless pass as there are no events being generated.
func TestBlockSearch(t *testing.T) {
	c := getHTTPClient()

	// first we broadcast a few txs
	for i := 0; i < 10; i++ {
		_, _, tx := MakeTxKV()

		_, err := c.BroadcastTxCommit(context.Background(), tx)
		require.NoError(t, err)
	}
	require.NoError(t, client.WaitForHeight(c, 5, nil))
	result, err := c.BlockSearch(context.Background(), "begin_event.foo = 100", nil, nil, "asc")
	require.NoError(t, err)
	blockCount := len(result.Blocks)
	// if we generate block events within the test (by uncommenting
	// the code in line main_test.go:L23) then we expect len(result.Blocks)
	// to be at least 5
	// require.GreaterOrEqual(t, blockCount, 5)

	// otherwise it is 0
	require.Equal(t, blockCount, 0)

}
func TestTxSearch(t *testing.T) {
	c := getHTTPClient()
	status, err := c.Status(context.Background())
	require.NoError(t, err)
	// first we broadcast a few txs
	for i := 0; i < 10; i++ {
		_, _, tx := MakeTxKV()
		_, err := c.BroadcastTxCommit(context.Background(), tx)
		require.NoError(t, err)
	}

	// since we're not using an isolated test server, we'll have lingering transactions
	// from other tests as well
	result, err := c.TxSearch(context.Background(), fmt.Sprintf("tx.height >= %d", status.SyncInfo.LatestBlockHeight), true, nil, nil, "asc")
	require.NoError(t, err)
	txCount := len(result.Txs)

	// pick out the last tx to have something to search for in tests
	find := result.Txs[len(result.Txs)-1]
	anotherTxHash := types.Tx("a different tx").Hash()

	for _, c := range GetClients() {

		// now we query for the tx.
		result, err := c.TxSearch(context.Background(), fmt.Sprintf("tx.hash='%v'", find.Hash), true, nil, nil, "asc")
		require.Nil(t, err)
		require.Len(t, result.Txs, 1)
		require.Equal(t, find.Hash, result.Txs[0].Hash)

		ptx := result.Txs[0]
		assert.EqualValues(t, find.Height, ptx.Height)
		assert.EqualValues(t, find.Tx, ptx.Tx)
		assert.Zero(t, ptx.Index)
		assert.True(t, ptx.TxResult.IsOK())
		assert.EqualValues(t, find.Hash, ptx.Hash)

		// // time to verify the proof
		// if assert.EqualValues(t, find.Tx, ptx.Proof.Data) {
		// 	assert.NoError(t, ptx.Proof.Proof.Verify(ptx.Proof.RootHash, find.Hash))
		// }

		// query by height
		result, err = c.TxSearch(context.Background(), fmt.Sprintf("tx.height=%d", find.Height), true, nil, nil, "asc")
		require.Nil(t, err)
		require.Len(t, result.Txs, 1)

		// query for non existing tx
		result, err = c.TxSearch(context.Background(), fmt.Sprintf("tx.hash='%X'", anotherTxHash), false, nil, nil, "asc")
		require.Nil(t, err)
		require.Len(t, result.Txs, 0)

		// query using a compositeKey (see kvstore application)
		result, err = c.TxSearch(context.Background(), "app.creator='Cosmoshi Netowoko'", false, nil, nil, "asc")
		require.Nil(t, err)
		require.Greater(t, len(result.Txs), 0, "expected a lot of transactions")

		// query using an index key
		result, err = c.TxSearch(context.Background(), "app.index_key='index is working'", false, nil, nil, "asc")
		require.Nil(t, err)
		require.Greater(t, len(result.Txs), 0, "expected a lot of transactions")

		// query using an noindex key
		result, err = c.TxSearch(context.Background(), "app.noindex_key='index is working'", false, nil, nil, "asc")
		require.Nil(t, err)
		require.Equal(t, len(result.Txs), 0, "expected a lot of transactions")

		// query using a compositeKey (see kvstore application) and height
		result, err = c.TxSearch(context.Background(),
			"app.creator='Cosmoshi Netowoko' AND tx.height<10000", true, nil, nil, "asc")
		require.Nil(t, err)
		require.Greater(t, len(result.Txs), 0, "expected a lot of transactions")

		// query a non existing tx with page 1 and txsPerPage 1
		perPage := 1
		result, err = c.TxSearch(context.Background(), "app.creator='Cosmoshi Neetowoko'", true, nil, &perPage, "asc")
		require.Nil(t, err)
		require.Len(t, result.Txs, 0)

		// check sorting
		result, err = c.TxSearch(context.Background(), fmt.Sprintf("tx.height >= %d", status.SyncInfo.LatestBlockHeight), false, nil, nil, "asc")
		require.Nil(t, err)
		for k := 0; k < len(result.Txs)-1; k++ {
			require.LessOrEqual(t, result.Txs[k].Height, result.Txs[k+1].Height)
			require.LessOrEqual(t, result.Txs[k].Index, result.Txs[k+1].Index)
		}

		result, err = c.TxSearch(context.Background(), fmt.Sprintf("tx.height >= %d", status.SyncInfo.LatestBlockHeight), false, nil, nil, "desc")
		require.Nil(t, err)
		for k := 0; k < len(result.Txs)-1; k++ {
			require.GreaterOrEqual(t, result.Txs[k].Height, result.Txs[k+1].Height)
			require.GreaterOrEqual(t, result.Txs[k].Index, result.Txs[k+1].Index)
		}
		// check pagination
		perPage = 3
		var (
			seen      = map[int64]bool{}
			maxHeight int64
			pages     = int(math.Ceil(float64(txCount) / float64(perPage)))
		)

		totalTx := 0
		for page := 1; page <= pages; page++ {
			page := page
			result, err := c.TxSearch(context.Background(), fmt.Sprintf("tx.height >= %d", status.SyncInfo.LatestBlockHeight), true, &page, &perPage, "asc")
			require.NoError(t, err)
			if page < pages {
				require.Len(t, result.Txs, perPage)
			} else {
				require.LessOrEqual(t, len(result.Txs), perPage)
			}
			totalTx = totalTx + len(result.Txs)
			for _, tx := range result.Txs {
				require.False(t, seen[tx.Height],
					"Found duplicate height %v in page %v", tx.Height, page)
				require.Greater(t, tx.Height, maxHeight,
					"Found decreasing height %v (max seen %v) in page %v", tx.Height, maxHeight, page)
				seen[tx.Height] = true
				maxHeight = tx.Height
			}
		}
		require.Equal(t, txCount, totalTx)
		require.Len(t, seen, txCount)
	}
}

func TestBatchedJSONRPCCalls(t *testing.T) {
	c := getHTTPClient()
	testBatchedJSONRPCCalls(t, c)
}

func testBatchedJSONRPCCalls(t *testing.T, c *rpchttp.HTTP) {
	k1, v1, tx1 := MakeTxKV()
	k2, v2, tx2 := MakeTxKV()

	batch := c.NewBatch()
	r1, err := batch.BroadcastTxCommit(context.Background(), tx1)
	require.NoError(t, err)
	r2, err := batch.BroadcastTxCommit(context.Background(), tx2)
	require.NoError(t, err)
	require.Equal(t, 2, batch.Count())
	bresults, err := batch.Send(ctx)
	require.NoError(t, err)
	require.Len(t, bresults, 2)
	require.Equal(t, 0, batch.Count())

	bresult1, ok := bresults[0].(*ctypes.ResultBroadcastTxCommit)
	require.True(t, ok)
	require.Equal(t, *bresult1, *r1)
	bresult2, ok := bresults[1].(*ctypes.ResultBroadcastTxCommit)
	require.True(t, ok)
	require.Equal(t, *bresult2, *r2)
	apph := cmtmath.MaxInt64(bresult1.Height, bresult2.Height) + 1

	err = client.WaitForHeight(c, apph, nil)
	require.NoError(t, err)

	q1, err := batch.ABCIQuery(context.Background(), "/key", k1)
	require.NoError(t, err)
	q2, err := batch.ABCIQuery(context.Background(), "/key", k2)
	require.NoError(t, err)
	require.Equal(t, 2, batch.Count())
	qresults, err := batch.Send(ctx)
	require.NoError(t, err)
	require.Len(t, qresults, 2)
	require.Equal(t, 0, batch.Count())

	qresult1, ok := qresults[0].(*ctypes.ResultABCIQuery)
	require.True(t, ok)
	require.Equal(t, *qresult1, *q1)
	qresult2, ok := qresults[1].(*ctypes.ResultABCIQuery)
	require.True(t, ok)
	require.Equal(t, *qresult2, *q2)

	require.Equal(t, qresult1.Response.Key, k1)
	require.Equal(t, qresult2.Response.Key, k2)
	require.Equal(t, qresult1.Response.Value, v1)
	require.Equal(t, qresult2.Response.Value, v2)
}

func TestBatchedJSONRPCCallsCancellation(t *testing.T) {
	c := getHTTPClient()
	_, _, tx1 := MakeTxKV()
	_, _, tx2 := MakeTxKV()

	batch := c.NewBatch()
	_, err := batch.BroadcastTxCommit(context.Background(), tx1)
	require.NoError(t, err)
	_, err = batch.BroadcastTxCommit(context.Background(), tx2)
	require.NoError(t, err)
	// we should have 2 requests waiting
	require.Equal(t, 2, batch.Count())
	// we want to make sure we cleared 2 pending requests
	require.Equal(t, 2, batch.Clear())
	// now there should be no batched requests
	require.Equal(t, 0, batch.Count())
}

func TestSendingEmptyRequestBatch(t *testing.T) {
	c := getHTTPClient()
	batch := c.NewBatch()
	_, err := batch.Send(ctx)
	require.Error(t, err, "sending an empty batch of JSON RPC requests should result in an error")
}

func TestClearingEmptyRequestBatch(t *testing.T) {
	c := getHTTPClient()
	batch := c.NewBatch()
	require.Zero(t, batch.Clear(), "clearing an empty batch of JSON RPC requests should result in a 0 result")
}

func TestConcurrentJSONRPCBatching(t *testing.T) {
	var wg sync.WaitGroup
	c := getHTTPClient()
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testBatchedJSONRPCCalls(t, c)
		}()
	}
	wg.Wait()
}

func TestTxStatus(t *testing.T) {
	c := getHTTPClient()
	require := require.New(t)
	mempool := node.Mempool()

	// Create a new transaction
	_, _, tx := MakeTxKV()

	// Get the initial size of the mempool
	initMempoolSize := mempool.Size()

	// Add the transaction to the mempool
	err := mempool.CheckTx(tx, nil, mempl.TxInfo{})
	require.NoError(err)

	// Check if the size of the mempool has increased
	require.Equal(initMempoolSize+1, mempool.Size())

	// Get the tx status from the mempool
	result, err := c.TxStatus(context.Background(), types.Tx(tx).Hash())
	require.NoError(err)
	require.EqualValues(0, result.Height)
	require.EqualValues(0, result.Index)
	require.Equal("PENDING", result.Status)

	// Flush the mempool
	mempool.Flush()
	require.Equal(0, mempool.Size())

	// Get tx status after flushing it from the mempool
	result, err = c.TxStatus(context.Background(), types.Tx(tx).Hash())
	require.NoError(err)
	require.EqualValues(0, result.Height)
	require.EqualValues(0, result.Index)
	require.Equal("UNKNOWN", result.Status)

	// Broadcast the tx again
	bres, err := c.BroadcastTxCommit(context.Background(), tx)
	require.NoError(err)
	require.True(bres.CheckTx.IsOK())
	require.True(bres.TxResult.IsOK())

	// Submit a malformed tx
	malformedTx := []byte("malformed-tx")
	_, err = c.BroadcastTxCommit(context.Background(), malformedTx)
	require.Error(err)

	// Get the tx status
	malformedTxResult, err := c.TxStatus(context.Background(), types.Tx(malformedTx).Hash())
	require.NoError(err)
	require.EqualValues(uint32(2), malformedTxResult.ExecutionCode)
	require.Equal("REJECTED", malformedTxResult.Status)

	// Get the tx status
	result, err = c.TxStatus(context.Background(), types.Tx(tx).Hash())
	require.NoError(err)
	require.EqualValues(bres.Height, result.Height)
	require.EqualValues(0, result.Index)
	require.Equal("COMMITTED", result.Status)
	require.Equal(abci.CodeTypeOK, result.ExecutionCode)
	require.Equal("", result.Error)
}

func TestDataCommitment(t *testing.T) {
	c := getHTTPClient()

	// first we broadcast a few tx
	expectedHeight := int64(3)
	var bres *ctypes.ResultBroadcastTxCommit
	var err error
	for i := int64(0); i < expectedHeight; i++ {
		_, _, tx := MakeTxKV()
		bres, err = c.BroadcastTxCommit(context.Background(), tx)
		require.Nil(t, err, "%+v when submitting tx %d", err, i)
	}

	// check if height >= 3
	actualHeight := bres.Height
	require.LessOrEqual(t, expectedHeight, actualHeight, "couldn't create enough blocks for testing the commitment.")

	// check if data commitment is not nil.
	// Checking if the commitment is correct is done in `core/blocks_test.go`.
	dataCommitment, err := c.DataCommitment(ctx, 1, uint64(expectedHeight))
	require.NotNil(t, dataCommitment, "data commitment shouldn't be nul.")
	require.Nil(t, err, "%+v when creating data commitment.", err)
}
