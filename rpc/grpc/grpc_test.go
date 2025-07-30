//nolint:dupl
package coregrpc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/proto/tendermint/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/rpc/core"
	coregrpc "github.com/cometbft/cometbft/rpc/grpc"
	rpctest "github.com/cometbft/cometbft/rpc/test"
	"github.com/cometbft/cometbft/types"
)

func TestGRPC(t *testing.T) {
	s := new(GRPCSuite)
	defer s.TearDown()
	suite.Run(t, s)
}

type GRPCSuite struct {
	suite.Suite
	testNode *node.Node
	env      *core.Environment
}

func (s *GRPCSuite) SetupSuite() {
	app := kvstore.NewInMemoryApplication()
	testNode := rpctest.StartTendermint(app)
	env, err := testNode.ConfigureRPC()
	require.NoError(s.T(), err)
	s.testNode = testNode
	s.env = env
}

func (s *GRPCSuite) TearDown() {
	assert.NoError(s.T(), s.testNode.Stop())
}

func (s *GRPCSuite) TestBroadcastTx() {
	t := s.T()
	res, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&coregrpc.RequestBroadcastTx{Tx: kvstore.NewTx("hello", "world")},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, res.CheckTx.Code)
	require.EqualValues(t, 0, res.TxResult.Code)
}

func setupClient(t *testing.T) coregrpc.BlockAPIClient {
	client, err := rpctest.GetBlockAPIClient()
	require.NoError(t, err)
	return client
}

func (s *GRPCSuite) TestBlockByHash() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockMeta := s.env.BlockStore.LoadBlockMeta(1)
	require.NotNil(t, expectedBlockMeta)

	// query the block along with the part proofs
	res, err := client.BlockByHash(context.Background(), &coregrpc.BlockByHashRequest{
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
	res, err = client.BlockByHash(context.Background(), &coregrpc.BlockByHashRequest{
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

func (s *GRPCSuite) TestCommit() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockCommit := s.env.BlockStore.LoadSeenCommit(1)

	res, err := client.Commit(context.Background(), &coregrpc.CommitRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedBlockCommit.BlockID.Hash.Bytes(), res.Commit.BlockID.Hash)
}

func (s *GRPCSuite) TestLatestCommit() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 3)

	res, err := client.Commit(context.Background(), &coregrpc.CommitRequest{
		Height: 0,
	})
	require.NoError(t, err)

	assert.Greater(t, res.Commit.Height, int64(2))
}

func (s *GRPCSuite) TestValidatorSet() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedValidatorSet, err := s.env.StateStore.LoadValidators(1)
	require.NoError(t, err)

	res, err := client.ValidatorSet(context.Background(), &coregrpc.ValidatorSetRequest{
		Height: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, len(expectedValidatorSet.Validators), len(res.ValidatorSet.Validators))
}

func (s *GRPCSuite) TestLatestValidatorSet() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 3)

	res, err := client.ValidatorSet(context.Background(), &coregrpc.ValidatorSetRequest{
		Height: 0,
	})
	require.NoError(t, err)

	assert.Greater(t, res.Height, int64(2))
}

func (s *GRPCSuite) TestStatus() {
	t := s.T()
	client := setupClient(t)
	expectedStatus, err := s.env.Status(nil)
	require.NoError(t, err)

	res, err := client.Status(context.Background(), &coregrpc.StatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, string(expectedStatus.NodeInfo.DefaultNodeID), res.NodeInfo.DefaultNodeID)
}

func (s *GRPCSuite) TestSubscribeNewHeights() {
	t := s.T()
	client := setupClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.SubscribeNewHeights(ctx, &coregrpc.SubscribeNewHeightsRequest{})
	require.NoError(t, err)

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
			assert.Equal(t, s.env.BlockStore.LoadBlockMeta(res.Height).BlockID.Hash.Bytes(), res.Hash)
			listenedHeightsCount++
		}
	}()

	time.Sleep(5 * time.Second)
}

func (s *GRPCSuite) TestBlockByHash_Streaming() {
	t := s.T()
	client := setupClient(t)

	status, err := s.env.Status(nil)
	require.NoError(t, err)
	latestBlockHeight := status.SyncInfo.LatestBlockHeight

	// send a big transaction that would result in a block
	// containing multiple block parts
	txRes, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&coregrpc.RequestBroadcastTx{Tx: kvstore.NewRandomTx(100000)},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, txRes.CheckTx.Code)
	require.EqualValues(t, 0, txRes.TxResult.Code)

	var expectedBlockMeta types.BlockMeta
	for i := latestBlockHeight; i < latestBlockHeight+500; i++ {
		waitForHeight(t, i+1)
		blockMeta := s.env.BlockStore.LoadBlockMeta(i)
		if blockMeta.BlockID.PartSetHeader.Total > 1 {
			expectedBlockMeta = *blockMeta
			break
		}
	}

	// Ensure we have a valid block to test with
	require.NotEmpty(t, expectedBlockMeta.BlockID.Hash, "Expected to find a valid block for testing")

	// query the block without the part proofs
	res, err := client.BlockByHash(context.Background(), &coregrpc.BlockByHashRequest{
		Hash:  expectedBlockMeta.BlockID.Hash,
		Prove: false,
	})
	require.NoError(t, err)

	part1, err := res.Recv()
	require.NoError(t, err)

	var part2 *coregrpc.BlockByHashResponse

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)

	assert.Equal(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.Commit.Height, expectedBlockMeta.Header.Height)

	part2, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)

	assert.Equal(t, part2.BlockPart.Proof, crypto.Proof{})

	// query the block along with the part proofs
	res, err = client.BlockByHash(context.Background(), &coregrpc.BlockByHashRequest{
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

	part2, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)

	assert.NotEqual(t, part2.BlockPart.Proof, crypto.Proof{})
}

func (s *GRPCSuite) TestBlockByHeight() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 2)
	expectedBlockMeta := s.env.BlockStore.LoadBlockMeta(1)

	// query the block along with the part proofs
	res, err := client.BlockByHeight(context.Background(), &coregrpc.BlockByHeightRequest{
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
	res, err = client.BlockByHeight(context.Background(), &coregrpc.BlockByHeightRequest{
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

func (s *GRPCSuite) TestLatestBlockByHeight() {
	t := s.T()
	client := setupClient(t)
	waitForHeight(t, 2)

	// query the block along with the part proofs
	res, err := client.BlockByHeight(context.Background(), &coregrpc.BlockByHeightRequest{
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

func (s *GRPCSuite) TestBlockQuery_Streaming() {
	t := s.T()
	client := setupClient(t)

	status, err := s.env.Status(nil)
	require.NoError(t, err)
	latestBlockHeight := status.SyncInfo.LatestBlockHeight

	// send a big transaction that would result in a block
	// containing multiple block parts
	txRes, err := rpctest.GetGRPCClient().BroadcastTx(
		context.Background(),
		&coregrpc.RequestBroadcastTx{Tx: kvstore.NewRandomTx(100000)},
	)
	require.NoError(t, err)
	require.EqualValues(t, 0, txRes.CheckTx.Code)
	require.EqualValues(t, 0, txRes.TxResult.Code)

	var expectedBlockMeta types.BlockMeta
	for i := latestBlockHeight; i < latestBlockHeight+500; i++ {
		waitForHeight(t, i+1)
		blockMeta := s.env.BlockStore.LoadBlockMeta(i)
		if blockMeta.BlockID.PartSetHeader.Total > 1 {
			expectedBlockMeta = *blockMeta
			break
		}
	}

	// Ensure we have a valid block to test with
	require.NotEmpty(t, expectedBlockMeta.BlockID.Hash, "Expected to find a valid block for testing")

	// query the block without the part proofs
	res, err := client.BlockByHeight(context.Background(), &coregrpc.BlockByHeightRequest{
		Height: expectedBlockMeta.Header.Height,
		Prove:  false,
	})
	require.NoError(t, err)

	part1, err := res.Recv()
	require.NoError(t, err)

	var part2 *coregrpc.BlockByHeightResponse

	require.NotNil(t, part1.BlockPart)
	require.NotNil(t, part1.ValidatorSet)
	require.NotNil(t, part1.Commit)

	assert.Equal(t, part1.BlockPart.Proof, crypto.Proof{})
	assert.Equal(t, part1.Commit.Height, expectedBlockMeta.Header.Height)

	part2, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)

	assert.Equal(t, part2.BlockPart.Proof, crypto.Proof{})

	// query the block along with the part proofs
	res, err = client.BlockByHeight(context.Background(), &coregrpc.BlockByHeightRequest{
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

	part2, err = res.Recv()
	require.NoError(t, err)

	require.NotNil(t, part2.BlockPart)
	require.Nil(t, part2.ValidatorSet)
	require.Nil(t, part2.Commit)

	assert.NotEqual(t, part2.BlockPart.Proof, crypto.Proof{})
}

func (s *GRPCSuite) TestBlobstreamAPI() {
	t := s.T()
	client, err := rpctest.GetBlobstreamAPIClient()
	require.NoError(t, err)
	waitForHeight(t, 10)

	resp, err := client.DataRootInclusionProof(
		context.Background(),
		&coregrpc.DataRootInclusionProofRequest{
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

func TestCanonicalGRPCAddress(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// DNS scheme should remain unchanged
		{"dns:localhost:26657", "dns:localhost:26657"},
		// Unix sockets should remain unchanged
		{"unix:///tmp/grpc.sock", "unix:///tmp/grpc.sock"},
		// No scheme should remain unchanged
		{"localhost:26657", "localhost:26657"},
		// TCP scheme should be stripped
		{"tcp://localhost:26657", "localhost:26657"},
		// Other schemes should be stripped
		{"http://localhost:26657", "localhost:26657"},
		{"grpc://127.0.0.1:8080", "127.0.0.1:8080"},
	}

	for _, tt := range tests {
		result := coregrpc.CanonicalGRPCAddress(tt.input)
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
