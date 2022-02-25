package types

import (
	"bytes"
	"fmt"

	"github.com/celestiaorg/nmt/namespace"
	"github.com/tendermint/tendermint/pkg/consts"
)

type MessageShareWriter struct {
	shares []NamespacedShare
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
	msw.shares = AppendToShares(msw.shares, msg.NamespaceID, rawMsg)
}

// Export finalizes and returns the underlying contiguous shares
func (msw *MessageShareWriter) Export() NamespacedShares {
	return msw.shares
}

// Count returns the current number of shares that will be made if exporting
func (msw *MessageShareWriter) Count() int {
	return len(msw.shares)
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

type ContiguousShareWriter struct {
	shares       []NamespacedShare
	pendingShare NamespacedShare
	namespace    namespace.ID
}

func NewContiguousShareWriter(ns namespace.ID) *ContiguousShareWriter {
	pendingShare := NamespacedShare{ID: ns}
	pendingShare.Share = append(pendingShare.Share, ns...)
	return &ContiguousShareWriter{pendingShare: pendingShare, namespace: ns}
}

// Write adds the delimited data to the underlying contiguous shares
func (csw *ContiguousShareWriter) Write(rawData []byte) {
	// if this is the first time writeing to a pending share, we must add the
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

// Export finalizes and returns the underlying contiguous shares
func (csw *ContiguousShareWriter) Export() NamespacedShares {
	// add the pending share to the current shares before returning
	if len(csw.pendingShare.Share) > consts.NamespaceSize {
		csw.pendingShare.Share = zeroPadIfNecessary(csw.pendingShare.Share, consts.ShareSize)
		csw.shares = append(csw.shares, csw.pendingShare)
	}
	return csw.shares
}

// Count returns the current number of shares that will be made if exporting
func (csw *ContiguousShareWriter) Count() int {
	c := len(csw.shares)
	// count the pending share if it has anything in it
	if len(csw.pendingShare.Share) > consts.NamespaceSize {
		c++
	}
	return c
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
