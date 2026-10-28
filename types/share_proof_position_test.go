package types

import (
	"bytes"
	"testing"

	"github.com/celestiaorg/go-square/v4/share"
	"github.com/celestiaorg/nmt"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	tmbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/consts"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// testSquare is a 2x2 data square extended to 4x4 with stand-in parity shares.
type testSquare struct {
	shares   [][][]byte // shares[row][col] of the extended square
	rowTrees []*nmt.NamespacedMerkleTree
	roots    [][]byte // row roots, then column roots
	root     []byte
	proofs   []*merkle.Proof
}

const testSquareWidth = 4 // extended square width

var testNamespace = append([]byte{0}, bytes.Repeat([]byte{1}, share.NamespaceIDSize)...)

func newTestSquare() testSquare {
	var sq testSquare
	for r := range testSquareWidth {
		row := make([][]byte, 0, testSquareWidth)
		for c := range testSquareWidth {
			row = append(row, append(append([]byte{}, testNamespace...), bytes.Repeat([]byte{byte(r*testSquareWidth + c)}, share.ShareSize-share.NamespaceSize)...))
		}
		sq.shares = append(sq.shares, row)
	}
	for r := range testSquareWidth {
		tree := sq.tree(func(i int) (int, int) { return r, i })
		sq.rowTrees = append(sq.rowTrees, tree)
		sq.roots = append(sq.roots, must(tree.Root()))
	}
	for c := range testSquareWidth {
		sq.roots = append(sq.roots, must(sq.tree(func(i int) (int, int) { return i, c }).Root()))
	}
	sq.root, sq.proofs = merkle.ProofsFromByteSlices(sq.roots)
	return sq
}

// tree builds the NMT of one row or column, hashing parity shares under the
// parity namespace like celestia-app does.
func (sq testSquare) tree(cell func(int) (int, int)) *nmt.NamespacedMerkleTree {
	tree := nmt.New(consts.NewBaseHashFunc(), nmt.NamespaceIDSize(share.NamespaceSize), nmt.IgnoreMaxNamespace(true))
	for i := range testSquareWidth {
		r, c := cell(i)
		ns := testNamespace
		if r >= testSquareWidth/2 || c >= testSquareWidth/2 {
			ns = share.ParitySharesNamespace.Bytes()
		}
		if err := tree.Push(append(append([]byte{}, ns...), sq.shares[r][c]...)); err != nil {
			panic(err)
		}
	}
	return tree
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

type rowRange struct{ row, start, end int }

// shareProof proves the given ranges, one per consecutive row, under ns.
func (sq testSquare) shareProof(ns []byte, ranges ...rowRange) ShareProof {
	sp := ShareProof{NamespaceID: ns[1:], NamespaceVersion: uint32(ns[0])}
	sp.RowProof.StartRow = uint32(ranges[0].row)
	sp.RowProof.EndRow = uint32(ranges[len(ranges)-1].row)
	for _, r := range ranges {
		p := must(sq.rowTrees[r.row].ProveRange(r.start, r.end))
		sp.Data = append(sp.Data, sq.shares[r.row][r.start:r.end]...)
		sp.ShareProofs = append(sp.ShareProofs, &tmproto.NMTProof{Start: int32(r.start), End: int32(r.end), Nodes: p.Nodes()})
		mp := *sq.proofs[r.row]
		sp.RowProof.RowRoots = append(sp.RowProof.RowRoots, tmbytes.HexBytes(sq.roots[r.row]))
		sp.RowProof.Proofs = append(sp.RowProof.Proofs, &mp)
	}
	return sp
}

func TestShareProofValidatePositions(t *testing.T) {
	sq := newTestSquare()
	parity := share.ParitySharesNamespace.Bytes()

	testCases := []struct {
		name    string
		sp      func() ShareProof
		wantErr bool
	}{
		{
			name:    "valid proof in one row",
			sp:      func() ShareProof { return sq.shareProof(testNamespace, rowRange{0, 0, 2}) },
			wantErr: false,
		},
		{
			name:    "valid proof across rows",
			sp:      func() ShareProof { return sq.shareProof(testNamespace, rowRange{0, 1, 2}, rowRange{1, 0, 1}) },
			wantErr: false,
		},
		{
			name: "total that is not a power of two",
			sp: func() ShareProof {
				sp := sq.shareProof(testNamespace, rowRange{0, 0, 2})
				sp.RowProof.Proofs[0].Total = 7
				return sp
			},
			wantErr: true,
		},
		{
			name:    "ranges that are not contiguous across rows",
			sp:      func() ShareProof { return sq.shareProof(testNamespace, rowRange{0, 1, 2}, rowRange{1, 1, 2}) },
			wantErr: true,
		},
		{
			name:    "parity row",
			sp:      func() ShareProof { return sq.shareProof(parity, rowRange{2, 0, 1}) },
			wantErr: true,
		},
		{
			name:    "parity column",
			sp:      func() ShareProof { return sq.shareProof(parity, rowRange{0, 2, 3}) },
			wantErr: true,
		},
		{
			name: "parity column relabeled past the row",
			sp: func() ShareProof {
				sp := sq.shareProof(parity, rowRange{0, 2, 3})
				sp.ShareProofs[0].Start, sp.ShareProofs[0].End = 4, 5
				return sp
			},
			wantErr: true,
		},
		{
			name: "namespace ID that is not 28 bytes",
			sp: func() ShareProof {
				sp := sq.shareProof(testNamespace, rowRange{0, 0, 2})
				sp.NamespaceID = append(sp.NamespaceID, 1)
				return sp
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sp := tc.sp()
			// Round trip through protobuf, as a client would receive the proof.
			sp, err := ShareProofFromProto(sp.ToProto())
			require.NoError(t, err)
			got := sp.Validate(sq.root)
			if tc.wantErr {
				require.Error(t, got)
				return
			}
			require.NoError(t, got)
		})
	}
}

func TestShareProofVerifyProofMalformed(t *testing.T) {
	sq := newTestSquare()

	sp := sq.shareProof(testNamespace, rowRange{0, 0, 2})
	sp.RowProof.RowRoots = nil
	require.False(t, sp.VerifyProof())

	sp = sq.shareProof(testNamespace, rowRange{0, 0, 2})
	sp.ShareProofs[0] = nil
	require.False(t, sp.VerifyProof())

	sp = sq.shareProof(testNamespace, rowRange{0, 0, 2})
	sp.Data = sp.Data[:1]
	require.False(t, sp.VerifyProof())
}
