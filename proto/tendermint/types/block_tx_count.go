package types

import (
	"errors"
	"io"
)

var errStopCount = errors.New("stop counting")

// CountBlockTxs counts Data.txs in an encoded Block, stopping at max+1. It parses
// like the generated Unmarshal, so it never fails on bytes Unmarshal accepts.
func CountBlockTxs(dAtA []byte, max int) (int, error) {
	n := 0
	err := forEachBytesField(dAtA, 2, func(data []byte) error { // Block.data
		return forEachBytesField(data, 1, func([]byte) error { // Data.txs
			if n++; n > max {
				return errStopCount
			}
			return nil
		})
	})
	if err == errStopCount {
		err = nil
	}
	return n, err
}

// forEachBytesField calls visit with the payload of every occurrence of field
// num. Other fields are skipped by the generated skipper.
func forEachBytesField(dAtA []byte, num int32, visit func([]byte) error) error {
	l := len(dAtA)
	for iNdEx := 0; iNdEx < l; {
		preIndex := iNdEx
		wire, err := decodeCountVarint(dAtA, &iNdEx)
		if err != nil {
			return err
		}
		if int32(wire>>3) != num || wire&0x7 != 2 {
			skippy, err := skipTypes(dAtA[preIndex:])
			if err != nil {
				return err
			}
			if skippy < 0 || preIndex+skippy < 0 {
				return ErrInvalidLengthTypes
			}
			if preIndex+skippy > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx = preIndex + skippy
			continue
		}
		length, err := decodeCountVarint(dAtA, &iNdEx)
		if err != nil {
			return err
		}
		postIndex := iNdEx + int(length)
		if int(length) < 0 || postIndex < 0 {
			return ErrInvalidLengthTypes
		}
		if postIndex > l {
			return io.ErrUnexpectedEOF
		}
		if err := visit(dAtA[iNdEx:postIndex]); err != nil {
			return err
		}
		iNdEx = postIndex
	}
	return nil
}

// decodeCountVarint is the generated decoder's varint loop.
func decodeCountVarint(dAtA []byte, iNdEx *int) (uint64, error) {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		if shift >= 64 {
			return 0, ErrIntOverflowTypes
		}
		if *iNdEx >= len(dAtA) {
			return 0, io.ErrUnexpectedEOF
		}
		b := dAtA[*iNdEx]
		*iNdEx++
		v |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return v, nil
		}
	}
}
