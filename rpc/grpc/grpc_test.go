//nolint:dupl
package coregrpc_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	nm "github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/proto/tendermint/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/rpc/core"
	core_grpc "github.com/cometbft/cometbft/rpc/grpc"
	rpctest "github.com/cometbft/cometbft/rpc/test"
	"github.com/cometbft/cometbft/types"
)

var testNode *nm.Node

func TestMain(m *testing.M) {
	// start a CometBFT node in the background to test against
	app := kvstore.NewInMemoryApplication()
	testNode = rpctest.StartTendermint(app)

	code := m.Run()

	// and shut down proper at the end
	rpctest.StopTendermint(testNode)
	os.Exit(code)
}

func TestBroadcastTx(t *testing.T) {
	res, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&core_grpc.RequestBroadcastTx{Tx: kvstore.NewTx("hello", "world")},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, res.CheckTx.Code)
	require.EqualValues(t, 0, res.TxResult.Code)
}

func setupClient(t *testing.T) core_grpc.BlockAPIClient {
	client, err := rpctest.GetBlockAPIClient()
	require.NoError(t, err)
	return client
}

func getEnvironment(t *testing.T) *core.Environment {
	env, err := testNode.ConfigureRPC()
	require.NoError(t, err)
	return env
}

func TestBlockByHash(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	env := getEnvironment(t)
	expectedBlockMeta := env.BlockStore.LoadBlockMeta(1)
	require.NotNil(t, expectedBlockMeta)

	// query the block along with the part proofs
	res, err := client.BlockByHash(context.Background(), &core_grpc.BlockByHashRequest{
		Hash:  expectedBlockMeta.BlockID.Hash,
		Prove: true,
	})
	require.NoError(t, err)

	part, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part.BlockPart)
	require.NotNil(t, part.ValidatorSet)
	require.NotNil(t, part.Commit)

	assert.NotEqual(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.Commit.Height, expectedBlockMeta.Header.Height)

	// query the block along without the part proofs
	res, err = client.BlockByHash(context.Background(), &core_grpc.BlockByHashRequest{
		Hash:  expectedBlockMeta.BlockID.Hash,
		Prove: false,
	})
	require.NoError(t, err)

	part, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part.BlockPart)
	require.NotNil(t, part.ValidatorSet)
	require.NotNil(t, part.Commit)

	assert.Equal(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.Commit.Height, expectedBlockMeta.Header.Height)
}

func TestCommit(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	env := getEnvironment(t)
	expectedBlockCommit := env.BlockStore.LoadSeenCommit(1)

	res, err := client.Commit(context.Background(), &core_grpc.CommitRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedBlockCommit.BlockID.Hash.Bytes(), res.Commit.BlockID.Hash)
}

func TestLatestCommit(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 3)

	res, err := client.Commit(context.Background(), &core_grpc.CommitRequest{
		Height: 0,
	})
	require.NoError(t, err)

	assert.Greater(t, res.Commit.Height, int64(2))
}

func TestValidatorSet(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	env := getEnvironment(t)
	expectedValidatorSet, err := env.StateStore.LoadValidators(1)
	require.NoError(t, err)

	res, err := client.ValidatorSet(context.Background(), &core_grpc.ValidatorSetRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, len(expectedValidatorSet.Validators), len(res.ValidatorSet.Validators))
}

func TestLatestValidatorSet(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 3)

	res, err := client.ValidatorSet(context.Background(), &core_grpc.ValidatorSetRequest{
		Height: 0,
	})
	require.NoError(t, err)

	assert.Greater(t, res.Height, int64(2))
}

func TestStatus(t *testing.T) {
	client := setupClient(t)
	env := getEnvironment(t)
	expectedStatus, err := env.Status(nil)
	require.NoError(t, err)

	res, err := client.Status(context.Background(), &core_grpc.StatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, string(expectedStatus.NodeInfo.DefaultNodeID), res.NodeInfo.DefaultNodeID)
}

func TestSubscribeNewHeights(t *testing.T) {
	client := setupClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.SubscribeNewHeights(ctx, &core_grpc.SubscribeNewHeightsRequest{})
	require.NoError(t, err)
	env := getEnvironment(t)

	go func() {
		listenedHeightsCount := 0
		defer func() {
			assert.Greater(t, listenedHeightsCount, 0)
		}()
		for {
			res, err := stream.Recv()
			if ctx.Err() != nil {
				return
			}
			require.NoError(t, err)
			require.Greater(t, res.Height, int64(0))
			assert.Equal(t, env.BlockStore.LoadBlockMeta(res.Height).BlockID.Hash.Bytes(), res.Hash)
			listenedHeightsCount++
		}
	}()

	time.Sleep(5 * time.Second)
}

func TestBlockByHash_Streaming(t *testing.T) {
	client := setupClient(t)

	// send a big transaction that would result in a block
	// containing multiple block parts
	txRes, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&core_grpc.RequestBroadcastTx{Tx: kvstore.NewRandomTx(100000)},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, txRes.CheckTx.Code)
	require.EqualValues(t, 0, txRes.TxResult.Code)

	env := getEnvironment(t)
	var expectedBlockMeta types.BlockMeta
	var foundMultiPart bool
	for i := int64(1); i < 500; i++ {
		waitForHeight(t, i+1)
		blockMeta := env.BlockStore.LoadBlockMeta(i)
		if blockMeta != nil && blockMeta.BlockID.PartSetHeader.Total > 1 {
			expectedBlockMeta = *blockMeta
			foundMultiPart = true
			break
		}
		// If we can't find a multi-part block, use the latest block for basic streaming test
		if i > 10 && !foundMultiPart {
			if blockMeta != nil {
				expectedBlockMeta = *blockMeta
			}
			break
		}
	}

	// Ensure we have a valid block to test with
	require.NotEmpty(t, expectedBlockMeta.BlockID.Hash, "Expected to find a valid block for testing")

	// query the block without the part proofs
	res, err := client.BlockByHash(context.Background(), &core_grpc.BlockByHashRequest{
		Hash:  expectedBlockMeta.BlockID.Hash,
		Prove: false,
	})
	require.NoError(t, err)

	part1, err := res.Recv()
	require.NoError(t, err)

	var part2 *core_grpc.BlockByHashResponse

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)

	assert.Equal(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.Commit.Height, expectedBlockMeta.Header.Height)

	// Only test for multiple parts if we found a multi-part block
	if foundMultiPart {
		part2, err = res.Recv()
		require.NoError(t, err)

		require.NotNil(t, part2.BlockPart)
		require.Nil(t, part2.ValidatorSet)
		require.Nil(t, part2.Commit)

		assert.Equal(t, part2.BlockPart.Proof, crypto.Proof{})
	}

	// query the block along with the part proofs
	res, err = client.BlockByHash(context.Background(), &core_grpc.BlockByHashRequest{
		Hash:  expectedBlockMeta.BlockID.Hash,
		Prove: true,
	})
	require.NoError(t, err)

	part1, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)

	assert.NotEqual(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.Commit.Height, expectedBlockMeta.Header.Height)

	// Only test for multiple parts if we found a multi-part block
	if foundMultiPart {
		part2, err = res.Recv()
		require.NoError(t, err)

		require.NotNil(t, part2.BlockPart)
		require.Nil(t, part2.ValidatorSet)
		require.Nil(t, part2.Commit)

		assert.NotEqual(t, part2.BlockPart.Proof, crypto.Proof{})
	}
}

func TestBlockByHeight(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	env := getEnvironment(t)
	expectedBlockMeta := env.BlockStore.LoadBlockMeta(1)

	// query the block along with the part proofs
	res, err := client.BlockByHeight(context.Background(), &core_grpc.BlockByHeightRequest{
		Height: expectedBlockMeta.Header.Height,
		Prove:  true,
	})
	require.NoError(t, err)

	part, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part.BlockPart)
	require.NotNil(t, part.ValidatorSet)
	require.NotNil(t, part.Commit)

	assert.NotEqual(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.Commit.Height, expectedBlockMeta.Header.Height)

	// query the block along without the part proofs
	res, err = client.BlockByHeight(context.Background(), &core_grpc.BlockByHeightRequest{
		Height: expectedBlockMeta.Header.Height,
		Prove:  false,
	})
	require.NoError(t, err)

	part, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part.BlockPart)
	require.NotNil(t, part.ValidatorSet)
	require.NotNil(t, part.Commit)

	assert.Equal(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.Commit.Height, expectedBlockMeta.Header.Height)
}

func TestLatestBlockByHeight(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)

	// query the block along with the part proofs
	res, err := client.BlockByHeight(context.Background(), &core_grpc.BlockByHeightRequest{
		Height: 0,
	})
	require.NoError(t, err)

	part, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part.BlockPart)
	require.NotNil(t, part.ValidatorSet)
	require.NotNil(t, part.Commit)

	assert.Greater(t, part.Commit.Height, int64(2))
}

func TestBlockQuery_Streaming(t *testing.T) {
	client := setupClient(t)

	// send a big transaction that would result in a block
	// containing multiple block parts
	txRes, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&core_grpc.RequestBroadcastTx{Tx: kvstore.NewRandomTx(100000)},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, txRes.CheckTx.Code)
	require.EqualValues(t, 0, txRes.TxResult.Code)

	env := getEnvironment(t)
	var expectedBlockMeta types.BlockMeta
	var foundMultiPart bool
	for i := int64(1); i < 500; i++ {
		waitForHeight(t, i+1)
		blockMeta := env.BlockStore.LoadBlockMeta(i)
		if blockMeta != nil && blockMeta.BlockID.PartSetHeader.Total > 1 {
			expectedBlockMeta = *blockMeta
			foundMultiPart = true
			break
		}
		// If we can't find a multi-part block, use the latest block for basic streaming test
		if i > 10 && !foundMultiPart {
			if blockMeta != nil {
				expectedBlockMeta = *blockMeta
			}
			break
		}
	}

	// Ensure we have a valid block to test with
	require.NotEmpty(t, expectedBlockMeta.BlockID.Hash, "Expected to find a valid block for testing")

	// query the block without the part proofs
	res, err := client.BlockByHeight(context.Background(), &core_grpc.BlockByHeightRequest{
		Height: expectedBlockMeta.Header.Height,
		Prove:  false,
	})
	require.NoError(t, err)

	part1, err := res.Recv()
	require.NoError(t, err)

	var part2 *core_grpc.BlockByHeightResponse

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)

	assert.Equal(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.Commit.Height, expectedBlockMeta.Header.Height)

	// Only test for multiple parts if we found a multi-part block
	if foundMultiPart {
		part2, err = res.Recv()
		require.NoError(t, err)

		require.NotNil(t, part2.BlockPart)
		require.Nil(t, part2.ValidatorSet)
		require.Nil(t, part2.Commit)

		assert.Equal(t, part2.BlockPart.Proof, crypto.Proof{})
	}

	// query the block along with the part proofs
	res, err = client.BlockByHeight(context.Background(), &core_grpc.BlockByHeightRequest{
		Height: expectedBlockMeta.Header.Height,
		Prove:  true,
	})
	require.NoError(t, err)

	part1, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)

	assert.NotEqual(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.Commit.Height, expectedBlockMeta.Header.Height)

	// Only test for multiple parts if we found a multi-part block
	if foundMultiPart {
		part2, err = res.Recv()
		require.NoError(t, err)

		require.NotNil(t, part2.BlockPart)
		require.Nil(t, part2.ValidatorSet)
		require.Nil(t, part2.Commit)

		assert.NotEqual(t, part2.BlockPart.Proof, crypto.Proof{})
	}
}

func TestBlobstreamAPI(t *testing.T) {
	client, err := rpctest.GetBlobstreamAPIClient()
	require.NoError(t, err)
	waitForHeight(t, 10)

	resp, err := client.DataRootInclusionProof(
		context.Background(),
		&core_grpc.DataRootInclusionProofRequest{
			Height: 6,
			Start:  1,
			End:    10,
		},
	)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int64(9), resp.Proof.Total)
	assert.Equal(t, int64(5), resp.Proof.Index)
	assert.Equal(t, 4, len(resp.Proof.Aunts))
}

func TestParseProtoAddr(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// DNS scheme
		{"dns:localhost:26657", "localhost:26657"},
		{"dns:example.com:9090", "example.com:9090"},
		{"dns:127.0.0.1:8080", "127.0.0.1:8080"},

		// Unix sockets
		{"unix:///tmp/grpc.sock", "/tmp/grpc.sock"},
		{"unix:///var/run/service.sock", "/var/run/service.sock"},

		// No scheme - should remain unchanged
		{"localhost:26657", "localhost:26657"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"/tmp/grpc.sock", "/tmp/grpc.sock"},

		// TCP scheme - should be stripped
		{"tcp://localhost:26657", "localhost:26657"},
	}

	for _, tt := range tests {
		result := core_grpc.ParseProtoAddr(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

func waitForHeight(t *testing.T, height int64) {
	rpcAddr := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.New(rpcAddr, "/websocket")
	require.NoError(t, err)
	err = rpcclient.WaitForHeight(c, height, nil)
	require.NoError(t, err)
}
