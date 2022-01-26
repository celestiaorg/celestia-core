package core

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/celestiaorg/nmt"
	"github.com/celestiaorg/rsmt2d"
	"github.com/tendermint/tendermint/pkg/consts"
	"github.com/tendermint/tendermint/pkg/wrapper"
	"github.com/tendermint/tendermint/types"
)

const (
	usableShareSize = consts.ShareSize - consts.NamespaceSize - consts.ShareReservedBytes
)

// ProveTxInclusion uses the provided block data to progressively generate rows
// of a data square, and then using those shares to creates nmt inclusion proofs
// It is possible that a transaction spans more than one row. In that case, we
// have to return two proofs.
func ProveTxInclusion(codec rsmt2d.Codec, data types.Data, origSquareSize, txIndex int) ([]nmt.Proof, error) {
	squareSize := origSquareSize * 2
	startPos, endPos := txSharePosition(data.Txs, txIndex)
	startRow := startPos / squareSize
	endRow := endPos / squareSize

	if (endPos - startPos) > 1 {
		return nil, errors.New("transaction spanned more than two shares, this is not yet supported")
	}

	rowShares := genRowShares(consts.DefaultCodec(), data, origSquareSize, startRow, endRow)

	var proofs []nmt.Proof
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

		var pos int
		if i == 0 {
			pos = startPos - (startRow * squareSize)
		} else {
			pos = endPos - (endRow * squareSize)
		}
		proof, err := tree.Prove(pos)
		if err != nil {
			return nil, err
		}
		proofs = append(proofs, proof)
	}

	return proofs, nil
}

// txSharePosition returns the share that a given transaction is included in.
// returns -1 if index is greater than that of the provided txs.
func txSharePosition(txs types.Txs, txIndex int) (startSharePos, endSharePos int) {
	if txIndex >= len(txs) {
		return -1, -1
	}

	totalLen := 0
	for i := 0; i < txIndex; i++ {
		txLen := len(txs[i])
		totalLen += (delimLen(txLen) + txLen)
	}

	txLen := len(txs[txIndex])

	startSharePos = (totalLen) / usableShareSize
	endSharePos = (totalLen + txLen + delimLen(txLen)) / usableShareSize

	return startSharePos, endSharePos
}

func delimLen(txLen int) int {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	return binary.PutUvarint(lenBuf, uint64(txLen))
}

// genRowShares progessively generates data square rows from block data
func genRowShares(codec rsmt2d.Codec, data types.Data, origSquareSize, startRow, endRow int) [][][]byte {
	if endRow > origSquareSize {
		panic("cannot generate row shares past the original square size")
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

	return encodedRowShares
}

// genOrigRowShares progressively generates data square rows for the original
// data square, meaning the rows only half the full square length, as there is
// not erasure data
func genOrigRowShares(data types.Data, originalSquareSize, startRow, endRow int) [][]byte {
	wantLen := (endRow + 1) * originalSquareSize
	startPos := startRow * originalSquareSize

	shares := data.Txs.SplitIntoShares()
	// return if we have enough shares
	if len(shares) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	shares = append(shares, data.IntermediateStateRoots.SplitIntoShares()...)
	if len(shares) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	shares = append(shares, data.Evidence.SplitIntoShares()...)
	if len(shares) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	for _, m := range data.Messages.MessagesList {
		rawData, err := m.MarshalDelimited()
		if err != nil {
			panic(fmt.Sprintf("app accepted a Message that can not be encoded %#v", m))
		}
		shares = types.AppendToShares(shares, m.NamespaceID, rawData)

		// return if we have enough shares
		if len(shares) >= wantLen {
			return shares[startPos:wantLen].RawShares()
		}
	}

	tailShares := types.TailPaddingShares(wantLen - len(shares))
	shares = append(shares, tailShares...)

	return shares[startPos:wantLen].RawShares()
}

// splitIntoRows splits shares into rows of a particular square size
func splitIntoRows(origSquareSize int, shares [][]byte) [][][]byte {
	rowCount := len(shares) / origSquareSize
	rows := make([][][]byte, rowCount)
	for i := 0; i < rowCount; i++ {
		rows[i] = shares[i*origSquareSize : (i+1)*origSquareSize]
	}
	return rows
}
