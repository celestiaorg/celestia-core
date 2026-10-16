package protoresource

import (
	"errors"
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrBudget means input exceeds a reviewed resource limit.
var ErrBudget = errors.New("protobuf resource budget exceeded")
var errWire = errors.New("malformed protobuf wire data")

type usage struct {
	fields, messages, elements, copies, units uint64
	counts                                    []uint64
	limits                                    Limits
}

// Check rejects over-budget input without constructing protobuf objects.
// Scratch space depends only on the profile, not input cardinality.
func (p *Profile) Check(data []byte) error {
	_, err := p.Measure(data)
	return err
}

// Measure returns a structural allocation weight for admission accounting.
// Units include structural overhead and copying; they are not measured heap bytes.
func (p *Profile) Measure(data []byte) (uint64, error) {
	if uint64(len(data)) > p.limits.Bytes {
		return 0, ErrBudget
	}
	u := usage{limits: p.limits, counts: make([]uint64, p.counts)}
	if err := u.charge(1, 4096); err != nil {
		return 0, err
	}
	if err := u.scan(data, p.root, 1); err != nil {
		return 0, err
	}
	return u.units, nil
}

func (u *usage) charge(n, weight uint64) error {
	if n > math.MaxUint64/weight {
		return ErrBudget
	}
	return add(&u.units, n*weight, u.limits.AllocationUnits)
}
func (u *usage) field() error {
	if err := add(&u.fields, 1, u.limits.Fields); err != nil {
		return err
	}
	return u.charge(1, 32)
}
func (u *usage) copy(n uint64) error {
	if err := add(&u.copies, n, u.limits.CopyBytes); err != nil {
		return err
	}
	return u.charge(n, 2)
}

func add(counter *uint64, n, max uint64) error {
	if n > max-*counter {
		return ErrBudget
	}
	*counter += n
	return nil
}

func (u *usage) count(f field, n uint64) error {
	if f.index >= 0 {
		if err := add(&u.counts[f.index], n, f.max); err != nil {
			return fmt.Errorf("%w: %s", err, f.name)
		}
	}
	if f.repeated {
		if err := add(&u.elements, n, u.limits.Elements); err != nil {
			return err
		}
		return u.charge(n, 64)
	}
	return nil
}

// scan consumes one message. Unknown groups are handled by group.
func (u *usage) scan(data []byte, m *message, depth int) error {
	if depth > u.limits.Depth {
		return ErrBudget
	}
	for len(data) > 0 {
		num, wire, n := consumeTag(data)
		if n < 0 {
			return errWire
		}
		data = data[n:]
		if err := u.field(); err != nil {
			return err
		}
		if wire == protowire.EndGroupType {
			return errWire
		}
		f, known := field{index: -1}, false
		if m != nil {
			f, known = m.fields[num]
		}
		if !known {
			f = field{index: -1}
		}
		switch wire {
		case protowire.VarintType:
			_, n = consumeVarint(data)
			if n < 0 || (known && scalarWire(f.kind) != wire) {
				return errWire
			}
			if err := u.count(f, 1); err != nil {
				return err
			}
		case protowire.Fixed32Type, protowire.Fixed64Type:
			n = 4
			if wire == protowire.Fixed64Type {
				n = 8
			}
			if len(data) < n || (known && scalarWire(f.kind) != wire) {
				return errWire
			}
			if err := u.count(f, 1); err != nil {
				return err
			}
		case protowire.BytesType:
			b, size := consumeBytes(data)
			if size < 0 {
				return errWire
			}
			n = size
			if known && f.child != nil {
				if err := u.count(f, 1); err != nil {
					return err
				}
				if err := add(&u.messages, 1, u.limits.Messages); err != nil {
					return err
				}
				if err := u.charge(1, 4096); err != nil {
					return err
				}
				if err := u.scan(b, f.child, depth+1); err != nil {
					return err
				}
			} else if known && f.repeated && scalarWire(f.kind) != protowire.BytesType {
				if err := u.packed(b, f); err != nil {
					return err
				}
			} else {
				if known && scalarWire(f.kind) != protowire.BytesType {
					return errWire
				}
				if err := u.count(f, 1); err != nil {
					return err
				}
				if err := u.copy(uint64(len(b))); err != nil {
					return err
				}
			}
		case protowire.StartGroupType:
			if known {
				return errWire
			}
			// Find the matching end while charging every nested token. Avoid
			// ConsumeFieldValue, whose group recursion has no profile work budget.
			used, err := u.group(data, depth+1)
			if err != nil {
				return err
			}
			n = used
		default:
			return errWire
		}
		data = data[n:]
	}
	return nil
}

func (u *usage) packed(data []byte, f field) error {
	var count uint64
	switch scalarWire(f.kind) {
	case protowire.Fixed32Type:
		if len(data)%4 != 0 {
			return errWire
		}
		count = uint64(len(data) / 4)
	case protowire.Fixed64Type:
		if len(data)%8 != 0 {
			return errWire
		}
		count = uint64(len(data) / 8)
	case protowire.VarintType:
		for len(data) > 0 {
			_, n := consumeVarint(data)
			if n < 0 {
				return errWire
			}
			if err := u.count(f, 1); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	default:
		return errWire
	}
	return u.count(f, count)
}

func (u *usage) group(data []byte, depth int) (int, error) {
	if depth > u.limits.Depth {
		return 0, ErrBudget
	}
	rest := data
	for len(rest) > 0 {
		tag, n := consumeVarint(rest)
		wire := protowire.Type(tag & 7)
		if n < 0 {
			return 0, errWire
		}
		rest = rest[n:]
		if err := u.field(); err != nil {
			return 0, err
		}
		if wire == protowire.EndGroupType {
			return len(data) - len(rest), nil
		}
		switch wire {
		case protowire.StartGroupType:
			consumed, err := u.group(rest, depth+1)
			if err != nil {
				return 0, err
			}
			n = consumed
		case protowire.VarintType:
			_, n = consumeVarint(rest)
		case protowire.Fixed32Type:
			n = 4
		case protowire.Fixed64Type:
			n = 8
		case protowire.BytesType:
			b, consumed := consumeBytes(rest)
			n = consumed
			if n >= 0 {
				if err := u.copy(uint64(len(b))); err != nil {
					return 0, err
				}
			}
		default:
			return 0, errWire
		}
		if n < 0 || n > len(rest) {
			return 0, errWire
		}
		rest = rest[n:]
	}
	return 0, errWire
}

func scalarWire(kind protoreflect.Kind) protowire.Type {
	switch kind {
	case protoreflect.BoolKind, protoreflect.EnumKind, protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind, protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		return protowire.VarintType
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return protowire.Fixed32Type
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return protowire.Fixed64Type
	default:
		return protowire.BytesType
	}
}

// Match gogoproto generated integer loops, including truncation of unused
// high bits in a terminating tenth byte. Validation must not silently tighten
// the production decoder's accepted wire encodings.
func consumeVarint(data []byte) (uint64, int) {
	var value uint64
	for i, b := range data {
		if i == 10 {
			return 0, -1
		}
		value |= uint64(b&0x7f) << uint(i*7)
		if b < 0x80 {
			return value, i + 1
		}
	}
	return 0, -1
}
func consumeTag(data []byte) (protowire.Number, protowire.Type, int) {
	value, n := consumeVarint(data)
	num := protowire.Number(int32(value >> 3))
	if n < 0 || num <= 0 {
		return 0, 0, -1
	}
	return num, protowire.Type(value & 7), n
}
func consumeBytes(data []byte) ([]byte, int) {
	size, n := consumeVarint(data)
	if n < 0 || size > uint64(len(data)-n) {
		return nil, -1
	}
	return data[n : n+int(size)], n + int(size)
}
