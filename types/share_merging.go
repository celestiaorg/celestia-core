package types

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/celestiaorg/rsmt2d"
	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/pkg/consts"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

// DataFromSquare extracts block data from an extended data square.
func DataFromSquare(eds *rsmt2d.ExtendedDataSquare) (Data, error) {
	originalWidth := eds.Width() / 2

	// sort block data shares by namespace
	var (
		sortedTxShares  [][]byte
		sortedEvdShares [][]byte
		sortedMsgShares [][]byte
	)

	// iterate over each row index
	for x := uint(0); x < originalWidth; x++ {
		// iterate over each share in the original data square
		row := eds.Row(x)

		for _, share := range row[:originalWidth] {
			// sort the data of that share types via namespace
			nid := share[:consts.NamespaceSize]
			switch {
			case bytes.Equal(consts.TxNamespaceID, nid):
				sortedTxShares = append(sortedTxShares, share)

			case bytes.Equal(consts.EvidenceNamespaceID, nid):
				sortedEvdShares = append(sortedEvdShares, share)

			case bytes.Equal(consts.TailPaddingNamespaceID, nid):
				continue

			// ignore unused but reserved namespaces
			case bytes.Compare(nid, consts.MaxReservedNamespace) < 1:
				continue

			// every other namespaceID should be a message
			default:
				sortedMsgShares = append(sortedMsgShares, share)
			}
		}
	}

	// pass the raw share data to their respective parsers
	txs, err := ParseTxs(sortedTxShares)
	if err != nil {
		return Data{}, err
	}

	evd, err := ParseEvd(sortedEvdShares)
	if err != nil {
		return Data{}, err
	}

	msgs, err := ParseMsgs(sortedMsgShares)
	if err != nil {
		return Data{}, err
	}

	return Data{
		Txs:      txs,
		Evidence: evd,
		Messages: msgs,
	}, nil
}

// ParseTxs collects all of the transactions from the shares provided
func ParseTxs(shares [][]byte) (Txs, error) {
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

// ParseEvd collects all evidence from the shares provided.
func ParseEvd(shares [][]byte) (EvidenceData, error) {
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
		var protoEvd tmproto.Evidence
		err := proto.Unmarshal(rawEvd[i], &protoEvd)
		if err != nil {
			return EvidenceData{}, err
		}
		evd, err := EvidenceFromProto(&protoEvd)
		if err != nil {
			return EvidenceData{}, err
		}

		evdList[i] = evd
	}

	return EvidenceData{Evidence: evdList}, nil
}

// ParseMsgs collects all messages from the shares provided
func ParseMsgs(shares [][]byte) (Messages, error) {
	msgList, err := parseMsgShares(shares)
	if err != nil {
		return Messages{}, err
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
		return nil, nil
	}

	ss := newShareStack(shares)
	return ss.resolve()
}

// shareStack hold variables for peel
type shareStack struct {
	shares [][]byte
	txLen  uint64
	txs    [][]byte
	cursor int
}

func newShareStack(shares [][]byte) *shareStack {
	return &shareStack{shares: shares}
}

func (ss *shareStack) resolve() ([][]byte, error) {
	if len(ss.shares) == 0 {
		return nil, nil
	}
	err := ss.peel(ss.shares[0][consts.NamespaceSize+consts.ShareReservedBytes:], true)
	return ss.txs, err
}

// peel recursively parses each chunk of data (either a transaction,
// intermediate state root, or evidence) and adds it to the underlying slice of data.
func (ss *shareStack) peel(share []byte, delimited bool) (err error) {
	if delimited {
		var txLen uint64
		share, txLen, err = ParseDelimiter(share)
		if err != nil {
			return err
		}
		if txLen == 0 {
			return nil
		}
		ss.txLen = txLen
	}
	// safeLen describes the point in the share where it can be safely split. If
	// split beyond this point, it is possible to break apart a length
	// delimiter, which will result in incorrect share merging
	safeLen := len(share) - binary.MaxVarintLen64
	if safeLen < 0 {
		safeLen = 0
	}
	if ss.txLen <= uint64(safeLen) {
		ss.txs = append(ss.txs, share[:ss.txLen])
		share = share[ss.txLen:]
		return ss.peel(share, true)
	}
	// add the next share to the current share to continue merging if possible
	if len(ss.shares) > ss.cursor+1 {
		ss.cursor++
		share := append(share, ss.shares[ss.cursor][consts.NamespaceSize+consts.ShareReservedBytes:]...)
		return ss.peel(share, false)
	}
	// collect any remaining data
	if ss.txLen <= uint64(len(share)) {
		ss.txs = append(ss.txs, share[:ss.txLen])
		share = share[ss.txLen:]
		return ss.peel(share, true)
	}
	return errors.New("failure to parse block data: transaction length exceeded data length")
}

// parseMsgShares iterates through raw shares and separates the contiguous chunks
// of data. It is only used for Messages, i.e. shares with a non-reserved namespace.
func parseMsgShares(shares [][]byte) ([]Message, error) {
	if len(shares) == 0 {
		return nil, nil
	}
	// msgs returned
	msgs := []Message{}
	// current msg len
	msgLen := 0
	// current msg
	currentMsg := Message{}
	// the current share contains the start of a new message
	newMessage := true
	// the len in bytes of the current chunk of data that will eventually become
	// a message. This is identical to len(currentMsg.Data) + consts.MsgShareSize
	// but we cache it here for readability
	dataLen := 0

	saveLatestMessage := func() {
		msgs = append(msgs, currentMsg)
		dataLen = 0
		newMessage = true
	}
	// iterate through all the shares and parse out each msg
	for i := 0; i < len(shares); i++ {
		dataLen = len(currentMsg.Data) + consts.MsgShareSize
		switch {
		case newMessage:
			nextMsgChunk, nextMsgLen, err := ParseDelimiter(shares[i][consts.NamespaceSize:])
			if err != nil {
				return nil, err
			}
			// the current share is namespaced padding so we ignore it
			if nextMsgLen == 0 {
				continue
			}
			msgLen = int(nextMsgLen)
			nid := shares[i][:consts.NamespaceSize]
			currentMsg = Message{
				NamespaceID: nid,
				Data:        nextMsgChunk,
			}
			// the current share contains the entire msg so we save it and
			// progress
			if msgLen <= len(nextMsgChunk) {
				currentMsg.Data = currentMsg.Data[:msgLen]
				saveLatestMessage()
				continue
			}
			newMessage = false
		// this entire share contains a chunk of message that we need to save
		case msgLen > dataLen:
			currentMsg.Data = append(currentMsg.Data, shares[i][consts.NamespaceSize:]...)
		// this share contains the last chunk of data needed to complete the
		// message
		case msgLen <= dataLen:
			remaining := msgLen - len(currentMsg.Data) + consts.NamespaceSize
			currentMsg.Data = append(currentMsg.Data, shares[i][consts.NamespaceSize:remaining]...)
			saveLatestMessage()
		}
	}
	return msgs, nil
}

// ParseDelimiter finds and returns the length delimiter of the message provided
// while also removing the delimiter bytes from the input
func ParseDelimiter(input []byte) ([]byte, uint64, error) {
	if len(input) == 0 {
		return input, 0, nil
	}

	l := binary.MaxVarintLen64
	if len(input) < binary.MaxVarintLen64 {
		l = len(input)
	}

	delimiter := zeroPadIfNecessary(input[:l], binary.MaxVarintLen64)

	// read the length of the message
	r := bytes.NewBuffer(delimiter)
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

func parseMsgDelim(input []byte) ([]byte, uint64, error) {
	if len(input) == 0 {
		return input, 0, nil
	}

	l := binary.MaxVarintLen64
	if len(input) < binary.MaxVarintLen64 {
		l = len(input)
	}

	// delimiter := zeroPadIfNecessary(input[:l], binary.MaxVarintLen64)

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
