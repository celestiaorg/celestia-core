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

// TxInclusion returns a proof that can be used to verify txIndex was included
// in the provided block data. It uses the provided block data to progressively
// generate rows of a data square. Then it uses the shares in these rows to
// creates nmt inclusion proofs. It is possible that a transaction spans more
// than one row, in which case the TxProof returned by this function will
// contain multiple proofs.
func TxInclusion(codec rsmt2d.Codec, data types.Data, txIndex uint64) (types.TxProof, error) {
	// calculate the index of the shares that contain the tx
	startPos, endPos, err := txSharePosition(data.Txs, txIndex)
	if err != nil {
		return types.TxProof{}, err
	}

	// use the index of the shares and the square size to determine the row(s)
	// that contains the tx we need to prove
	startRow := startPos / data.OriginalSquareSize
	endRow := endPos / data.OriginalSquareSize
	startLeaf := startPos % data.OriginalSquareSize
	endLeaf := endPos % data.OriginalSquareSize

	rowShares, err := genRowShares(codec, data, startRow, endRow)
	if err != nil {
		return types.TxProof{}, err
	}

	var proofs []*tmproto.NMTProof //nolint:prealloc // rarely will this contain more than a single proof
	// the comment below seems inaccurate because messages can span multiple shares
	var shares [][]byte             //nolint:prealloc // rarely will this contain more than a single share
	var rowRoots []tmbytes.HexBytes //nolint:prealloc // rarely will this contain more than a single root
	for i, row := range rowShares {
		// create an nmt to use to generate a proof
		tree := wrapper.NewErasuredNamespacedMerkleTree(data.OriginalSquareSize)
		for j, share := range row {
			tree.Push(
				share,
				rsmt2d.SquareIndex{
					Axis: uint(i),
					Cell: uint(j),
				},
			)
		}

		startLeafPos := startLeaf
		endLeafPos := endLeaf

		// if this is not the first row, then start with the first leaf
		if i > 0 {
			startLeafPos = 0
		}
		// Question: why select for the rest of the row?
		// if this is not the last row, then select for the rest of the row
		if i != (len(rowShares) - 1) {
			endLeafPos = data.OriginalSquareSize - 1
		}

		shares = append(shares, row[startLeafPos:endLeafPos+1]...)

		proof, err := tree.Tree().ProveRange(int(startLeafPos), int(endLeafPos+1))
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
		// regenerate the roots to every row that contains data for this txIndex
		rowRoots = append(rowRoots, tree.Root())
	}

	return types.TxProof{
		RowRoots: rowRoots,
		Data:     shares,
		Proofs:   proofs,
	}, nil
}

// txSharePosition returns the start and end positions for shares that contain
// the provided txIndex. returns an error if txIndex is greater than that of the
// provided txs.
func txSharePosition(txs types.Txs, txIndex uint64) (startSharePos, endSharePos uint64, err error) {
	if txIndex >= uint64(len(txs)) {
		return startSharePos, endSharePos, errors.New("transaction index is greater than the number of txs")
	}

	totalLen := 0
	for i := uint64(0); i < txIndex; i++ {
		txLen := len(txs[i])
		totalLen += (delimLen(txLen) + txLen)
	}

	txLen := len(txs[txIndex])

	startSharePos = uint64((totalLen) / consts.TxShareSize)
	endSharePos = uint64((totalLen + txLen + delimLen(txLen)) / consts.TxShareSize)

	return startSharePos, endSharePos, nil
}

func delimLen(txLen int) int {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	return binary.PutUvarint(lenBuf, uint64(txLen))
}

// genRowShares progessively generates data square rows from block data
func genRowShares(codec rsmt2d.Codec, data types.Data, startRow, endRow uint64) ([][][]byte, error) {
	if endRow > data.OriginalSquareSize {
		return nil, errors.New("cannot generate row shares past the original square size")
	}
	origRowShares := splitIntoRows(
		data.OriginalSquareSize,
		genOrigRowShares(data, startRow, endRow),
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
func genOrigRowShares(data types.Data, startRow, endRow uint64) [][]byte {
	wantLen := (endRow + 1) * data.OriginalSquareSize
	startPos := startRow * data.OriginalSquareSize

	shares := data.Txs.SplitIntoShares()
	// return if we have enough shares
	if uint64(len(shares)) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	evdShares := data.Evidence.SplitIntoShares()

	shares = append(shares, evdShares...)
	if uint64(len(shares)) >= wantLen {
		return shares[startPos:wantLen].RawShares()
	}

	for _, m := range data.Messages.MessagesList {
		rawData, err := m.MarshalDelimited()
		if err != nil {
			panic(fmt.Sprintf("app accepted a Message that can not be encoded %#v", m))
		}
		shares = types.AppendToShares(shares, m.NamespaceID, rawData)

		// return if we have enough shares
		if uint64(len(shares)) >= wantLen {
			return shares[startPos:wantLen].RawShares()
		}
	}

	tailShares := types.TailPaddingShares(int(wantLen) - len(shares))
	shares = append(shares, tailShares...)

	return shares[startPos:wantLen].RawShares()
}

// splitIntoRows splits shares into rows of a particular square size
func splitIntoRows(origSquareSize uint64, shares [][]byte) [][][]byte {
	rowCount := uint64(len(shares)) / origSquareSize
	rows := make([][][]byte, rowCount)
	for i := uint64(0); i < rowCount; i++ {
		rows[i] = shares[i*origSquareSize : (i+1)*origSquareSize]
	}
	return rows
}
