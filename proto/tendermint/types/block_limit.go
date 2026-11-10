package types

import (
	"errors"
	"io"
)

var (
	ErrTooManyBlockTxs              = errors.New("block exceeds maximum transaction count")
	ErrTooManyBlockCommitSignatures = errors.New("block exceeds maximum commit signatures")
	ErrTooManyBlockEvidence         = errors.New("block exceeds maximum evidence items")
	ErrTooManyBlockEvidenceElements = errors.New("block exceeds maximum nested evidence elements")
)

// MaxBlockCommitSignatures is the largest validator set, so no valid commit exceeds it.
const MaxBlockCommitSignatures = 10_000

// BlockLimits are the counts CheckBlockCardinality enforces.
type BlockLimits struct {
	Txs int
	// EvidenceBytes is the consensus evidence allowance. Each evidence item holds
	// a 64-byte signature and each nested validator a 32-byte key.
	EvidenceBytes int64
	// AllOneofs also counts evidence values that a later oneof field replaces.
	AllOneofs bool
}

// CheckBlockCardinality checks field counts of the Block reached through prefix
// before Unmarshal allocates them, and never fails on bytes Unmarshal accepts.
func CheckBlockCardinality(bz []byte, lim BlockLimits, prefix ...int32) error {
	c := &cardinality{
		maxTxs:      lim.Txs,
		maxEvidence: int(lim.EvidenceBytes/64) + 1,
		maxElements: int(lim.EvidenceBytes/32) + 1,
		allOneofs:   lim.AllOneofs,
	}
	return c.at(bz, prefix)
}

type cardinality struct {
	maxTxs, maxEvidence, maxElements       int
	allOneofs                              bool
	txs, sigs, evidence                    int
	byzantineVals, conflictingSigs, valSet int
}

func (c *cardinality) at(bz []byte, prefix []int32) error {
	if len(prefix) == 0 {
		return c.block(bz)
	}
	return forEachField(bz, func(num int32, f []byte) error {
		if num != prefix[0] {
			return nil
		}
		return c.at(f, prefix[1:])
	})
}

func (c *cardinality) block(bz []byte) error {
	return forEachField(bz, func(num int32, f []byte) error {
		switch num {
		case 2: // data.txs
			return countField(f, 1, &c.txs, c.maxTxs, ErrTooManyBlockTxs)
		case 3: // evidence.evidence
			return forEachField(f, func(num int32, f []byte) error {
				if num != 1 {
					return nil
				}
				return c.evidenceItem(f)
			})
		case 4: // last_commit.signatures
			return countField(f, 4, &c.sigs, MaxBlockCommitSignatures, ErrTooManyBlockCommitSignatures)
		}
		return nil
	})
}

// evidenceItem counts an Evidence. Its oneof keeps only the last value set, so
// unless allOneofs, only a final light_client_attack_evidence is counted.
func (c *cardinality) evidenceItem(bz []byte) error {
	if c.evidence++; c.evidence > c.maxEvidence {
		return ErrTooManyBlockEvidence
	}
	var last []byte
	err := forEachField(bz, func(num int32, f []byte) error {
		switch {
		case num == 2 && c.allOneofs:
			return c.lightClientAttack(f)
		case num == 1 || num == 2:
			last = nil
			if num == 2 {
				last = f
			}
		}
		return nil
	})
	if err != nil || last == nil {
		return err
	}
	return c.lightClientAttack(last)
}

func (c *cardinality) lightClientAttack(bz []byte) error {
	return forEachField(bz, func(num int32, f []byte) error {
		switch num {
		case 1: // conflicting_block
			return forEachField(f, func(num int32, f []byte) error {
				switch num {
				case 1: // signed_header.commit.signatures
					return forEachField(f, func(num int32, f []byte) error {
						if num != 2 {
							return nil
						}
						return countField(f, 4, &c.conflictingSigs, c.maxElements, ErrTooManyBlockEvidenceElements)
					})
				case 2: // validator_set.validators
					return countField(f, 1, &c.valSet, c.maxElements, ErrTooManyBlockEvidenceElements)
				}
				return nil
			})
		case 3: // byzantine_validators
			if c.byzantineVals++; c.byzantineVals > c.maxElements {
				return ErrTooManyBlockEvidenceElements
			}
		}
		return nil
	})
}

func countField(bz []byte, field int32, count *int, limit int, limitErr error) error {
	return forEachField(bz, func(num int32, _ []byte) error {
		if num != field {
			return nil
		}
		if *count++; *count > limit {
			return limitErr
		}
		return nil
	})
}

// forEachField calls visit with the payload of every length-delimited field.
// It mirrors the generated Unmarshal loop; other fields use the generated skipper.
func forEachField(dAtA []byte, visit func(num int32, payload []byte) error) error {
	l := len(dAtA)
	for iNdEx := 0; iNdEx < l; {
		preIndex := iNdEx
		wire, err := decodeVarint(dAtA, &iNdEx)
		if err != nil {
			return err
		}
		if wire&0x7 != 2 {
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
		length, err := decodeVarint(dAtA, &iNdEx)
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
		if err := visit(int32(wire>>3), dAtA[iNdEx:postIndex]); err != nil {
			return err
		}
		iNdEx = postIndex
	}
	return nil
}

// decodeVarint is the generated decoder's varint loop: bits past 64 are dropped.
func decodeVarint(dAtA []byte, iNdEx *int) (uint64, error) {
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
