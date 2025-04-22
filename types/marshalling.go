package types

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/gogo/protobuf/proto"
)

// TxPosition holds the start and end indexes (in the overall encoded []byte)
// for a given txs field.
type TxPosition struct {
	Start uint32
	// End exclusive position of the transaction
	End uint32
}

// safeAddUint32 performs checked addition of two uint32 numbers.
func safeAddUint32(a, b uint32) (uint32, error) {
	if a > math.MaxUint32-b {
		return 0, fmt.Errorf("integer overflow: %d + %d", a, b)
	}
	return a + b, nil
}

// MarshalBlockWithTxPositions marshals the given Block message using protobuf
// and returns both the encoded []byte and a slice of positions marking the
// boundaries of each nested tx (repeated []byte field) inside Data (field number 1).
func MarshalBlockWithTxPositions(block proto.Message) ([]byte, []TxPosition, error) {
	// First, marshal the entire message normally.
	b, err := proto.Marshal(block)
	if err != nil {
		return nil, nil, err
	}

	// In our Block proto, field number 2 is the Data message.
	dataContentOffset, _, dataContent, err := findField(b, 2)
	if err != nil {
		return b, nil, err
	}

	var positions []TxPosition
	var offset uint32 = 0
	for offset < uint32(len(dataContent)) {
		// Read the field tag (a varint).
		tag, n, err := readVarint(dataContent[offset:])
		if err != nil {
			return b, nil, err
		}
		fieldStartInData := offset
		offset, err = safeAddUint32(offset, uint32(n))
		if err != nil {
			return b, nil, err
		}

		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		if wireType == 2 { // length-delimited
			length, n, err := readVarint(dataContent[offset:])
			if err != nil {
				return b, nil, err
			}
			offset, err = safeAddUint32(offset, uint32(n))
			if err != nil {
				return b, nil, err
			}
			newOffset, err := safeAddUint32(offset, uint32(length))
			if err != nil {
				return b, nil, err
			}
			fieldEndInData := newOffset
			offset = newOffset

			if fieldNum == 1 {
				overallStart, err := safeAddUint32(uint32(dataContentOffset), fieldStartInData)
				if err != nil {
					return b, nil, err
				}
				overallEnd, err := safeAddUint32(uint32(dataContentOffset), fieldEndInData)
				if err != nil {
					return b, nil, err
				}
				positions = append(positions, TxPosition{Start: overallStart, End: overallEnd})
			}
			continue
		}

		// For non length-delimited fields, skip appropriately.
		switch wireType {
		case 0: // varint
			_, n, err := readVarint(dataContent[offset:])
			if err != nil {
				return b, nil, err
			}
			offset, err = safeAddUint32(offset, uint32(n))
			if err != nil {
				return b, nil, err
			}
		case 1: // 64-bit
			offset, err = safeAddUint32(offset, 8)
			if err != nil {
				return b, nil, err
			}
		case 5: // 32-bit
			offset, err = safeAddUint32(offset, 4)
			if err != nil {
				return b, nil, err
			}
		default:
			return b, nil, fmt.Errorf("unsupported wire type %d", wireType)
		}
	}

	return b, positions, nil
}

// findField scans the encoded message b for a length-delimited field with the given targetField number.
func findField(b []byte, targetField int) (contentStart int, contentEnd int, content []byte, err error) {
	offset := 0
	for offset < len(b) {
		tag, n, err := readVarint(b[offset:])
		if err != nil {
			return 0, 0, nil, err
		}
		offset += n
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		if wireType == 2 {
			length, n, err := readVarint(b[offset:])
			if err != nil {
				return 0, 0, nil, err
			}
			offset += n
			start := offset
			end := offset + int(length)
			if fieldNum == targetField {
				return start, end, b[start:end], nil
			}
			offset = end
		} else {
			switch wireType {
			case 0:
				_, n, err := readVarint(b[offset:])
				if err != nil {
					return 0, 0, nil, err
				}
				offset += n
			case 1:
				offset += 8
			case 5:
				offset += 4
			default:
				return 0, 0, nil, fmt.Errorf("unsupported wire type %d", wireType)
			}
		}
	}
	return 0, 0, nil, errors.New("field not found")
}

// readVarint reads a varint-encoded unsigned integer from b.
func readVarint(b []byte) (uint64, int, error) {
	value, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, 0, errors.New("failed to read varint")
	}
	return value, n, nil
}
