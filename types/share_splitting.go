package types

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/celestiaorg/nmt/namespace"
	"github.com/tendermint/tendermint/pkg/consts"
)

// MessageShareWriter lazily merges messages into shares that will eventually be
// included in a data square. It also has methods to help progressively count
// how many shares the messages written take up.
type MessageShareWriter struct {
	shares [][]NamespacedShare
	count  int
}

func NewMessageShareWriter() *MessageShareWriter {
	return &MessageShareWriter{}
}

// Write adds the delimited data to the underlying contiguous shares
func (msw *MessageShareWriter) Write(msg Message) {
	rawMsg, err := msg.MarshalDelimited()
	if err != nil {
		panic(fmt.Sprintf("app accepted a Message that can not be encoded %#v", msg))
	}
	newShares := make([]NamespacedShare, 0)
	newShares = AppendToShares(newShares, msg.NamespaceID, rawMsg)
	msw.shares = append(msw.shares, newShares)
	msw.count += len(newShares)
}

// Export finalizes and returns the underlying contiguous shares
func (msw *MessageShareWriter) Export() NamespacedShares {
	msw.sortMsgs()
	shares := make([]NamespacedShare, msw.count)
	cursor := 0
	for _, messageShares := range msw.shares {
		for _, share := range messageShares {
			shares[cursor] = share
			cursor++
		}
	}
	return shares
}

func (msw *MessageShareWriter) sortMsgs() {
	sort.Slice(msw.shares, func(i, j int) bool {
		return bytes.Compare(msw.shares[i][0].ID, msw.shares[j][0].ID) < 0
	})
}

// Count returns the current number of shares that will be made if exporting
func (msw *MessageShareWriter) Count() int {
	return msw.count
}

// appendToShares appends raw data as shares.
// Used for messages.
func AppendToShares(shares []NamespacedShare, nid namespace.ID, rawData []byte) []NamespacedShare {
	if len(rawData) <= consts.MsgShareSize {
		rawShare := append(append(
			make([]byte, 0, len(nid)+len(rawData)),
			nid...),
			rawData...,
		)
		paddedShare := zeroPadIfNecessary(rawShare, consts.ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
	} else { // len(rawData) > MsgShareSize
		shares = append(shares, splitMessage(rawData, nid)...)
	}
	return shares
}

// splitMessage breaks the data in a message into the minimum number of
// namespaced shares
func splitMessage(rawData []byte, nid namespace.ID) NamespacedShares {
	shares := make([]NamespacedShare, 0)
	firstRawShare := append(append(
		make([]byte, 0, consts.ShareSize),
		nid...),
		rawData[:consts.MsgShareSize]...,
	)
	shares = append(shares, NamespacedShare{firstRawShare, nid})
	rawData = rawData[consts.MsgShareSize:]
	for len(rawData) > 0 {
		shareSizeOrLen := min(consts.MsgShareSize, len(rawData))
		rawShare := append(append(
			make([]byte, 0, consts.ShareSize),
			nid...),
			rawData[:shareSizeOrLen]...,
		)
		paddedShare := zeroPadIfNecessary(rawShare, consts.ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
		rawData = rawData[shareSizeOrLen:]
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
		rawData, outerIndex, innerIndex, startIndex = getNextChunk(rawDatas, outerIndex, innerIndex, consts.TxShareSize)
		rawShare := append(append(append(
			make([]byte, 0, len(nid)+1+len(rawData)),
			nid...),
			byte(startIndex)),
			rawData...)
		paddedShare := zeroPadIfNecessary(rawShare, consts.ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
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

		csw.pendingShare.Share = append(csw.pendingShare.Share, reservedByte)
	}

	// if the share is exactly the correct size, then append to shares
	if len(csw.pendingShare.Share) == consts.ShareSize {
		csw.stackPending()
	}
}

// stackPending will add the pending share to accumlated shares provided that it is long enough
func (csw *ContiguousShareWriter) stackPending() {
	if len(csw.pendingShare.Share) < consts.ShareSize {
		return
	}
	csw.shares = append(csw.shares, csw.pendingShare)
	newPendingShare := make([]byte, 0, consts.ShareSize)
	newPendingShare = append(newPendingShare, csw.namespace...)
	csw.pendingShare = NamespacedShare{
		Share: newPendingShare,
		ID:    csw.namespace,
	}
}

// Export finalizes and returns the underlying contiguous shares
func (csw *ContiguousShareWriter) Export() NamespacedShares {
	// add the pending share to the current shares before returning
	if len(csw.pendingShare.Share) > consts.NamespaceSize {
		csw.pendingShare.Share = zeroPadIfNecessary(csw.pendingShare.Share, consts.ShareSize)
		csw.shares = append(csw.shares, csw.pendingShare)
	}

// Count returns the current number of shares that will be made if exporting
func (csw *ContiguousShareWriter) Count() (count, availableBytes int) {
	availableBytes = consts.TxShareSize - (len(csw.pendingShare.Share) - consts.NamespaceSize)
	return len(csw.shares), availableBytes
}

// tail is filler for all tail padded shares
// it is allocated once and used everywhere
var tailPaddingShare = append(
	append(make([]byte, 0, consts.ShareSize), consts.TailPaddingNamespaceID...),
	bytes.Repeat([]byte{0}, consts.ShareSize-consts.NamespaceSize)...,
)

func TailPaddingShares(n int) NamespacedShares {
	shares := make([]NamespacedShare, n)
	for i := 0; i < n; i++ {
		shares[i] = NamespacedShare{
			Share: tailPaddingShare,
			ID:    consts.TailPaddingNamespaceID,
		}
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
