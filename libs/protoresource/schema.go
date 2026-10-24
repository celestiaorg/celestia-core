// Package protoresource checks protobuf wire resource budgets before decoding.
package protoresource

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Limits bounds one complete decode, including overwritten field occurrences.
// All limits must be positive. Units are structural work, not exact heap bytes.
type Limits struct {
	Bytes, Fields, Messages, Elements, CopyBytes, AllocationUnits uint64
	Depth                                                         int
}

// FieldLimits bounds cumulative occurrences of selected schema fields across
// the entire input. Packed scalar elements count separately.
type FieldLimits map[protoreflect.FullName]uint64

// Profile is immutable and can be shared by concurrent callers.
type Profile struct {
	root   *message
	limits Limits
	counts int
}

type message struct {
	fields map[protoreflect.FieldNumber]field
}
type field struct {
	kind     protoreflect.Kind
	name     protoreflect.FullName
	repeated bool
	child    *message
	index    int
	max      uint64
}

// New compiles a profile from generated descriptors, without decoding input.
// Unknown policy fields and unsupported schema features fail at setup time.
func New(resolver protodesc.Resolver, name protoreflect.FullName, limits Limits, caps FieldLimits) (*Profile, error) {
	if resolver == nil || !name.IsValid() || limits.Bytes == 0 || limits.Fields == 0 || limits.Messages == 0 || limits.Elements == 0 || limits.CopyBytes == 0 || limits.AllocationUnits == 0 || limits.Depth <= 0 || limits.Depth > 100 {
		return nil, errors.New("protoresource: missing schema or invalid limits")
	}
	p := &Profile{limits: limits}
	seen := make(map[protoreflect.FullName]*message)
	used := make(map[protoreflect.FullName]bool)
	var compile func(protoreflect.FullName) (*message, error)
	compile = func(name protoreflect.FullName) (*message, error) {
		if m := seen[name]; m != nil {
			return m, nil
		}
		desc, err := resolver.FindDescriptorByName(name)
		if err != nil {
			return nil, err
		}
		d, ok := desc.(protoreflect.MessageDescriptor)
		if !ok || d.IsPlaceholder() {
			return nil, fmt.Errorf("protoresource: unresolved message %s", name)
		}
		if d.ExtensionRanges().Len() != 0 || d.Extensions().Len() != 0 || (d.Syntax() != protoreflect.Proto2 && d.Syntax() != protoreflect.Proto3) {
			return nil, fmt.Errorf("protoresource: unsupported schema %s", d.FullName())
		}
		m := &message{fields: make(map[protoreflect.FieldNumber]field, d.Fields().Len())}
		seen[d.FullName()] = m
		for i := 0; i < d.Fields().Len(); i++ {
			f := d.Fields().Get(i)
			if f.Kind() == protoreflect.GroupKind {
				return nil, fmt.Errorf("protoresource: unsupported group field %s", f.FullName())
			}
			v := field{kind: f.Kind(), repeated: f.IsList() || f.IsMap(), index: -1}
			if cap, ok := caps[f.FullName()]; ok {
				if cap == 0 {
					return nil, fmt.Errorf("protoresource: zero cap for %s", f.FullName())
				}
				v.index, v.max = p.counts, cap
				v.name = f.FullName()
				p.counts++
				used[f.FullName()] = true
			}
			if f.Message() != nil {
				child, err := compile(f.Message().FullName())
				if err != nil {
					return nil, err
				}
				v.child = child
			}
			m.fields[f.Number()] = v
		}
		return m, nil
	}
	var err error
	p.root, err = compile(name)
	if err != nil {
		return nil, err
	}
	for name := range caps {
		if !used[name] {
			return nil, fmt.Errorf("protoresource: policy field %s is not reachable", name)
		}
	}
	return p, nil
}
