package types

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/gogoproto/proto"
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
func MarshalBlockWithTxPositions(block proto.Message, txsCount int) ([]byte, []TxPosition, error) {
	if pb, ok := block.(*cmtproto.Block); ok && len(pb.Data.Txs) == txsCount && len(pb.Data.Txs) >= 16 && pb.Data.Size() >= 1<<20 && runtime.GOMAXPROCS(0) > 1 && pb.Size() <= math.MaxUint32 {
		return marshalLargeBlockByAlias(pb)
	}
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

	positions := make([]TxPosition, txsCount)
	var offset uint32 = 0
	txIndex := 0
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
				positions[txIndex] = TxPosition{Start: overallStart, End: overallEnd}
				txIndex++
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

// marshalLargeBlockByAlias copies large transaction payloads in parallel, then
// lets the generated marshaler write the canonical protobuf framing. Its tx
// sources already point at their final offsets, so the generated copies have
// identical source and destination slices.
func marshalLargeBlockByAlias(pb *cmtproto.Block) ([]byte, []TxPosition, error) {
	b := make([]byte, pb.Size())
	positions := make([]TxPosition, len(pb.Data.Txs))
	txAliases := make([][]byte, len(pb.Data.Txs))
	var varint [binary.MaxVarintLen64]byte
	pos := 1 + binary.PutUvarint(varint[:], uint64(pb.Header.Size())) + pb.Header.Size()
	pos += 1 + binary.PutUvarint(varint[:], uint64(pb.Data.Size()))
	for i, tx := range pb.Data.Txs {
		start := pos
		pos += 1 + binary.PutUvarint(varint[:], uint64(len(tx)))
		txAliases[i] = b[pos : pos+len(tx) : pos+len(tx)]
		pos += len(tx)
		positions[i] = TxPosition{Start: uint32(start), End: uint32(pos)}
	}

	jobs := make(chan int, len(pb.Data.Txs))
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(pb.Data.Txs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				copy(txAliases[i], pb.Data.Txs[i])
			}
		}()
	}
	for i := range pb.Data.Txs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	blockCopy := *pb
	blockCopy.Data.Txs = txAliases
	n, err := blockCopy.MarshalToSizedBuffer(b)
	if err != nil {
		return nil, nil, err
	}
	if n != len(b) {
		return nil, nil, fmt.Errorf("marshaled block length %d != expected %d", n, len(b))
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
