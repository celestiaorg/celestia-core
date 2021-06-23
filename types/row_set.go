package types

import (
	"fmt"
	"sync"

	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/libs/bits"
	"github.com/lazyledger/lazyledger-core/p2p/ipld"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
	"github.com/lazyledger/lazyledger-core/types/consts"
)

type Row []byte

func NewRow(row [][]byte) Row {
	r := make([]byte, 0, len(row[0])*len(row))
	for j := 0; j < len(row); j++ {
		r = append(r, row[j]...)
	}
	return r
}

type RowSet struct {
	DAHeader *ipld.DataAvailabilityHeader
	rowsHave *bits.BitArray
	rows     [][]byte
	l sync.RWMutex
}

func NewRowSet(eds *rsmt2d.ExtendedDataSquare) *RowSet {
	width := int(eds.Width())
	rowsHave := bits.NewBitArray(width)
	rows := make([][]byte, width)
	for i := 0; i < width; i++ {
		rowsHave.SetIndex(i, true)
		rows[i] = NewRow(eds.Row(uint(i)))
	}

	return &RowSet{
		DAHeader: ipld.MakeDataHeader(eds),
		rowsHave: rowsHave,
		rows:     rows,
	}
}

func NewRowSetFromHeader(dah *ipld.DataAvailabilityHeader) *RowSet {
	return &RowSet{
		DAHeader: dah,
		rowsHave: bits.NewBitArray(len(dah.RowsRoots)),
		rows:     make([][]byte, len(dah.RowsRoots)),
	}
}

func (rs *RowSet) Count() int {
	return rs.rowsHave.Ones()
}

func (rs *RowSet) Total() int {
	return rs.rowsHave.Size()
}

func (rs *RowSet) IsComplete() bool {
	return rs.rowsHave.IsFull()
}

func (rs *RowSet) BitArray() *bits.BitArray {
	return rs.rowsHave.Copy()
}

func (rs *RowSet) Square() (*rsmt2d.ExtendedDataSquare, error) {
	if !rs.IsComplete() {
		return nil, fmt.Errorf("square is not complete")
	}
	return rsmt2d.ImportExtendedDataSquare(
		rs.rows,
		rsmt2d.NewRSGF8Codec(),
		wrapper.NewErasuredNamespacedMerkleTree(uint64(len(rs.rows))).Constructor,
	)
}

func (rs *RowSet) HasHeader(dah *ipld.DataAvailabilityHeader) bool {
	return rs.DAHeader.Equals(dah)
}

func (rs *RowSet) GetRow(i int) Row {
	if i > len(rs.rows) {
		return nil
	}
	rs.l.RLock()
	defer rs.l.RUnlock()
	return rs.rows[i]
}

func (rs *RowSet) AddRow(row Row) (bool, error) {
	rs.l.Lock()
	defer rs.l.Unlock()

	ss := consts.ShareSize
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(len(rs.rows)))
	for i := 0; i < len(row)/ss; i++ {
		tree.Push(row[i*ss:(i+1)*ss], rsmt2d.SquareIndex{Cell: uint(i)})
	}

	root, err := namespace.IntervalDigestFromBytes(ipld.NamespaceSize, tree.Root())
	if err != nil {
		return false, err
	}

	idx, isFound := findRoot(rs.DAHeader, &root)
	if !isFound {
		return false, fmt.Errorf("invalid row")
	}

	rs.rows[idx] = row
	rs.rowsHave.SetIndex(idx, true)
	return true, nil
}

func findRoot(dah *ipld.DataAvailabilityHeader, root *namespace.IntervalDigest) (int, bool) {
	for i, rr := range dah.RowsRoots {
		if rr.Equal(root) {
			return i, true
		}
	}

	return 0, false
}
