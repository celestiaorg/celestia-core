package types

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gogo/protobuf/proto"
)

// TxPosition holds the start and end indexes (in the overall encoded []byte)
// for a given txs field.
type TxPosition struct {
	Start int
	// End exclusive position of the transaction
	End int
}

// MarshalBlockWithTxPositions marshals the given Block message using protobuf
// (so that the final encoding is identical to what proto.Marshal produces)
// and returns both the encoded []byte and a slice of positions marking the
// boundaries of each nested tx (repeated []byte field) inside Data (field number 1).
func MarshalBlockWithTxPositions(block proto.Message) ([]byte, []TxPosition, error) {
	// First, marshal the entire message normally.
	b, err := proto.Marshal(block)
	if err != nil {
		return nil, nil, err
	}

	// In our Block proto, field number 2 is the Data message.
	// We need to find it (it’s encoded as a length-delimited field).
	dataContentOffset, _, dataContent, err := findField(b, 2)
	if err != nil {
		return b, nil, err
	}

	// Now, parse the encoded Data message to locate each repeated "txs" field.
	// In Data, txs is field number 1 and is a length-delimited field.
	var positions []TxPosition
	offset := 0
	for offset < len(dataContent) {
		// Read the field tag (a varint).
		tag, n, err := readVarint(dataContent[offset:])
		if err != nil {
			return b, nil, err
		}
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)
		fieldStartInData := offset // marks start of this field (tag start)
		offset += n

		if wireType == 2 { // length-delimited
			// Read the length of the field’s value.
			length, n, err := readVarint(dataContent[offset:])
			if err != nil {
				return b, nil, err
			}
			offset += n
			// value starts here
			offset += int(length)
			fieldEndInData := offset

			if fieldNum == 1 {
				// Record positions relative to the entire Block encoding.
				overallStart := dataContentOffset + fieldStartInData
				overallEnd := dataContentOffset + fieldEndInData
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
			offset += n
		case 1: // 64-bit
			offset += 8
		case 5: // 32-bit
			offset += 4
		default:
			return b, nil, fmt.Errorf("unsupported wire type %d", wireType)
		}
	}

	return b, positions, nil
}

// findField scans the encoded message b for a length-delimited field with the given targetField number.
// It returns the offset (within b) where the field’s content begins and ends, as well as the raw content bytes.
func findField(b []byte, targetField int) (contentStart int, contentEnd int, content []byte, err error) {
	offset := 0
	for offset < len(b) {
		// Read the key (a varint).
		tag, n, err := readVarint(b[offset:])
		if err != nil {
			return 0, 0, nil, err
		}
		offset += n
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		if wireType == 2 { // length-delimited
			// Read the length of the field.
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
			// Skip other wire types.
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
// It returns the decoded value, the number of bytes consumed, or an error.
func readVarint(b []byte) (uint64, int, error) {
	value, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, 0, errors.New("failed to read varint")
	}
	return value, n, nil
}
