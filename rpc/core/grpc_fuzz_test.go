package core_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/cometbft/cometbft/rpc/core"
	coregrpc "github.com/cometbft/cometbft/rpc/grpc"
	cmttypes "github.com/cometbft/cometbft/types"
)

// FuzzRPCGRPCAPI calls the legacy gRPC BroadcastAPI, BlockAPI and
// BlobstreamAPI servers over an in-memory bufconn listener with fuzzed
// request fields, against an Environment with committed blocks.
func FuzzRPCGRPCAPI(f *testing.F) {
	env := core.NewEnvForTest(f)
	// BroadcastTx waits for the tx to be committed; nothing commits blocks
	// here, so let it time out right away.
	env.Config.TimeoutBroadcastTxCommit = time.Millisecond

	lis := bufconn.Listen(1 << 20)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = coregrpc.StartGRPCServer(env, lis) //nolint:staticcheck // legacy gRPC API is still served and is the attack surface under test
	}()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(f, err)
	f.Cleanup(func() {
		_ = conn.Close()
		_ = lis.Close()
		<-served
	})
	broadcast := coregrpc.NewBroadcastAPIClient(conn)
	blocks := coregrpc.NewBlockAPIClient(conn)
	blobstream := coregrpc.NewBlobstreamAPIClient(conn)
	h := int64(core.EnvTestHeight)
	// BlockByHash serves a block only once its commit is stored, which
	// happens when the next block is saved, so every block but the tip.
	servedHashes := make(map[string]bool, h-1)
	for height := int64(1); height <= h; height++ {
		hash := env.BlockStore.LoadBlockMeta(height).BlockID.Hash
		servedHashes[string(hash)] = height < h
		f.Add(uint8(1), int64(0), uint64(height), uint64(0), []byte(hash))
	}

	for sel := uint8(0); sel < 6; sel++ {
		f.Add(sel, int64(2), uint64(1), uint64(h+1), []byte("k=v"))
	}
	f.Add(uint8(2), int64(-1), uint64(0), uint64(0), []byte{})
	f.Add(uint8(5), int64(-1), uint64(1), ^uint64(0), []byte{})

	f.Fuzz(func(t *testing.T, sel uint8, height int64, a, b uint64, data []byte) {
		if len(data) > 2*core.EnvTestMaxTxBytes {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		prove := a&1 == 1
		switch sel % 6 {
		case 0:
			res, err := broadcast.BroadcastTx(ctx, &coregrpc.RequestBroadcastTx{Tx: data})
			if err == nil {
				require.NotNil(t, res.CheckTx)
			}
		case 1:
			stream, err := blocks.BlockByHash(ctx, &coregrpc.BlockByHashRequest{Hash: data, Prove: prove})
			require.NoError(t, err)
			n, err := drainParts(t, stream.Recv)
			if servedHashes[string(data)] {
				require.NoError(t, err)
				require.Positive(t, n)
			} else {
				require.Error(t, err)
			}
		case 2:
			stream, err := blocks.BlockByHeight(ctx, &coregrpc.BlockByHeightRequest{Height: height, Prove: prove})
			require.NoError(t, err)
			n, err := drainParts(t, stream.Recv)
			if height >= 0 && height <= h {
				require.NoError(t, err)
				require.Positive(t, n)
			}
		case 3:
			res, err := blocks.Commit(ctx, &coregrpc.CommitRequest{Height: height})
			if height >= 0 && height <= h {
				require.NoError(t, err)
				want := height
				if want == 0 {
					want = h
				}
				require.Equal(t, want, res.Commit.Height)
			}
		case 4:
			res, err := blocks.ValidatorSet(ctx, &coregrpc.ValidatorSetRequest{Height: height})
			switch {
			case height < 0:
				require.Error(t, err)
			case height <= h:
				require.NoError(t, err)
				fallthrough
			case err == nil:
				vs, err := cmttypes.ValidatorSetFromProto(res.ValidatorSet)
				require.NoError(t, err)
				require.Equal(t, 1, vs.Size())
			}
		case 5:
			res, err := blobstream.DataRootInclusionProof(ctx, &coregrpc.DataRootInclusionProofRequest{Height: height, Start: a, End: b})
			// Same validity oracle as the DataRootInclusionProof case of
			// FuzzRPCCoreHandlers, as an equivalence: b-a never exceeds
			// dataCommitmentBlocksLimit here since b <= h+1.
			valid := a >= 1 && a < b && b <= uint64(h)+1 &&
				height >= 0 && uint64(height) >= a && uint64(height) < b
			if !valid {
				require.Error(t, err)
				break
			}
			require.NoError(t, err)
			require.EqualValues(t, b-a, res.Proof.Total)
		}
		// BroadcastTx subscribes as "" and must always unsubscribe.
		require.Zero(t, env.EventBus.NumClientSubscriptions(""))
		env.Mempool.Flush()
	})
}

// partResponse is the common shape of the BlockByHash/BlockByHeight responses.
type partResponse interface{ GetIsLast() bool }

// drainParts reads a block stream to the end and returns the number of parts
// and the stream's error. A stream that ends without error must have sent a
// final part marked IsLast; the call must not have hit the test deadline.
func drainParts[R partResponse](t *testing.T, recv func() (R, error)) (int, error) {
	n := 0
	for {
		res, err := recv()
		require.NotErrorIs(t, err, io.EOF, "stream ended after %d parts without IsLast", n)
		if err != nil {
			require.NotEqual(t, codes.DeadlineExceeded, status.Code(err), "%v", err)
			return n, err
		}
		n++
		require.LessOrEqual(t, n, int(cmttypes.MaxBlockPartsCount))
		if res.GetIsLast() {
			_, err = recv()
			require.ErrorIs(t, err, io.EOF)
			return n, nil
		}
	}
}
