package core

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/mempool/cat"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
)

type fuzzWSConn struct{ ctx context.Context }

func (fuzzWSConn) GetRemoteAddr() string { return "fuzz-ws-client" }
func (c fuzzWSConn) WriteRPCResponse(ctx context.Context, _ rpctypes.RPCResponse) error {
	return context.Canceled
}
func (fuzzWSConn) TryWriteRPCResponse(rpctypes.RPCResponse) bool { return false }
func (c fuzzWSConn) Context() context.Context                    { return c.ctx }

// FuzzRPCCoreHandlers calls the rpc/core handlers directly with fuzzed
// parameters against an Environment holding EnvTestHeight committed blocks.
func FuzzRPCCoreHandlers(f *testing.F) {
	env := NewEnvForTest(f)
	mp := env.Mempool.(*cat.TxPool)
	h := int64(EnvTestHeight)
	committed := EnvTestTx(2, 1)
	for sel := uint8(0); sel < fuzzHandlerCount; sel++ {
		f.Add(sel, int64(2), uint64(1), uint64(h+1), 1, 30, "tx.height>=1", []byte(committed))
	}
	f.Add(uint8(4), int64(0), uint64(0), uint64(0), 0, 0, "tm.event='NewBlock'", []byte(nil))
	f.Add(uint8(5), int64(0), uint64(0), uint64(1), 1<<62, -1, "tx.height>1 AND tx.height<=3", []byte{})
	f.Add(uint8(13), int64(0), uint64(1), ^uint64(0), 0, 0, "", []byte{})
	f.Add(uint8(14), int64(-1), uint64(1), uint64(3), 0, 0, "", []byte{})
	f.Add(uint8(18), h+1, uint64(0), uint64(0), 2, 1000, "", []byte{})
	f.Add(uint8(9), int64(0), uint64(21), uint64(0), 0, 0, "", bytes.Repeat(committed.Hash(), 21))

	f.Fuzz(func(t *testing.T, sel uint8, height int64, a, b uint64, page, perPage int, s string, data []byte) {
		if len(data) > 2*EnvTestMaxTxBytes || len(s) > 2*maxQueryLength {
			return
		}
		ctx := &rpctypes.Context{}
		pagePtr, perPagePtr := &page, &perPage
		if a&2 != 0 {
			pagePtr, perPagePtr = nil, nil
		}
		switch sel % fuzzHandlerCount {
		case 0:
			res, err := env.BroadcastTxAsync(ctx, data)
			if len(data) > EnvTestMaxTxBytes {
				require.Error(t, err)
			}
			if err == nil {
				require.Equal(t, types.Tx(data).Hash(), []byte(res.Hash))
			}
		case 1:
			res, err := env.BroadcastTxSync(ctx, data)
			if len(data) > EnvTestMaxTxBytes {
				require.Error(t, err)
			}
			if err == nil {
				require.Equal(t, types.Tx(data).Hash(), []byte(res.Hash))
			}
		case 2:
			_, err := env.CheckTx(ctx, data)
			require.NoError(t, err)
		case 3:
			var pb cmtproto.Evidence
			if pb.Unmarshal(data) != nil {
				return
			}
			ev, err := types.EvidenceFromProto(&pb)
			if err != nil {
				return
			}
			// Nobody can sign for the test validator, so evidence is never accepted.
			_, err = env.BroadcastEvidence(ctx, ev)
			require.Error(t, err)
			require.Empty(t, env.EvidencePool.(interface{ Size() uint32 }).Size())
		case 4:
			wsCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wsc := &rpctypes.Context{JSONReq: &rpctypes.RPCRequest{ID: rpctypes.JSONRPCIntID(1)}, WSConn: fuzzWSConn{wsCtx}}
			_, subErr := env.Subscribe(wsc, s)
			_, unsubErr := env.Unsubscribe(wsc, s)
			if subErr == nil {
				require.NoError(t, unsubErr)
			}
			_, _ = env.UnsubscribeAll(wsc)
		case 5:
			res, err := env.TxSearch(ctx, s, a&1 == 1, pagePtr, perPagePtr, fuzzOrderBy(b))
			if err == nil {
				require.LessOrEqual(t, len(res.Txs), maxPerPage)
				require.LessOrEqual(t, len(res.Txs), res.TotalCount)
			}
		case 6:
			res, err := env.BlockSearch(ctx, s, pagePtr, perPagePtr, fuzzOrderBy(b))
			if err == nil {
				require.LessOrEqual(t, len(res.Blocks), maxPerPage)
				require.LessOrEqual(t, res.TotalCount, int(h))
			}
		case 7:
			_, _ = env.Tx(ctx, data, a&1 == 1)
		case 8:
			res, err := env.TxStatus(ctx, data)
			if err == nil && bytes.Equal(data, committed.Hash()) {
				require.Equal(t, TxStatusCommitted, res.Status)
			}
		case 9:
			n := int(a % (MaxTxStatusBatchSize + 5))
			hashes := make([][]byte, n)
			for i := range hashes {
				if len(data) > 0 {
					hashes[i] = data[(i*7)%len(data):]
				}
			}
			res, err := env.TxStatusBatch(ctx, hashes)
			if n > MaxTxStatusBatchSize {
				require.Error(t, err)
			} else if err == nil {
				require.Len(t, res.Statuses, n)
			}
		case 10:
			_, _ = env.ProveShares(ctx, height, a, b)
		case 11:
			_, _ = env.ProveSharesV2(ctx, height, a, b)
		case 12:
			res, err := env.SignedBlock(ctx, &height)
			if height >= 1 && height <= h {
				require.NoError(t, err)
				require.Equal(t, height, res.Header.Height)
			}
		case 13:
			_, err := env.DataCommitment(ctx, a, b)
			if err == nil {
				require.True(t, a >= 1 && a < b && b <= uint64(h)+1 && b-a <= dataCommitmentBlocksLimit)
			}
		case 14:
			res, err := env.DataRootInclusionProof(ctx, height, a, b)
			if err == nil {
				require.True(t, a >= 1 && a < b && b <= uint64(h)+1)
				require.True(t, height >= 0 && uint64(height) >= a && uint64(height) < b)
				require.EqualValues(t, b-a, res.Proof.Total)
			}
		case 15:
			res, err := env.Block(ctx, &height)
			if height >= 1 && height <= h {
				require.NoError(t, err)
				require.Equal(t, res.Block.Hash(), res.BlockID.Hash)
			}
		case 16:
			_, err := env.BlockResults(ctx, &height)
			if height >= 1 && height <= h {
				require.NoError(t, err)
			}
		case 17:
			_, _ = env.Commit(ctx, &height)
		case 18:
			res, err := env.Validators(ctx, &height, pagePtr, perPagePtr)
			if err == nil {
				require.LessOrEqual(t, res.Count, maxPerPage)
				require.Equal(t, 1, res.Total)
			}
		case 19:
			_, _ = env.ABCIQuery(ctx, s, data, height, a&1 == 1)
		case 20:
			res, err := env.BlockchainInfo(ctx, height, int64(a)) //nolint:gosec
			if err == nil {
				require.LessOrEqual(t, len(res.BlockMetas), 20)
			}
		case 21:
			_, _ = env.HeaderByHash(ctx, cmtbytes.HexBytes(data))
			_, _ = env.BlockByHash(ctx, data)
			_, _ = env.Header(ctx, &height)
		case 22:
			res, err := env.UnconfirmedTxs(ctx, &perPage)
			require.NoError(t, err)
			require.LessOrEqual(t, res.Count, maxPerPage)
		case 23:
			_, err := env.ConsensusParams(ctx, &height)
			if height >= 1 && height <= h {
				require.NoError(t, err)
			}
		case 24:
			_, err := env.GenesisChunked(ctx, uint(a))
			if a != 0 {
				require.Error(t, err)
			}
		}
		// Reset shared state so every input starts from the same environment.
		mp.Flush()
		require.Zero(t, env.EventBus.NumClients())
	})
}

const fuzzHandlerCount = 25

func fuzzOrderBy(b uint64) string {
	return [...]string{"", "asc", "desc", "ASC"}[b%4]
}
