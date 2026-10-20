package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"github.com/stretchr/testify/require"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
)

func TestScanBlock(t *testing.T) {
	ns := share.MustNewV0Namespace(bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize))
	blob, err := share.NewV0Blob(ns, []byte("data"))
	require.NoError(t, err)
	valid, err := squaretx.MarshalBlobTx([]byte("inner"), blob)
	require.NoError(t, err)
	unknownField := append(append([]byte{}, valid...), 0x78, 0x01)

	block := &types.Block{
		Header: types.Header{Height: 7},
		Data:   types.Data{Txs: types.Txs{[]byte("plain-tx"), valid, unknownField}},
	}
	block.Version.App = 3
	s := &stats{ByAppVersion: map[uint64]int64{}, ByReason: map[string]int64{}}

	findings := scanBlock(block, 4096, s)
	require.Len(t, findings, 1)
	f := findings[0]
	require.Equal(t, int64(7), f.Height)
	require.Equal(t, 2, f.Index)
	require.Equal(t, uint64(3), f.AppVersion)
	require.True(t, f.LegacyOK)
	require.False(t, f.SquareOK)
	require.Equal(t, hash([]byte("inner")), f.LegacyHash)
	require.Equal(t, hash(unknownField), f.SquareHash)
	require.NotEmpty(t, f.RawTx)

	require.Equal(t, int64(3), s.Txs)
	require.Equal(t, int64(2), s.LegacyBlobs)
	require.Equal(t, int64(1), s.SquareBlobs)
	require.Equal(t, int64(1), s.ByReason[squaretx.ErrNonCanonicalBlobTx.Error()])
}

// fakeNode serves /status and /block for heights 1..latest. The block at
// badHeight holds a BlobTx that only the legacy decoder accepts.
func fakeNode(t *testing.T, latest, badHeight int64) *httptest.Server {
	t.Helper()
	ns := share.MustNewV0Namespace(bytes.Repeat([]byte{1}, share.NamespaceVersionZeroIDSize))
	blob, err := share.NewV0Blob(ns, []byte("data"))
	require.NoError(t, err)
	valid, err := squaretx.MarshalBlobTx([]byte("inner"), blob)
	require.NoError(t, err)
	unknownField := append(append([]byte{}, valid...), 0x78, 0x01)

	reply := func(w http.ResponseWriter, result any) {
		bz, err := json.Marshal(rpctypes.NewRPCSuccessResponse(rpctypes.JSONRPCIntID(-1), result))
		require.NoError(t, err)
		_, _ = w.Write(bz)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			status := &ctypes.ResultStatus{}
			status.NodeInfo.Network = "test-chain"
			status.SyncInfo.EarliestBlockHeight = 1
			status.SyncInfo.LatestBlockHeight = latest
			reply(w, status)
		case "/block":
			h, err := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
			require.NoError(t, err)
			txs := types.Txs{valid}
			if h == badHeight {
				txs = append(txs, unknownField)
			}
			block := &types.Block{Header: types.Header{Height: h}, Data: types.Data{Txs: txs}}
			reply(w, &ctypes.ResultBlock{Block: block})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRunResume(t *testing.T) {
	node := fakeNode(t, 20, 5)
	defer node.Close()
	dir := t.TempDir()
	c := config{
		remote: node.URL, to: 10, stride: 1, workers: 2, maxRawSize: 4096,
		outPath: filepath.Join(dir, "findings.jsonl"), cpPath: filepath.Join(dir, "checkpoint"),
	}

	cp, err := run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, int64(10), cp.Done)
	require.Equal(t, int64(1), cp.Stats.Findings)

	// Resuming to the latest height keeps the totals from the first run.
	c.to = 0
	cp, err = run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, int64(20), cp.Done)
	require.Equal(t, int64(20), cp.Stats.Blocks)
	require.Equal(t, int64(1), cp.Stats.Findings)

	bz, err := os.ReadFile(c.outPath)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(bz, []byte("\n")))
}

func TestRunRejectsMismatchedCheckpoint(t *testing.T) {
	node := fakeNode(t, 20, 5)
	defer node.Close()
	dir := t.TempDir()
	c := config{
		remote: node.URL, stride: 10, workers: 2, maxRawSize: 4096,
		outPath: filepath.Join(dir, "findings.jsonl"), cpPath: filepath.Join(dir, "checkpoint"),
	}
	cp, err := run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, int64(0), cp.Stats.Findings, "sampled heights 1 and 11 miss height 5")

	// A full scan must not reuse the sampled scan's checkpoint.
	c.stride = 1
	_, err = run(context.Background(), c)
	require.ErrorContains(t, err, "stride 10")

	// Nor report a shorter range than the checkpoint already covers.
	c.stride = 10
	c.to = 5
	_, err = run(context.Background(), c)
	require.ErrorContains(t, err, "already covers heights up to 20")

	// Nor append to its findings file without a checkpoint.
	c.stride, c.to = 1, 0
	require.NoError(t, os.Remove(c.cpPath))
	require.NoError(t, os.WriteFile(c.outPath, []byte("{}\n"), 0o644))
	_, err = run(context.Background(), c)
	require.ErrorContains(t, err, "already has entries")
}
