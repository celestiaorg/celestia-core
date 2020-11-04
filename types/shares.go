package types

import (
	"bytes"
	"encoding/binary"

	"github.com/lazyledger/nmt/namespace"
)

var _ namespace.Data = NamespacedShare{}

// Share contains the raw share data without the corresponding namespace.
type Share []byte

// NamespacedShare extends a Share with the corresponding namespace.
// It implements the namespace.Data interface and hence can be used
// for pushing the shares to the namespaced Merkle tree.
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

func appendToShares(shares []NamespacedShare, nid namespace.ID, rawData []byte, shareSize int) []NamespacedShare {
	if len(rawData) < shareSize {
		rawShare := rawData
		paddedShare := zeroPadIfNecessary(rawShare, shareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
	} else { // len(rawData) >= shareSize
		shares = append(shares, split(rawData, shareSize, nid)...)
	}
	return shares
}

// TODO(ismail): implement corresponding merge method for clients requesting
// shares for a particular namespace
func split(rawData []byte, shareSize int, nid namespace.ID) []NamespacedShare {
	shares := make([]NamespacedShare, 0)
	firstRawShare := rawData[:shareSize]
	shares = append(shares, NamespacedShare{firstRawShare, nid})
	rawData = rawData[shareSize:]
	for len(rawData) > 0 {
		shareSizeOrLen := min(shareSize, len(rawData))
		paddedShare := zeroPadIfNecessary(rawData[:shareSizeOrLen], shareSize)
		share := NamespacedShare{paddedShare, nid}
		shares = append(shares, share)
		rawData = rawData[shareSizeOrLen:]
	}
	return shares
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
