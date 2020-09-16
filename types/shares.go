package types

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/gogo/protobuf/proto"
	"github.com/lazyledger/lazyledger-core/libs/protoio"
	"github.com/lazyledger/nmt/namespace"
)

// TODO list:
// * pass in a way to extract / get the namespace ID of an element
// * data structures & algos:
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

type LenDelimitedMarshaler interface {
	MarshalDelimited() ([]byte,error)
}

// ProtoLenDelimitedMarshaler decorates any (gogo)proto.Message with a MarshalDelimited
// method.
type ProtoLenDelimitedMarshaler struct {
	proto.Message
}

func (p ProtoLenDelimitedMarshaler) MarshalDelimited() ([]byte, error) {
	return protoio.MarshalDelimited(p.Message)
}

// TxLenDelimitedMarshaler turns a Tx into a LenDelimitedMarshaler.
// It prefixes the Tx with its length as protoio.MarshalDelimited would do.
type TxLenDelimitedMarshaler Tx

func (tx TxLenDelimitedMarshaler)  MarshalDelimited() ([]byte, error) {
	lenBuf := make([]byte, binary.MaxVarintLen64)
	length := uint64(len(tx))
	n := binary.PutUvarint(lenBuf, length)

	return append(lenBuf[:n], tx...), nil
}

var _ LenDelimitedMarshaler = ProtoLenDelimitedMarshaler{}

// XXX: We could easily generalize this s.t. it does not use
// proto.Message in the method signature. But for now this
// does not seem necessary.
func MakeShares(data []LenDelimitedMarshaler, shareSize int, nidFunc func(elem interface{}) namespace.ID) NamespacedShares {
	shares := make([]NamespacedShare, 0)
	for _, element := range data {
		// TODO: implement a helper that also returns the size
		// to make it possible to (cleanly) implement:
		// https://github.com/lazyledger/lazyledger-specs/issues/69
		// For now, we do not squeeze multiple Tx into one share and
		// hence can ignore prefixing with the starting location of the
		// first start of a transaction in the share (aka SHARE_RESERVED_BYTES)
		rawData, err := element.MarshalDelimited()
		if err != nil {
			// The (abci) app has to guarantee that it only includes messages
			// that can be encoded without an error (equivalently tendermint
			// must not include any Tx, Evidence etc that is unencodable)
			panic(fmt.Sprintf("can not encode %v", element))
		}
		nid := nidFunc(element)
		if len(rawData) < shareSize {
			rawShare := rawData
			paddedShare := zeroPadIfNecessary(rawShare, shareSize)
			share := NamespacedShare{paddedShare, nid}
			shares = append(shares, share)
		} else { // len(rawData) >= shareWithoutNidSize:
			shares = append(shares, split(rawData, shareSize, nid)...)
		}
	}
	return shares
}

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

func min(a, b int) int {
	if a <= b { return a}
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
