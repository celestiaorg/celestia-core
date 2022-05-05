package prove

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/celestiaorg/rsmt2d"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/pkg/consts"
	"github.com/tendermint/tendermint/pkg/wrapper"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/types"
)

// TxInclusion uses the provided block data to progressively generate rows
// of a data square, and then using those shares to creates nmt inclusion proofs
// It is possible that a transaction spans more than one row. In that case, we
// have to return two proofs.
func TxInclusion(codec rsmt2d.Codec, data types.Data, origSquareSize, txIndex uint) (types.TxProof, error) {
	// calculate the index of the shares that contain the tx
	startPos, endPos, err := txSharePosition(data.Txs, txIndex)
	if err != nil {
		return types.TxProof{}, err
	}
	if (endPos - startPos) > 1 {
		return types.TxProof{}, errors.New("transaction spanned more than two shares, this is not yet supported")
	}

	// use the index of the shares and the square size to determine the row that
	// contains the tx we need to prove
	startRow := startPos / origSquareSize
	endRow := endPos / origSquareSize

	rowShares, err := genRowShares(codec, data, origSquareSize, startRow, endRow)
	if err != nil {
		return types.TxProof{}, err
	}

	var proofs []*tmproto.NMTProof  //nolint:prealloc // rarely will this contain more than a single proof
	var shares [][]byte             //nolint:prealloc // rarely will this contain more than a single share
	var rowRoots []tmbytes.HexBytes //nolint:prealloc // rarely will this contain more than a single root
	for i, row := range rowShares {
		// create an nmt to use to generate a proof
		tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(origSquareSize))
		for j, share := range row {
			tree.Push(
				share,
				rsmt2d.SquareIndex{
					Axis: uint(i),
					Cell: uint(j),
				},
			)
		}

		var pos uint
		if i == 0 {
			pos = startPos - (startRow * origSquareSize)
		} else {
			pos = endPos - (endRow * origSquareSize)
		}

		shares = append(shares, row[pos])

		proof, err := tree.Prove(int(pos))
		if err != nil {
			return types.TxProof{}, err
		}

		proofs = append(proofs, &tmproto.NMTProof{
			Start:    int32(proof.Start()),
			End:      int32(proof.End()),
			Nodes:    proof.Nodes(),
			LeafHash: proof.LeafHash(),
		})

		// we don't store the data availability header anywhere, so we
		// regenerate the roots to each row
		rowRoots = append(rowRoots, tree.Root())
	}

	return types.TxProof{
		RowRoots: rowRoots,
		Data:     shares,
		Proofs:   proofs,
	}, nil
}

// txSharePosition returns the share that a given transaction is included in.
// returns -1 if index is greater than that of the provided txs.
func txSharePosition(txs types.Txs, txIndex uint) (startSharePos, endSharePos uint, err error) {
	if txIndex >= uint(len(txs)) {
		return startSharePos, endSharePos, errors.New("transaction index is greater than the number of txs")
	}

	totalLen := 0
	for i := uint(0); i < txIndex; i++ {
		txLen := len(txs[i])
		totalLen += (delimLen(txLen) + txLen)
	}

	txLen := len(txs[txIndex])

	startSharePos = uint((totalLen) / consts.TxShareSize)
	endSharePos = uint((totalLen + txLen + delimLen(txLen)) / consts.TxShareSize)

	return startSharePos, endSharePos, nil
}

func delimLen(txLen int) int {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	return binary.PutUvarint(lenBuf, uint64(txLen))
}

// genRowShares progessively generates data square rows from block data
func genRowShares(codec rsmt2d.Codec, data types.Data, origSquareSize, startRow, endRow uint) ([][][]byte, error) {
	if endRow > origSquareSize {
		return nil, errors.New("cannot generate row shares past the original square size")
	}
	origRowShares := splitIntoRows(
		origSquareSize,
		genOrigRowShares(data, origSquareSize, startRow, endRow),
	)

	encodedRowShares := make([][][]byte, len(origRowShares))
	for i, row := range origRowShares {
		encRow, err := codec.Encode(row)
		if err != nil {
			panic(err)
		}
		encodedRowShares[i] = append(
			append(
				make([][]byte, 0, len(row)+len(encRow)),
				row...,
			), encRow...,
		)
	}

	return encodedRowShares, nil
}

// genOrigRowShares progressively generates data square rows for the original
// data square, meaning the rows only half the full square length, as there is
// not erasure data
func genOrigRowShares(data types.Data, originalSquareSize, startRow, endRow uint) [][]byte {
	wantLen := (endRow + 1) * originalSquareSize
	startPos := startRow * originalSquareSize

	shares := data.Txs.SplitIntoShares()
	// return if we have enough shares
	if uint(len(shares)) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	shares = append(shares, data.Evidence.SplitIntoShares()...)
	if uint(len(shares)) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	for _, m := range data.Messages.MessagesList {
		rawData, err := m.MarshalDelimited()
		if err != nil {
			panic(fmt.Sprintf("app accepted a Message that can not be encoded %#v", m))
		}
		shares = types.AppendToShares(shares, m.NamespaceID, rawData)

		// return if we have enough shares
		if uint(len(shares)) >= wantLen {
			return shares[startPos:wantLen].RawShares()
		}
	}

	tailShares := types.TailPaddingShares(int(wantLen) - len(shares))
	shares = append(shares, tailShares...)

	return shares[startPos:wantLen].RawShares()
}

// splitIntoRows splits shares into rows of a particular square size
func splitIntoRows(origSquareSize uint, shares [][]byte) [][][]byte {
	rowCount := uint(len(shares)) / origSquareSize
	rows := make([][][]byte, rowCount)
	for i := uint(0); i < rowCount; i++ {
		rows[i] = shares[i*origSquareSize : (i+1)*origSquareSize]
	}
	return rows
}
