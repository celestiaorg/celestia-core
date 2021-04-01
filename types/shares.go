package types

import (
	"bytes"
	"encoding/binary"

	"github.com/gogo/protobuf/proto"
	tmbytes "github.com/lazyledger/lazyledger-core/libs/bytes"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"
)

// Share contains the raw share data without the corresponding namespace.
type Share []byte

// NamespacedShare extends a Share with the corresponding namespace.
type NamespacedShare struct {
	Share
	ID namespace.ID
}

func (n NamespacedShare) NamespaceID() namespace.ID {
	return n.ID
}

func (n NamespacedShare) Data() []byte {
	return n.Share
}

// NamespacedShares is just a list of NamespacedShare elements.
// It can be used to extract the raw raw shares.
type NamespacedShares []NamespacedShare

// RawShares returns the raw shares that can be fed into the erasure coding
// library (e.g. rsmt2d).
func (ns NamespacedShares) RawShares() [][]byte {
	res := make([][]byte, len(ns))
	for i, nsh := range ns {
		res[i] = nsh.Share
	}
	return res
}

func (tx Tx) MarshalDelimited() ([]byte, error) {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	length := uint64(len(tx))
	n := binary.PutUvarint(lenBuf, length)
	return append(lenBuf[:n], tx...), nil
}

// MarshalDelimited marshals the raw data (excluding the namespace) of this
// message and prefixes it with the length of that encoding.
func (m Message) MarshalDelimited() ([]byte, error) {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	length := uint64(len(m.Data))
	n := binary.PutUvarint(lenBuf, length)
	return append(lenBuf[:n], m.Data...), nil
}

// appendToShares appends raw data as shares.
// Used for messages.
func appendToShares(shares []NamespacedShare, nid namespace.ID, rawData []byte) []NamespacedShare {
	if len(rawData) <= MsgShareSize {
		rawShare := append(append(
			make([]byte, 0, len(nid)+len(rawData)),
			nid...),
			rawData...,
		)
		paddedShare := zeroPadIfNecessary(rawShare, ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
	} else { // len(rawData) > MsgShareSize
		shares = append(shares, split(rawData, nid)...)
	}
	return shares
}

// splitContiguous splits multiple raw data contiguously as shares.
// Used for transactions, intermediate state roots, and evidence.
func splitContiguous(nid namespace.ID, rawDatas [][]byte) []NamespacedShare {
	shares := make([]NamespacedShare, 0)
	// Index into the outer slice of rawDatas
	outerIndex := 0
	// Index into the inner slice of rawDatas
	innerIndex := 0
	for outerIndex < len(rawDatas) {
		var rawData []byte
		startIndex := 0
		rawData, outerIndex, innerIndex, startIndex = getNextChunk(rawDatas, outerIndex, innerIndex, TxShareSize)
		rawShare := append(append(append(
			make([]byte, 0, len(nid)+ShareReservedBytes+len(rawData)),
			nid...),
			byte(startIndex)),
			rawData...)
		paddedShare := zeroPadIfNecessary(rawShare, ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
	}
	return shares
}

func split(rawData []byte, nid namespace.ID) []NamespacedShare {
	shares := make([]NamespacedShare, 0)
	firstRawShare := append(append(
		make([]byte, 0, ShareSize),
		nid...),
		rawData[:MsgShareSize]...,
	)
	shares = append(shares, NamespacedShare{firstRawShare, nid})
	rawData = rawData[MsgShareSize:]
	for len(rawData) > 0 {
		shareSizeOrLen := min(MsgShareSize, len(rawData))
		rawShare := append(append(
			make([]byte, 0, ShareSize),
			nid...),
			rawData[:shareSizeOrLen]...,
		)
		paddedShare := zeroPadIfNecessary(rawShare, ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
		rawData = rawData[shareSizeOrLen:]
	}
	return shares
}

// getNextChunk gets the next chunk for contiguous shares
// Precondition: none of the slices in rawDatas is zero-length
// This precondition should always hold at this point since zero-length txs are simply invalid.
func getNextChunk(rawDatas [][]byte, outerIndex int, innerIndex int, width int) ([]byte, int, int, int) {
	rawData := make([]byte, 0, width)
	startIndex := 0
	firstBytesToFetch := 0

	curIndex := 0
	for curIndex < width && outerIndex < len(rawDatas) {
		bytesToFetch := min(len(rawDatas[outerIndex])-innerIndex, width-curIndex)
		if bytesToFetch == 0 {
			panic("zero-length contiguous share data is invalid")
		}
		if curIndex == 0 {
			firstBytesToFetch = bytesToFetch
		}
		// If we've already placed some data in this chunk, that means
		// a new data segment begins
		if curIndex != 0 {
			// Offset by the fixed reserved bytes at the beginning of the share
			startIndex = firstBytesToFetch + NamespaceSize + ShareReservedBytes
		}
		rawData = append(rawData, rawDatas[outerIndex][innerIndex:innerIndex+bytesToFetch]...)
		innerIndex += bytesToFetch
		if innerIndex >= len(rawDatas[outerIndex]) {
			innerIndex = 0
			outerIndex++
		}
		curIndex += bytesToFetch
	}

	return rawData, outerIndex, innerIndex, startIndex
}

func GenerateTailPaddingShares(n int, shareWidth int) NamespacedShares {
	shares := make([]NamespacedShare, n)
	for i := 0; i < n; i++ {
		shares[i] = NamespacedShare{bytes.Repeat([]byte{0}, shareWidth), TailPaddingNamespaceID}
	}
	return shares
}

func min(a, b int) int {
	if a <= b {
		return a
	}
	return b
}

func zeroPadIfNecessary(share []byte, width int) []byte {
	oldLen := len(share)
	if oldLen < width {
		missingBytes := width - oldLen
		padByte := []byte{0}
		padding := bytes.Repeat(padByte, missingBytes)
		share = append(share, padding...)
		return share
	}
	return share
}

// DataFromSquare extracts block data from an extended data square.
func DataFromSquare(eds *rsmt2d.ExtendedDataSquare) (Data, error) {
	originalWidth := eds.Width() / 2

	// sort block data by namespace
	// define a slice for the raw share data of each type
	var (
		// transactions
		txsShares [][]byte
		// intermediate state roots
		isrShares [][]byte
		// evidence
		evdShares [][]byte
		// messages
		msgShares [][]byte
	)

	// iterate over each row index
	for x := uint(0); x < originalWidth; x++ {
		// iterate over each col index
		for y := uint(0); y < originalWidth; y++ {
			// sort the data of that share types via namespace
			share := eds.Cell(x, y)
			nid := share[:NamespaceSize]
			switch {
			case bytes.Equal(TxNamespaceID, nid):
				txsShares = append(txsShares, share)

			case bytes.Equal(IntermediateStateRootsNamespaceID, nid):
				isrShares = append(isrShares, share)

			case bytes.Equal(EvidenceNamespaceID, nid):
				evdShares = append(evdShares, share)

			case bytes.Equal(TailPaddingNamespaceID, nid):
				continue

			// every other namespaceID should be a message
			default:
				msgShares = append(msgShares, share)
			}
		}
	}

	// pass the raw share data to their respective parsers
	txs, err := parseTxs(txsShares)
	if err != nil {
		return Data{}, err
	}

	isrs, err := parseIsrs(isrShares)
	if err != nil {
		return Data{}, err
	}

	evd, err := parseEvd(evdShares)
	if err != nil {
		return Data{}, err
	}

	msgs, err := parseMsgs(msgShares)
	if err != nil {
		return Data{}, err
	}

	return Data{
		Txs:                    txs,
		IntermediateStateRoots: isrs,
		Evidence:               evd,
		Messages:               msgs,
	}, nil
}

// parseTxs collects all of the transactions from the shares provided
func parseTxs(shares [][]byte) (Txs, error) {
	// parse the sharse
	rawTxs, err := processContiguousShares(shares)
	if err != nil {
		return nil, err
	}

	// convert to the Tx type
	txs := make(Txs, len(rawTxs))
	for i := 0; i < len(txs); i++ {
		txs[i] = Tx(rawTxs[i])
	}

	return txs, nil
}

// parseIsrs collects all the intermediate state roots from the shares provided
func parseIsrs(shares [][]byte) (IntermediateStateRoots, error) {
	rawISRs, err := processContiguousShares(shares)
	if err != nil {
		return IntermediateStateRoots{}, err
	}

	ISRs := make([]tmbytes.HexBytes, len(rawISRs))
	for i := 0; i < len(ISRs); i++ {
		ISRs[i] = rawISRs[i]
	}

	return IntermediateStateRoots{RawRootsList: ISRs}, nil
}

// parseEvd collects all evidence from the shares provided.
func parseEvd(shares [][]byte) (EvidenceData, error) {
	// the raw data returned does not have length delimiters or namespaces and
	// is ready to be unmarshaled
	rawEvd, err := processContiguousShares(shares)
	if err != nil {
		return EvidenceData{}, err
	}

	evdList := make(EvidenceList, len(rawEvd))

	// parse into protobuf bytes
	for i := 0; i < len(rawEvd); i++ {
		// unmarshal the evidence
		var protoEvd *tmproto.Evidence
		err := proto.Unmarshal(rawEvd[i], protoEvd)
		if err != nil {
			return EvidenceData{}, err
		}

		evd, err := EvidenceFromProto(protoEvd)
		if err != nil {
			return EvidenceData{}, err
		}

		evdList[i] = evd
	}

	return EvidenceData{Evidence: evdList}, nil
}

// parseMsgs collects all messages from the shares provided
func parseMsgs(shares [][]byte) (Messages, error) {
	msgList, err := parseMsgShares(shares)
	if err != nil {
		return MessagesEmpty, err
	}

	return Messages{
		MessagesList: msgList,
	}, nil
}

// processContiguousShares takes raw shares and extracts out transactions,
// intermediate state roots, or evidence. The returned [][]byte do have
// namespaces or length delimiters and are ready to be unmarshalled
func processContiguousShares(shares [][]byte) (txs [][]byte, err error) {
	if len(shares) == 0 {
		return nil, err
	}
	share := shares[0][NamespaceSize+ShareReservedBytes:]
	share, txLen, err := parseDelimiter(share)
	if err != nil {
		return nil, err
	}

	for i := 0; i < len(shares); i++ {
		var newTxs [][]byte
		newTxs, share, txLen, err = collectTxsFromShare(share, txLen)
		if err != nil {
			return nil, err
		}

		// add the collected txs to the output
		txs = append(txs, newTxs...)

		// if there is no next share, the work is done
		// if not, then we know there are more shares to process
		if len(shares) == i+1 {
			break
		}

		nextShare := shares[i+1][NamespaceSize+ShareReservedBytes:]

		// if there is no current share remaining, process the next share
		if len(share) == 0 {
			share, txLen, err = parseDelimiter(nextShare)
			if err != nil {
				break
			}
			continue
		}

		// create the next share by merging the next share with the remaining of
		// the current share
		share = append(share, nextShare...)
	}

	return txs, err
}

func collectTxsFromShare(share []byte, txLen uint64) (txs [][]byte, extra []byte, l uint64, err error) {
	for uint64(len(share)) >= txLen {
		tx := share[:txLen]
		if len(tx) == 0 {
			share = nil
			break
		}

		txs = append(txs, tx)
		share = share[txLen:]

		share, txLen, err = parseDelimiter(share)
		if txLen == 0 || err != nil {
			break
		}
	}
	return txs, share, txLen, err
}

// parseMessages iterates through raw shares and separates the contiguous chunks
// of data. we use this for transactions, evidence, and intermediate state roots
func parseMsgShares(shares [][]byte) ([]Message, error) {
	if len(shares) == 0 {
		return nil, nil
	}

	// set the first nid and current share
	nid := shares[0][:NamespaceSize]
	currentShare := shares[0][NamespaceSize:]

	// find and remove the msg len delimiter
	currentShare, msgLen, err := parseDelimiter(currentShare)
	if err != nil {
		return nil, err
	}

	var msgs []Message
	for cursor := uint64(0); cursor < uint64(len(shares)); {
		var msg Message
		currentShare, nid, cursor, msgLen, msg, err = nextMsg(
			shares,
			currentShare,
			nid,
			cursor,
			msgLen,
		)
		if err != nil {
			return nil, err
		}
		if msg.Data != nil {
			msgs = append(msgs, msg)
		}
	}

	return msgs, nil
}

func nextMsg(shares [][]byte, current, nid []byte, cursor, l uint64) ([]byte, []byte, uint64, uint64, Message, error) {
	switch {
	// the message uses all of the current share data and at least some of the
	// next share
	case l > uint64(len(current)):
		// add the next share to the current one and try again
		cursor++
		current = append(current, shares[cursor][NamespaceSize:]...)
		return nextMsg(shares, current, nid, cursor, l)

	// the msg we're looking for is contained in the current share
	case l <= uint64(len(current)):
		msg := Message{nid, current[:l]}
		cursor++

		// call it a day if the work is done
		if cursor >= uint64(len(shares)) {
			return nil, nil, cursor, 0, msg, nil
		}

		nextNid := shares[cursor][:NamespaceSize]
		next, msgLen, err := parseDelimiter(shares[cursor][NamespaceSize:])
		return next, nextNid, cursor, msgLen, msg, err
	}
	// this code is unreachable but the compiler doesn't know that
	return nil, nil, 0, 0, MessageEmpty, nil
}

// parseDelimiter finds and returns the length delimiter of the message provided
// while also removing the delimiter bytes from the input
func parseDelimiter(input []byte) ([]byte, uint64, error) {
	if len(input) == 0 {
		return input, 0, nil
	}

	l := binary.MaxVarintLen64
	if len(input) < binary.MaxVarintLen64 {
		l = len(input)
	}

	// read the length of the message
	r := bytes.NewBuffer(input[:l])
	msgLen, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, 0, err
	}

	// calculate the number of bytes used by the delimiter
	lenBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(lenBuf, msgLen)

	// return the input without the length delimiter
	return input[n:], msgLen, nil
}
