package rpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/celestiaorg/go-square/v4/share"
	"github.com/celestiaorg/nmt"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/consts"
	"github.com/cometbft/cometbft/light/rpc/mocks"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cometbft/cometbft/types"
)

// testSquareWidth is the width of the extended test square; the original data
// square is testSquareWidth/2 shares wide.
const testSquareWidth = 4

var testNamespace = append([]byte{0}, bytes.Repeat([]byte{1}, share.NamespaceIDSize)...)

// testSquare is a 2x2 data square extended to 4x4 with stand-in parity shares.
type testSquare struct {
	shares   [][][]byte
	rowTrees []*nmt.NamespacedMerkleTree
	roots    [][]byte // row roots, then column roots
	root     []byte
	proofs   []*merkle.Proof
}

func newTestSquare(t *testing.T) testSquare {
	t.Helper()
	var sq testSquare
	for r := range testSquareWidth {
		row := make([][]byte, 0, testSquareWidth)
		for c := range testSquareWidth {
			data := bytes.Repeat([]byte{byte(r*testSquareWidth + c)}, share.ShareSize-share.NamespaceSize)
			row = append(row, append(append([]byte{}, testNamespace...), data...))
		}
		sq.shares = append(sq.shares, row)
	}
	for r := range testSquareWidth {
		tree := sq.tree(t, func(i int) (int, int) { return r, i })
		root, err := tree.Root()
		require.NoError(t, err)
		sq.rowTrees = append(sq.rowTrees, tree)
		sq.roots = append(sq.roots, root)
	}
	for c := range testSquareWidth {
		root, err := sq.tree(t, func(i int) (int, int) { return i, c }).Root()
		require.NoError(t, err)
		sq.roots = append(sq.roots, root)
	}
	sq.root, sq.proofs = merkle.ProofsFromByteSlices(sq.roots)
	return sq
}

// tree builds the NMT of one row or column, hashing parity shares under the
// parity namespace like celestia-app does.
func (sq testSquare) tree(t *testing.T, cell func(int) (int, int)) *nmt.NamespacedMerkleTree {
	t.Helper()
	tree := nmt.New(consts.NewBaseHashFunc(), nmt.NamespaceIDSize(share.NamespaceSize), nmt.IgnoreMaxNamespace(true))
	for i := range testSquareWidth {
		r, c := cell(i)
		ns := testNamespace
		if r >= testSquareWidth/2 || c >= testSquareWidth/2 {
			ns = share.ParitySharesNamespace.Bytes()
		}
		require.NoError(t, tree.Push(append(append([]byte{}, ns...), sq.shares[r][c]...)))
	}
	return tree
}

type rowRange struct{ row, start, end int }

// shareProof proves the given ranges, one per consecutive row.
func (sq testSquare) shareProof(t *testing.T, ranges ...rowRange) types.ShareProof {
	t.Helper()
	sp := types.ShareProof{NamespaceID: testNamespace[1:], NamespaceVersion: uint32(testNamespace[0])}
	sp.RowProof.StartRow = uint32(ranges[0].row)
	sp.RowProof.EndRow = uint32(ranges[len(ranges)-1].row)
	for _, r := range ranges {
		p, err := sq.rowTrees[r.row].ProveRange(r.start, r.end)
		require.NoError(t, err)
		sp.Data = append(sp.Data, sq.shares[r.row][r.start:r.end]...)
		sp.ShareProofs = append(sp.ShareProofs, &cmtproto.NMTProof{Start: int32(r.start), End: int32(r.end), Nodes: p.Nodes()})
		mp := *sq.proofs[r.row]
		sp.RowProof.RowRoots = append(sp.RowProof.RowRoots, cmtbytes.HexBytes(sq.roots[r.row]))
		sp.RowProof.Proofs = append(sp.RowProof.Proofs, &mp)
	}
	return sp
}

// nextShareProofClient stubs the underlying RPC client's share proof methods.
type nextShareProofClient struct {
	rpcclient.Client
	proof types.ShareProof
}

func (n nextShareProofClient) ProveShares(_ context.Context, _, _, _ uint64) (types.ShareProof, error) {
	return n.proof, nil
}

func (n nextShareProofClient) ProveSharesV2(_ context.Context, _, _, _ uint64) (*ctypes.ResultShareProof, error) {
	return &ctypes.ResultShareProof{ShareProof: n.proof}, nil
}

// lightClientWithDataHash returns a light client mock whose verified header at
// height has the given data hash.
func lightClientWithDataHash(height int64, dataHash []byte) *mocks.LightClient {
	lc := &mocks.LightClient{}
	lc.On("VerifyLightBlockAtHeight", mock.Anything, height, mock.Anything).Return(&types.LightBlock{
		SignedHeader: &types.SignedHeader{Header: &types.Header{Height: height, DataHash: dataHash}},
	}, nil)
	return lc
}

// proveSharesFuncs runs each test against both share proof methods.
var proveSharesFuncs = map[string]func(c *Client, height, start, end uint64) (types.ShareProof, error){
	"ProveShares": func(c *Client, height, start, end uint64) (types.ShareProof, error) {
		return c.ProveShares(context.Background(), height, start, end)
	},
	"ProveSharesV2": func(c *Client, height, start, end uint64) (types.ShareProof, error) {
		res, err := c.ProveSharesV2(context.Background(), height, start, end)
		if err != nil {
			return types.ShareProof{}, err
		}
		return res.ShareProof, nil
	},
}

func TestProveSharesAcceptsValidProof(t *testing.T) {
	sq := newTestSquare(t)
	for name, prove := range proveSharesFuncs {
		t.Run(name, func(t *testing.T) {
			// Shares [1, 3) of the 2-wide square: share 1 of row 0 and share 0 of row 1.
			proof := sq.shareProof(t, rowRange{0, 1, 2}, rowRange{1, 0, 1})
			c := NewClient(nextShareProofClient{proof: proof}, lightClientWithDataHash(5, sq.root))

			got, err := prove(c, 5, 1, 3)
			require.NoError(t, err)
			require.Equal(t, proof.Data, got.Data)
		})
	}
}

// A malicious upstream can answer with a genuine proof for other shares in the
// same block. It must be rejected before any light-client verification runs.
func TestProveSharesRejectsOtherRange(t *testing.T) {
	sq := newTestSquare(t)
	for name, prove := range proveSharesFuncs {
		t.Run(name, func(t *testing.T) {
			proof := sq.shareProof(t, rowRange{0, 0, 2}) // shares [0, 2)
			lc := &mocks.LightClient{}
			c := NewClient(nextShareProofClient{proof: proof}, lc)

			_, err := prove(c, 5, 1, 3)
			require.Error(t, err)
			require.Contains(t, err.Error(), "does not match requested range")
			lc.AssertNotCalled(t, "VerifyLightBlockAtHeight")
		})
	}
}

// A proof that does not verify against the light-verified data root must be
// rejected.
func TestProveSharesRejectsWrongDataRoot(t *testing.T) {
	sq := newTestSquare(t)
	for name, prove := range proveSharesFuncs {
		t.Run(name, func(t *testing.T) {
			proof := sq.shareProof(t, rowRange{0, 0, 2})
			c := NewClient(nextShareProofClient{proof: proof}, lightClientWithDataHash(5, bytes.Repeat([]byte{7}, 32)))

			_, err := prove(c, 5, 0, 2)
			require.Error(t, err)
		})
	}
}

func TestProveSharesRejectsBadHeight(t *testing.T) {
	sq := newTestSquare(t)
	for name, prove := range proveSharesFuncs {
		t.Run(name, func(t *testing.T) {
			lc := &mocks.LightClient{}
			c := NewClient(nextShareProofClient{proof: sq.shareProof(t, rowRange{0, 0, 2})}, lc)

			for _, height := range []uint64{0, 1 << 63} {
				_, err := prove(c, height, 0, 2)
				require.Error(t, err, "height %d", height)
			}
			lc.AssertNotCalled(t, "VerifyLightBlockAtHeight")
		})
	}
}
