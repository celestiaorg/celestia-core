package types

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrTooManyBlockTxs indicates that a block exceeds its transaction-count limit.
var ErrTooManyBlockTxs = errors.New("block exceeds maximum transaction count")

// UnmarshalWithTxLimit checks the encoded transaction count before unmarshalling
// any repeated fields. Unlike Unmarshal, it is intended for live proposals with
// a limit selected from trusted consensus state, not historical block decoding.
// Repeated Data fields merge, so the limit covers their combined count. On a
// count error the receiver is unchanged and no transaction slices are allocated.
func (m *Block) UnmarshalWithTxLimit(bz []byte, maxTxs int) error {
	remaining := maxTxs - len(m.Data.Txs)
	if maxTxs < 0 || remaining < 0 {
		return ErrTooManyBlockTxs
	}
	for rest := bz; len(rest) > 0; {
		field, payload, next, err := blockLimitField(rest, 2)
		if err != nil {
			return err
		}
		if field == 2 {
			for len(payload) > 0 {
				txField, _, tail, err := blockLimitField(payload, 1)
				if err != nil {
					return err
				}
				if txField == 1 {
					if remaining == 0 {
						return ErrTooManyBlockTxs
					}
					remaining--
				}
				payload = tail
			}
		}
		rest = next
	}
	return m.Unmarshal(bz)
}

// blockLimitField consumes a field without copying its bytes. The selected
// field must be length-delimited. Unknown fields use the generated iterative
// skipper, which handles groups without recursion and matches Unmarshal.
func blockLimitField(bz []byte, selected int32) (int32, []byte, []byte, error) {
	tag, n := binary.Uvarint(bz)
	if n == 0 {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	if n < 0 {
		return 0, nil, nil, ErrIntOverflowBlock
	}
	field := int32(tag >> 3)
	if field <= 0 {
		return 0, nil, nil, fmt.Errorf("proto: illegal tag %d", field)
	}
	if field == selected {
		if tag&7 != 2 {
			return 0, nil, nil, fmt.Errorf("proto: wrong wire type %d for field %d", tag&7, field)
		}
		length, size := binary.Uvarint(bz[n:])
		if size == 0 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		if size < 0 {
			return 0, nil, nil, ErrIntOverflowBlock
		}
		start := n + size
		if length > uint64(len(bz)-start) {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		end := start + int(length)
		return field, bz[start:end], bz[end:], nil
	}
	size, err := skipBlock(bz)
	if err != nil {
		return 0, nil, nil, err
	}
	if size < 0 || size > len(bz) {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	return field, nil, bz[size:], nil
}
