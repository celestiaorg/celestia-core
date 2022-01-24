package core

import (
	"encoding/binary"
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

// ProveTxInclusion creates nmt inclusion proofs by using the provided block
// data to progressively generate rows of a data square, and then using those
// shares to creates nmt proofs
func ProveTxInclusion(codec rsmt2d.Codec, data types.Data, squareSize, txIndex int) ([]nmt.Proof, error) {
	startPos, endPos := txSharePosition(data.Txs, txIndex)
	startRow := startPos / squareSize
	// endRow := endPos / squareSize

	rowShares := generateRowShares(data, squareSize, startPos, endPos)
	erasureRowShares, err := codec.Encode(rowShares)
	if err != nil {
		return nil, err
	}

	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(squareSize))

	for i := 0; i < len(erasureRowShares); i++ {
		tree.Push(
			erasureRowShares[i],
			rsmt2d.SquareIndex{
				Axis: uint(0),
				Cell: uint(i),
			},
		)
	}
	pos := startPos - (squareSize * startRow)
	fmt.Println("deets", "square", squareSize, "pos", pos, "txindx", txIndex, "startPos", startPos, "share", rowShares[0][:20])

	startProof, err := tree.Prove(pos)
	if err != nil {
		fmt.Println("proove error")
		return nil, err
	}

	if startPos == endPos {
		return []nmt.Proof{startProof}, nil
	}

	endProof, err := tree.Prove(endPos)
	if err != nil {
		return nil, err
	}

	return []nmt.Proof{startProof, endProof}, nil
}

// txSharePosition returns the share that a given transaction is included in.
// returns -1 if index is greater than that of the provided txs.
func txSharePosition(txs types.Txs, txIndex int) (startSharePos, endSharePos int) {
	if txIndex > len(txs) {
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

// generateRowShares creates the row(s) needed to cover a set of shares from
// index startSharePos to endSharePos by progressively generating each row until
// they are included. It returns those shares as a flattened slice. If the
// specified shares are contained within a single row, it will result in
// 'squareSize' length [][]bytes being returned, but if the range of shares are
// split across multiple shares, and those shares are on different rows of the
// square, it will return a [][]byte that is n * 'squareSize'
func generateRowShares(data types.Data, squareSize, startSharePos, endSharePos int) [][]byte {
	// find the rows we need and their positions
	txStartRow := (startSharePos + 1) / squareSize
	txEndRow := (endSharePos + 1) / squareSize
	firstSharePos := txStartRow * squareSize
	lastSharePos := ((txEndRow+1)*squareSize - 1)

	// start by generating the tx shares
	txShares := data.Txs.SplitIntoShares().RawShares()

	// check if we already have enought shares to create the proof
	// find the row at which the last tx share ends
	txSharesEndRow := len(txShares) / squareSize

	// compare that to row(s) that we need to prove the tx
	// to see if we need to generate more shares
	if txEndRow < txSharesEndRow {
		// the tx is in a row we already have shares for
		return txShares[firstSharePos:lastSharePos]
	}

	// we need more shares
	shares := txShares

	// try completing the row by adding the ISR shares
	shares = append(shares, data.IntermediateStateRoots.SplitIntoShares().RawShares()...)
	// check if we have enough shares
	if len(shares) >= lastSharePos {
		return shares[firstSharePos:lastSharePos]
	}

	// try completing the row by adding evidence shares
	shares = append(shares, data.Evidence.SplitIntoShares().RawShares()...)
	// check if we have enough shares
	if len(shares) >= lastSharePos {
		return shares[firstSharePos:lastSharePos]
	}

	// progressively add message shares until there are enough shares in the row
	for i := 0; len(shares) < lastSharePos; i++ {
		shares = append(shares, types.Messages{
			MessagesList: []types.Message{data.Messages.MessagesList[i]},
		}.SplitIntoShares().RawShares()...)
	}

	return shares[firstSharePos:lastSharePos]
}

func delimLen(txLen int) int {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	return binary.PutUvarint(lenBuf, uint64(txLen))
}
