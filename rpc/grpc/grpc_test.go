//nolint:dupl
package coregrpc_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/proto/tendermint/crypto"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	"github.com/tendermint/tendermint/rpc/core"
	"github.com/tendermint/tendermint/types"

	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/abci/example/kvstore"
	core_grpc "github.com/tendermint/tendermint/rpc/grpc"
	rpctest "github.com/tendermint/tendermint/rpc/test"
)

func TestMain(m *testing.M) {
	// start a CometBFT node in the background to test against
	app := kvstore.NewApplication()
	node := rpctest.StartTendermint(app)

	code := m.Run()

	// and shut down proper at the end
	rpctest.StopTendermint(node)
	os.Exit(code)
}

func TestBroadcastTx(t *testing.T) {
	res, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&core_grpc.RequestBroadcastTx{Tx: []byte("this is a tx")},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, res.CheckTx.Code)
	require.EqualValues(t, 0, res.DeliverTx.Code)
}

func setupClient(t *testing.T) core_grpc.BlockAPIClient {
	client, err := rpctest.GetBlockAPIClient()
	require.NoError(t, err)
	return client
}

func TestBlockByHash(t *testing.T) {
	client := setupClient(t)
	expectedBlockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(1)

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
	require.NotNil(t, part.BlockMeta)

	assert.NotEqual(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())

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
	require.NotNil(t, part.BlockMeta)

	assert.Equal(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())
}

func TestBlockMetaByHash(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(1)

	res, err := client.BlockMetaByHash(context.Background(), &core_grpc.BlockMetaByHashRequest{
		Hash: expectedBlockMeta.BlockID.Hash,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedBlockMeta.BlockID.Hash.Bytes(), res.BlockMeta.BlockID.Hash)
}

func TestBlockMetaByHeight(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(1)

	res, err := client.BlockMetaByHeight(context.Background(), &core_grpc.BlockMetaByHeightRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedBlockMeta.BlockID.Hash.Bytes(), res.BlockMeta.BlockID.Hash)
}

func TestCommit(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockCommit := core.GetEnvironment().BlockStore.LoadSeenCommit(1)

	res, err := client.Commit(context.Background(), &core_grpc.CommitRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedBlockCommit.BlockID.Hash.Bytes(), res.Commit.BlockID.Hash)
}

func TestValidatorSet(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedValidatorSet, err := core.GetEnvironment().StateStore.LoadValidators(1)
	require.NoError(t, err)

	res, err := client.ValidatorSet(context.Background(), &core_grpc.ValidatorSetRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, len(expectedValidatorSet.Validators), len(res.ValidatorSet.Validators))
}

func TestStatus(t *testing.T) {
	client := setupClient(t)
	expectedStatus, err := core.Status(nil)
	require.NoError(t, err)

	res, err := client.Status(context.Background(), &core_grpc.StatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, string(expectedStatus.NodeInfo.DefaultNodeID), res.NodeInfo.DefaultNodeID)
}

func TestSubscribeNewHeights(t *testing.T) {
	client := setupClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.SubscribeNewHeights(ctx, &core_grpc.SubscribeNewHeightsRequest{})
	require.NoError(t, err)
	store := core.GetEnvironment().BlockStore

	listenedHeightsCount := 0
	go func() {
		for {
			res, err := stream.Recv()
			if ctx.Err() != nil {
				return
			}
			require.NoError(t, err)
			require.Greater(t, res.Height, int64(0))
			assert.Equal(t, store.LoadBlockMeta(res.Height).BlockID.Hash.Bytes(), res.Hash)
			listenedHeightsCount++
		}
	}()

	time.Sleep(5 * time.Second)
	cancel()
	assert.Greater(t, listenedHeightsCount, 0)
}

func TestBlockByHash_Streaming(t *testing.T) {
	client := setupClient(t)

	// send a big transaction that would result in a block
	// containing multiple block parts
	txRes, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&core_grpc.RequestBroadcastTx{Tx: rand.NewRand().Bytes(1000000)},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, txRes.CheckTx.Code)
	require.EqualValues(t, 0, txRes.DeliverTx.Code)

	var expectedBlockMeta types.BlockMeta
	for i := int64(1); i < 500; i++ {
		waitForHeight(t, i+1)
		blockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(i)
		if blockMeta.BlockID.PartSetHeader.Total > 1 {
			expectedBlockMeta = *blockMeta
			break
		}
	}

	// query the block without the part proofs
	res, err := client.BlockByHash(context.Background(), &core_grpc.BlockByHashRequest{
		Hash:  expectedBlockMeta.BlockID.Hash,
		Prove: false,
	})
	require.NoError(t, err)

	part1, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)
	require.NotNil(t, part1.BlockMeta)

	assert.Equal(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())

	part2, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)
	require.Nil(t, part2.BlockMeta)

	assert.Equal(t, part2.BlockPart.Proof, crypto.Proof{})

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
	require.NotNil(t, part1.BlockMeta)

	assert.NotEqual(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())

	part2, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)
	require.Nil(t, part2.BlockMeta)

	assert.NotEqual(t, part2.BlockPart.Proof, crypto.Proof{})
}

func TestBlockByHeight(t *testing.T) {
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(1)

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
	require.NotNil(t, part.BlockMeta)

	assert.NotEqual(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())

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
	require.NotNil(t, part.BlockMeta)

	assert.Equal(t, part.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())
}

func TestBlockQuery_Streaming(t *testing.T) {
	client := setupClient(t)

	// send a big transaction that would result in a block
	// containing multiple block parts
	txRes, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&core_grpc.RequestBroadcastTx{Tx: rand.NewRand().Bytes(1000000)},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, txRes.CheckTx.Code)
	require.EqualValues(t, 0, txRes.DeliverTx.Code)

	var expectedBlockMeta types.BlockMeta
	for i := int64(1); i < 500; i++ {
		waitForHeight(t, i+1)
		blockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(i)
		if blockMeta.BlockID.PartSetHeader.Total > 1 {
			expectedBlockMeta = *blockMeta
			break
		}
	}

	// query the block without the part proofs
	res, err := client.BlockByHeight(context.Background(), &core_grpc.BlockByHeightRequest{
		Height: expectedBlockMeta.Header.Height,
		Prove:  false,
	})
	require.NoError(t, err)

	part1, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)
	require.NotNil(t, part1.BlockMeta)

	assert.Equal(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())

	part2, err := res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)
	require.Nil(t, part2.BlockMeta)

	assert.Equal(t, part2.BlockPart.Proof, crypto.Proof{})

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
	require.NotNil(t, part1.BlockMeta)

	assert.NotEqual(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.BlockMeta.BlockID.Hash, expectedBlockMeta.BlockID.Hash.Bytes())

	part2, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)
	require.Nil(t, part2.BlockMeta)

	assert.NotEqual(t, part2.BlockPart.Proof, crypto.Proof{})
}

func waitForHeight(t *testing.T, height int64) {
	rpcAddr := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.New(rpcAddr, "/websocket")
	require.NoError(t, err)
	err = rpcclient.WaitForHeight(c, height, nil)
	require.NoError(t, err)
}
