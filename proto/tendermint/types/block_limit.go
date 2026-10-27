package types

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrTooManyBlockTxs indicates that a block exceeds its transaction-count limit.
var ErrTooManyBlockTxs = errors.New("block exceeds maximum transaction count")

var (
	ErrTooManyBlockCommitSignatures = errors.New("block exceeds maximum commit signatures")
	ErrTooManyBlockEvidence         = errors.New("block exceeds maximum evidence items")
	ErrTooManyBlockEvidenceElements = errors.New("block exceeds maximum nested evidence elements")
)

const (
	// Celestia fixes evidence MaxBytes at 1 MiB and does not permit governance
	// to change it. Every valid evidence item contains at least one 64-byte
	// signature, and every valid nested commit/validator entry contains at least
	// 32 bytes of cryptographic material. The extra item makes these ceilings
	// strictly greater than the capacity of any valid evidence payload.
	MaxBlockEvidenceItems    = 1_048_576/64 + 1
	MaxBlockEvidenceElements = 1_048_576/32 + 1
	MaxBlockCommitSignatures = 10_000
)

// UnmarshalWithTxLimit checks the encoded transaction count before unmarshalling
// any repeated fields. Unlike Unmarshal, it is intended for live proposals with
// a limit selected from trusted consensus state, not historical block decoding.
// Repeated Data fields merge, so the limit covers their combined count. On a
// count error the receiver is unchanged and no transaction slices are allocated.
func (m *Block) UnmarshalWithTxLimit(bz []byte, maxTxs int) error {
	if maxTxs < 0 || len(m.Data.Txs) > maxTxs {
		return ErrTooManyBlockTxs
	}
	if err := ValidateBlockCardinality(bz, maxTxs-len(m.Data.Txs)); err != nil {
		return err
	}
	return m.Unmarshal(bz)
}

// ValidateBlockCardinality scans allocation-sensitive repeated fields without
// constructing their elements. A negative maxTxs skips the transaction check.
func ValidateBlockCardinality(bz []byte, maxTxs int) error {
	return NewBlockCardinalityScanner(maxTxs).Scan(bz)
}

// BlockCardinalityScanner aggregates counts across protobuf occurrences of a
// singular Block field, which generated decoders merge into one Block.
type BlockCardinalityScanner struct {
	maxTxs int

	txs, commitSigs, evidenceItems                      int
	byzantineValidators, evidenceCommitSigs, validators int
}

// NewBlockCardinalityScanner creates a scanner with a shared transaction limit.
func NewBlockCardinalityScanner(maxTxs int) *BlockCardinalityScanner {
	return &BlockCardinalityScanner{maxTxs: maxTxs}
}

func (s *BlockCardinalityScanner) Scan(bz []byte) error {
	checks := []struct {
		path  []int32
		count *int
		limit int
		err   error
	}{
		{[]int32{4, 4}, &s.commitSigs, MaxBlockCommitSignatures, ErrTooManyBlockCommitSignatures},
		{[]int32{3, 1}, &s.evidenceItems, MaxBlockEvidenceItems, ErrTooManyBlockEvidence},
		{[]int32{3, 1, 2, 3}, &s.byzantineValidators, MaxBlockEvidenceElements, ErrTooManyBlockEvidenceElements},
		{[]int32{3, 1, 2, 1, 1, 2, 4}, &s.evidenceCommitSigs, MaxBlockEvidenceElements, ErrTooManyBlockEvidenceElements},
		{[]int32{3, 1, 2, 1, 2, 1}, &s.validators, MaxBlockEvidenceElements, ErrTooManyBlockEvidenceElements},
	}
	if s.maxTxs >= 0 {
		checks = append(checks, struct {
			path  []int32
			count *int
			limit int
			err   error
		}{[]int32{2, 1}, &s.txs, s.maxTxs, ErrTooManyBlockTxs})
	}
	for _, check := range checks {
		if err := validateBlockPathCount(bz, check.path, check.count, check.limit, check.err); err != nil {
			return err
		}
	}
	return nil
}

func validateBlockPathCount(bz []byte, path []int32, count *int, limit int, limitErr error) error {
	var walk func([]byte, int) error
	walk = func(message []byte, depth int) error {
		for len(message) > 0 {
			field, payload, next, err := blockLimitField(message, path[depth])
			if err != nil {
				return err
			}
			if field == path[depth] {
				if depth == len(path)-1 {
					(*count)++
					if *count > limit {
						return limitErr
					}
				} else if err := walk(payload, depth+1); err != nil {
					return err
				}
			}
			message = next
		}
		return nil
	}
	return walk(bz, 0)
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
