package types

import (
	"bytes"
	"encoding/binary"

	"github.com/lazyledger/nmt/namespace"
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

// appendToSharesContiguous appends one raw data separately as shares
// Used for messages
func appendToShares(shares []NamespacedShare, nid namespace.ID, rawData []byte) []NamespacedShare {
	const adjustedSize = ShareSize - NamespaceSize
	if len(rawData) < adjustedSize {
		rawShare := []byte(append(nid, rawData...))
		paddedShare := zeroPadIfNecessary(rawShare, ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
	} else { // len(rawData) >= adjustedSize
		shares = append(shares, split(rawData, nid)...)
	}
	return shares
}

// appendToSharesContiguous appends multiple raw data contiguously as shares
// Used for transactions, intermediate state roots, and evidence
func appendToSharesContiguous(shares []NamespacedShare, nid namespace.ID, rawDatas [][]byte) []NamespacedShare {
	const adjustedSize = ShareSize - NamespaceSize - ShareReservedBytes
	// Index into the outer slice of rawDatas
	outerIndex := 0
	// Index into the inner slice of rawDatas
	innerIndex := 0
	for outerIndex < len(rawDatas) {
		var rawData []byte
		startIndex := 0
		rawData, outerIndex, innerIndex, startIndex = getNextChunk(rawDatas, outerIndex, innerIndex, adjustedSize)
		rawShare := []byte(append(append(nid, byte(startIndex)), rawData...))
		paddedShare := zeroPadIfNecessary(rawShare, ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
	}
	return shares
}

// TODO(ismail): implement corresponding merge method for clients requesting
// shares for a particular namespace
func split(rawData []byte, nid namespace.ID) []NamespacedShare {
	const adjustedSize = ShareSize - NamespaceSize
	shares := make([]NamespacedShare, 0)
	firstRawShare := []byte(append(nid, rawData[:adjustedSize]...))
	shares = append(shares, NamespacedShare{firstRawShare, nid})
	rawData = rawData[adjustedSize:]
	for len(rawData) > 0 {
		shareSizeOrLen := min(adjustedSize, len(rawData))
		rawShare := []byte(append(nid, rawData[:shareSizeOrLen]...))
		paddedShare := zeroPadIfNecessary(rawShare, ShareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
		rawData = rawData[shareSizeOrLen:]
	}
	return shares
}

func getNextChunk(rawDatas [][]byte, outerIndex int, innerIndex int, width int) ([]byte, int, int, int) {
	rawData := make([]byte, 0, width)
	startIndex := len(rawDatas[outerIndex]) - innerIndex - 1
	// If the start index would go past the end of the share, no transaction begins in this share
	if startIndex >= width {
		startIndex = 0
	}
	// Offset by the fixed reserved bytes at the beginning of the share
	if startIndex > 0 {
		startIndex += NamespaceSize + ShareReservedBytes
	}

	curIndex := 0
	for curIndex < width && outerIndex < len(rawDatas) {
		bytesToFetch := min(len(rawDatas[outerIndex])-innerIndex-1, width-curIndex-1)
		if bytesToFetch == 0 {
			innerIndex = 0
			outerIndex++
			continue
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
