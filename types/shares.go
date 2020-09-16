package types

import (
	"bytes"

	"github.com/lazyledger/nmt/namespace"
)

// TODO list:
// * pass in a way to extract / get the namespace ID of an element
// * data structures & algos:
//   - Iterator for each list in Block.Data (instead of passing in Data directly) ?
//   - define Share as pair (namespace, rawData)
//   - padding
//   - abstract away encoding (?)

// Share contains the raw share data without the corresponding namespace.
type Share []byte

var _ namespace.Data = NamespacedShare{}

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

type NamespacedShares []NamespacedShare

// Shares returns the raw shares that can be fed into the erasure coding
// library (e.g. rsmt2d).
func (ns NamespacedShares) Shares() []Share {
	res := make([]Share, len(ns))
	for i, nsh := range ns {
		res[i] = nsh.Share
	}
	return res
}

type Byter interface {
	Bytes() []byte
}

func MakeShares(data []Byter, shareSize int, nidFunc func(elem interface{}) namespace.ID) NamespacedShares {
	shares := make([]NamespacedShare, 0)
	for _, element := range data {
		rawData := element.Bytes() // data without nid
		nid := nidFunc(element)
		if len(rawData) < shareSize {
			rawShare := rawData
			paddedShare := zeroPadIfNecessary(rawShare, shareSize)
			share := NamespacedShare{paddedShare, nid}
			shares = append(shares, share)
		} else { // len(rawData) >= shareWithoutNidSize:
			firstRawShare := rawData[:shareSize-1]
			shares = append(shares, NamespacedShare{firstRawShare, nid})
			rawData := rawData[:shareSize-1]
			for len(rawData) > 0 {
				paddedShare := zeroPadIfNecessary(rawData[:shareSize-1], shareSize)
				share := NamespacedShare{paddedShare, nid}
				shares = append(shares, share)
			}
		}
	}
	return shares
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
