package types

import (
	"errors"
	"fmt"
	"sync"

	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"

	"github.com/lazyledger/lazyledger-core/libs/bits"
	"github.com/lazyledger/lazyledger-core/p2p/ipld"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/wrapper"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	"github.com/lazyledger/lazyledger-core/types/consts"
)

var ErrInvalidRow = errors.New("invalid row")

type Row struct {
	Index int
	Data  []byte
}

func NewRow(idx int, row [][]byte) *Row {
	data := make([]byte, len(row)*ipld.ShareSize)
	for i := 0; i < len(row); i++ {
		copy(data[i*ipld.ShareSize:(i+1)*ipld.ShareSize], row[i])
	}
	return &Row{Index: idx, Data: data}
}

func (r *Row) ForEachShare(f func(int, []byte)) {
	for i := 0; i < r.Shares(); i++ {
		f(i, r.Data[i*ipld.ShareSize:(i+1)*ipld.ShareSize])
	}
}

func (r *Row) Shares() int {
	return len(r.Data) / ipld.ShareSize
}

// TODO(Wondertan): Ideally, it would be nice to check that Row corresponds to DAHeader here(on Row message is received)
//  but:
//  * Then we would need to calculate Tree for Row twice or more
//  * Violate ValidateBasic pattern by passing header there are reworking reactor logic.
//  This way we can also sending Row Index and only rely on Hash.
func (r *Row) ValidateBasic() error {
	if len(r.Data)%ipld.ShareSize != 0 {
		return fmt.Errorf("row's data must be multople of share size(%d)", ipld.ShareSize)
	}
	return nil
}

func (r *Row) ToProto() (*tmproto.Row, error) {
	if r == nil {
		return nil, errors.New("nil part")
	}
	pb := new(tmproto.Row)
	pb.Index = uint32(r.Index)
	pb.Data = r.Data
	return pb, nil
}

func RowFromProto(pb *tmproto.Row) (*Row, error) {
	if pb == nil {
		return nil, errors.New("nil part")
	}

	part := new(Row)
	part.Index = int(pb.Index)
	part.Data = pb.Data
	return part, part.ValidateBasic()
}

type RowSet struct {
	DAHeader *ipld.DataAvailabilityHeader

	rowsHave *bits.BitArray
	rows     []*Row
	l        sync.RWMutex
}

func NewRowSet(eds *rsmt2d.ExtendedDataSquare) *RowSet {
	width := int(eds.Width())
	rowsHave := bits.NewBitArray(width)
	rows := make([]*Row, width)
	for i := 0; i < width; i++ {
		rowsHave.SetIndex(i, true)
		rows[i] = NewRow(i, eds.Row(uint(i)))
	}

	dah := ipld.MakeDataHeader(eds)
	dah.Hash() // cache hash earlier to avoid races
	return &RowSet{
		DAHeader: dah,
		rowsHave: rowsHave,
		rows:     rows,
	}
}

func NewRowSetFromHeader(dah *ipld.DataAvailabilityHeader) *RowSet {
	dah.Hash() // ensure hash is cached to avoid races
	return &RowSet{
		DAHeader: dah,
		rowsHave: bits.NewBitArray(len(dah.RowsRoots)),
		rows:     make([]*Row, len(dah.RowsRoots)),
	}
}

func (rs *RowSet) StringShort() string {
	if rs == nil {
		return "nil-RowSet"
	}
	return fmt.Sprintf("(%v of %v)", rs.Count(), rs.Total())
}

func (rs *RowSet) Count() int {
	if rs == nil {
		return 0
	}
	return rs.rowsHave.Ones()
}

func (rs *RowSet) Total() int {
	if rs == nil {
		return 0
	}
	return rs.rowsHave.Size()
}

func (rs *RowSet) TotalSize() int {
	if rs == nil {
		return 0
	}
	return rs.Total() * rs.SquareSize() * consts.ShareSize
}

func (rs *RowSet) IsComplete() bool {
	if rs == nil {
		return false
	}
	return rs.rowsHave.IsFull()
}

func (rs *RowSet) BitArray() *bits.BitArray {
	if rs == nil {
		return bits.NewBitArray(0)
	}
	return rs.rowsHave.Copy()
}

func (rs *RowSet) Square() (*rsmt2d.ExtendedDataSquare, error) {
	if !rs.IsComplete() {
		return nil, fmt.Errorf("square is not complete")
	}
	size := rs.SquareSize()
	shares := make([][]byte, size*size)
	for i, r := range rs.rows {
		r.ForEachShare(func(j int, share []byte) {
			shares[(i*size)+j] = share
		})
	}
	return rsmt2d.ImportExtendedDataSquare(
		shares,
		rsmt2d.NewRSGF8Codec(),
		wrapper.NewErasuredNamespacedMerkleTree(uint64(size/2)).Constructor,
	)
}

func (rs *RowSet) SquareSize() int {
	if rs == nil {
		return 0
	}
	return len(rs.DAHeader.RowsRoots)
}

func (rs *RowSet) HasHeader(dah *ipld.DataAvailabilityHeader) bool {
	if rs == nil {
		return false
	}
	return rs.DAHeader.Equals(dah)
}

func (rs *RowSet) GetRow(i int) *Row {
	if i > len(rs.rows) {
		return nil
	}
	rs.l.RLock()
	defer rs.l.RUnlock()
	return rs.rows[i]
}

func (rs *RowSet) AddRow(row *Row) (bool, error) {
	if rs == nil {
		return false, nil
	}
	if rs.rowsHave.GetIndex(row.Index) {
		return false, nil
	}
	if row.Shares() != rs.SquareSize() {
		return false, ErrInvalidRow
	}

	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(rs.SquareSize() / 2))
	row.ForEachShare(func(i int, data []byte) {
		tree.Push(data, rsmt2d.SquareIndex{Axis: uint(row.Index), Cell: uint(i)})
	})

	root, err := namespace.IntervalDigestFromBytes(ipld.NamespaceSize, tree.Root())
	if err != nil {
		return false, err
	}

	if !rs.DAHeader.RowsRoots[row.Index].Equal(&root) {
		return false, ErrInvalidRow
	}

	rs.l.Lock()
	rs.rows[row.Index] = row
	rs.l.Unlock()
	rs.rowsHave.SetIndex(row.Index, true)
	return true, nil
}
