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
	out := append(lenBuf[:n], tx...)

	return out, nil
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
		rawData, outerIndex, innerIndex, _ = getNextChunk(rawDatas, outerIndex, innerIndex, MsgShareSize)
		rawShare := append(append(
			make([]byte, 0, MsgShareSize),
			nid...),
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
		make([]byte, 0, len(nid)+len(rawData[:MsgShareSize])),
		nid...),
		rawData[:MsgShareSize]...,
	)
	shares = append(shares, NamespacedShare{firstRawShare, nid})
	rawData = rawData[MsgShareSize:]
	for len(rawData) > 0 {
		shareSizeOrLen := min(MsgShareSize, len(rawData))
		rawShare := append(append(
			make([]byte, 0, len(nid)+1+len(rawData[:shareSizeOrLen])),
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
			case bytes.Compare(TxNamespaceID, nid) == 0:
				txsShares = append(txsShares, share)

			case bytes.Compare(IntermediateStateRootsNamespaceID, nid) == 0:
				isrShares = append(isrShares, share)

			case bytes.Compare(EvidenceNamespaceID, nid) == 0:
				evdShares = append(evdShares, share)

			case bytes.Compare(TailPaddingNamespaceID, nid) == 0:
				continue

			// every other namespaceID should be a message
			default:
				msgShares = append(msgShares, share)
			}
		}
	}

	// pass the raw share data to their respective parsers
	txs := parseTxs(txsShares)

	isrs := parseIsrs(isrShares)

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

func parseTxs(shares [][]byte) Txs {
	// parse the sharse
	rawTxs := parseShares(shares)

	// convert to the Tx type
	txs := make(Txs, len(rawTxs))
	for i := 0; i < len(txs); i++ {
		txs[i] = Tx(rawTxs[i])
	}

	return txs
}

func parseIsrs(shares [][]byte) IntermediateStateRoots {
	rawISRs := parseShares(shares)

	ISRs := make([]tmbytes.HexBytes, len(rawISRs))
	for i := 0; i < len(ISRs); i++ {
		ISRs[i] = rawISRs[i]
	}

	return IntermediateStateRoots{RawRootsList: ISRs}
}

// parseMsgs collects all messages from the shares provided
func parseEvd(shares [][]byte) (EvidenceData, error) {
	rawEvd := parseShares(shares)

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

// parseShares iterates through raw shares and separates the contiguous chunks
// of data. we use this for transactions, evidence, and intermediate state roots
func parseShares(shares [][]byte) [][]byte {
	currentShare := shares[0][NamespaceSize+ShareReservedBytes:]
	txLen := uint8(shares[0][NamespaceSize])
	var parsed [][]byte
	for cursor := 0; cursor < len(shares); {
		var p []byte

		currentShare, cursor, txLen, p = next(shares, currentShare, cursor, txLen)
		if p != nil {
			parsed = append(parsed, p)
		}
	}

	return parsed
}

// next returns the next chunk of a contiguous share. Used for parsing
// transaction, evidence, and intermediate state root block data.
func next(shares [][]byte, current []byte, cursor int, l uint8) ([]byte, int, uint8, []byte) {
	switch {
	// the rest of the shares should be tail padding
	case l == 0:

		cursor++
		if len(shares) != cursor {
			panic("contiguous share of length zero")
		}
		return nil, cursor, 0, nil

	// the tx is contained in the current share
	case int(l) < len(current):

		tx := append(make([]byte, 0, l), current[:l]...)

		// set the next txLen and update the next share
		txLen := current[l]

		// make sure that nothing panics if txLen is at the end of the share
		if len(current) < int(l)+ShareReservedBytes+1 {
			cursor++
			return shares[cursor][NamespaceSize:], cursor, txLen, tx
		}

		current := current[l+ShareReservedBytes:]
		// try printing current everytime instead

		return current, cursor, txLen, tx

	// the tx requires some portion of the following share
	case int(l) > len(current):

		cursor++

		// merge the current and the next share

		current := append(current, shares[cursor][NamespaceSize:]...)

		// try again using the next share
		return next(shares, current, cursor, l)

	// the tx is exactly the same size of the current share
	case int(l) == len(current):

		tx := make([]byte, l)
		copy(tx, current)

		cursor++

		// if this is the end of shares only return the tx
		if cursor == len(shares) {
			return []byte{}, cursor, 0, tx
		}

		// set the next txLen and next share
		next := shares[cursor][NamespaceSize+ShareReservedBytes:]
		nextTxLen := shares[cursor][NamespaceSize]

		return next, cursor, nextTxLen, tx
	}

	// this code is unreachable but the compiler doesn't know that
	return nil, 0, 0, nil
}

// parseMessages iterates through raw shares and separates the contiguous chunks
// of data. we use this for transactions, evidence, and intermediate state roots
func parseMsgShares(shares [][]byte) ([]Message, error) {
	// set the first nid and current share
	nid := shares[0][:NamespaceSize]
	currentShare := shares[0][NamespaceSize:]

	// find and remove the msg len delimiter
	currentShare, msgLen, err := tailorMsg(currentShare)
	if err != nil {
		return nil, err
	}

	var msgs []Message
	for cursor := 0; cursor < len(shares); {
		var msg Message
		currentShare, cursor, msgLen, msg, err = nextMsg(
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

// next returns the next chunk of a contiguous share. Used for parsing
// transaction, evidence, and intermediate state root block data.
func nextMsg(shares [][]byte, current, nid []byte, cursor int, l uint64) ([]byte, int, uint64, Message, error) {
	switch {
	// the rest of the share should be tail padding
	case l == 0:
		cursor++
		if len(shares) != cursor {
			panic("message of length zero")
		}
		return nil, cursor, 0, MessageEmpty, nil

	// the msg is contained in the current share
	case int(l) < len(current):
		msg := Message{NamespaceID: nid, Data: current[:l]}

		// set the next msgLen and update the next share
		next, msgLen, err := tailorMsg(current[l:])
		if err != nil {
			return nil, 0, 0, MessageEmpty, err
		}

		return next, cursor, msgLen, msg, nil

	// the msg requires some portion of the following share
	case int(l) > len(current):
		cursor++

		// merge the current and the next share
		current := append(current, shares[cursor][NamespaceSize:]...)

		// try again using the next share
		return nextMsg(shares, current, nid, cursor, l)

	// the msg is exactly the same size of the current share
	case l == uint64(len(current)):
		msg := Message{NamespaceID: nid, Data: current}

		cursor++

		// if this is the end of shares only return the msg
		if cursor == len(shares) {
			return []byte{}, cursor, 0, msg, nil
		}

		// set the next msgLen and next share
		next, nextMsgLen, err := tailorMsg(shares[cursor][NamespaceSize:])
		if err != nil {
			return nil, 0, 0, MessageEmpty, err
		}

		return next, cursor, nextMsgLen, msg, nil
	}

	// this code is unreachable but the compiler doesn't know that
	return nil, 0, 0, MessageEmpty, nil
}

// tailorMsg finds and returns the length delimiter of the message provided
// while also removing the delimiter bytes from the input
func tailorMsg(input []byte) ([]byte, uint64, error) {
	// read the length of the message
	r := bytes.NewBuffer(input[:binary.MaxVarintLen64])
	msgLen, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, 0, err
	}

	// calculate the number of bytes used by the delimiter
	lenBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(lenBuf, msgLen)

	// return the input without the length delimiter
	return input[n+1:], msgLen, nil
}
