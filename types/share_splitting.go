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

// Write adds the delimited data to the underlying contiguous shares.
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

// WriteNamespacedPaddedShares adds empty shares using the namespace of the last written share.
// This is useful to follow the message layout rules. It assumes that at least
// one share has already been written, if not it panics.
func (msw *MessageShareWriter) WriteNamespacedPaddedShares(count int) {
	if len(msw.shares) == 0 {
		panic("Cannot write empty namespaced shares on an empty MessageShareWriter")
	}
	if count == 0 {
		return
	}
	lastMessage := msw.shares[len(msw.shares)-1]
	msw.shares = append(msw.shares, namespacedPaddedShares(lastMessage[0].ID, count))
	msw.count += count
}

// Export finalizes and returns the underlying contiguous shares.
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

// note: as an optimization we can probably get rid of this if we just add
// checks each time we write.
func (msw *MessageShareWriter) sortMsgs() {
	sort.SliceStable(msw.shares, func(i, j int) bool {
		return bytes.Compare(msw.shares[i][0].ID, msw.shares[j][0].ID) < 0
	})
}

// Count returns the current number of shares that will be made if exporting.
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

// ContiguousShareWriter will write raw data contiguously across a progressively
// increasing set of shares. It is used to lazily split block data such as transactions
// into shares.
type ContiguousShareWriter struct {
	shares       []NamespacedShare
	pendingShare NamespacedShare
	namespace    namespace.ID
}

// NewContiguousShareWriter returns a ContigousShareWriter using the provided
// namespace.
func NewContiguousShareWriter(ns namespace.ID) *ContiguousShareWriter {
	pendingShare := NamespacedShare{ID: ns, Share: make([]byte, 0, consts.ShareSize)}
	pendingShare.Share = append(pendingShare.Share, ns...)
	return &ContiguousShareWriter{pendingShare: pendingShare, namespace: ns}
}

// Write adds the delimited data to the underlying contiguous shares.
func (csw *ContiguousShareWriter) Write(rawData []byte) {
	// if this is the first time writing to a pending share, we must add the
	// reserved bytes
	if len(csw.pendingShare.Share) == consts.NamespaceSize {
		csw.pendingShare.Share = append(csw.pendingShare.Share, 0)
	}

	txCursor := len(rawData)
	for txCursor != 0 {
		// find the len left in the pending share
		pendingLeft := consts.ShareSize - len(csw.pendingShare.Share)

		// if we can simply add the tx to the share without creating a new
		// pending share, do so and return
		if len(rawData) <= pendingLeft {
			csw.pendingShare.Share = append(csw.pendingShare.Share, rawData...)
			break
		}

		// if we can only add a portion of the transaction to the pending share,
		// then we add it and add the pending share to the finalized shares.
		chunk := rawData[:pendingLeft]
		csw.pendingShare.Share = append(csw.pendingShare.Share, chunk...)
		csw.stackPending()

		// update the cursor
		rawData = rawData[pendingLeft:]
		txCursor = len(rawData)

		// add the share reserved bytes to the new pending share
		pendingCursor := len(rawData) + consts.NamespaceSize + consts.ShareReservedBytes
		var reservedByte byte
		if pendingCursor >= consts.ShareSize {
			// the share reserve byte is zero when some contiguously written
			// data takes up the entire share
			reservedByte = byte(0)
		} else {
			reservedByte = byte(pendingCursor)
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

// Export finalizes and returns the underlying contiguous shares.
func (csw *ContiguousShareWriter) Export() NamespacedShares {
	// add the pending share to the current shares before returning
	if len(csw.pendingShare.Share) > consts.NamespaceSize {
		csw.pendingShare.Share = zeroPadIfNecessary(csw.pendingShare.Share, consts.ShareSize)
		csw.shares = append(csw.shares, csw.pendingShare)
	}
	// force the last share to have a reserve byte of zero
	if len(csw.shares) == 0 {
		return csw.shares
	}
	lastShare := csw.shares[len(csw.shares)-1]
	rawLastShare := lastShare.Data()

	for i := 0; i < consts.ShareReservedBytes; i++ {
		// here we force the last share reserved byte to be zero to avoid any
		// confusion for light clients parsing these shares, as the rest of the
		// data after transaction is padding. See
		// https://github.com/celestiaorg/celestia-specs/blob/master/src/specs/data_structures.md#share
		rawLastShare[consts.NamespaceSize+i] = byte(0)
	}

	newLastShare := NamespacedShare{
		Share: rawLastShare,
		ID:    lastShare.NamespaceID(),
	}
	csw.shares[len(csw.shares)-1] = newLastShare
	return csw.shares
}

// Count returns the current number of shares that will be made if exporting.
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

func namespacedPaddedShares(ns []byte, count int) []NamespacedShare {
	shares := make([]NamespacedShare, count)
	for i := 0; i < count; i++ {
		shares[i] = NamespacedShare{
			Share: append(append(
				make([]byte, 0, consts.ShareSize), ns...),
				make([]byte, consts.MsgShareSize)...),
			ID: ns,
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
