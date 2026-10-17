// Command proto-inventory records message structure from a compiled descriptor set.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

type messageRecord struct {
	Name     string        `json:"name"`
	File     string        `json:"file"`
	Syntax   string        `json:"syntax"`
	MapEntry bool          `json:"map_entry,omitempty"`
	Fields   []fieldRecord `json:"fields"`
}

type fieldRecord struct {
	Number         int32  `json:"number"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Cardinality    string `json:"cardinality"`
	Type           string `json:"type,omitempty"`
	Oneof          string `json:"oneof,omitempty"`
	Proto3Optional bool   `json:"proto3_optional,omitempty"`
	Packed         bool   `json:"packed,omitempty"`
}

func main() {
	input := flag.String("input", "", "compiled FileDescriptorSet, including imports")
	output := flag.String("output", "proto/schema.jsonl", "inventory output")
	prefix := flag.String("prefix", "tendermint/", "root descriptor file prefix (empty selects all)")
	check := flag.Bool("check", false, "fail if the existing output differs; do not write")
	flag.Parse()
	if err := run(*input, *output, *prefix, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input, output, prefix string, check bool) error {
	if input == "" {
		return errors.New("-input is required")
	}
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read descriptor set: %w", err)
	}
	want, err := inventory(data, prefix)
	if err != nil {
		return err
	}
	if check {
		got, err := os.ReadFile(output)
		if err != nil {
			return fmt.Errorf("read inventory: %w", err)
		}
		if !bytes.Equal(got, want) {
			return errors.New("protobuf inventory is stale; run make proto-inventory")
		}
		return nil
	}
	if err := os.WriteFile(output, want, 0o644); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	return nil
}

func inventory(data []byte, prefix string) ([]byte, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("decode descriptor set: %w", err)
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return nil, fmt.Errorf("resolve descriptors: %w", err)
	}
	// Include every root message and its nested declarations, then follow field
	// references into imports. Annotation-only imports are not payload schemas.
	seen := make(map[protoreflect.FullName]protoreflect.MessageDescriptor)
	var visit func(protoreflect.MessageDescriptor)
	visit = func(m protoreflect.MessageDescriptor) {
		if _, ok := seen[m.FullName()]; ok {
			return
		}
		seen[m.FullName()] = m
		for i := 0; i < m.Messages().Len(); i++ {
			visit(m.Messages().Get(i))
		}
		for i := 0; i < m.Fields().Len(); i++ {
			if child := m.Fields().Get(i).Message(); child != nil {
				visit(child)
			}
		}
	}
	var rootErr error
	files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		if strings.HasPrefix(f.Path(), prefix) {
			if f.Extensions().Len() != 0 {
				rootErr = fmt.Errorf("extension declarations are not supported: %s", f.Path())
				return false
			}
			for i := 0; i < f.Messages().Len(); i++ {
				visit(f.Messages().Get(i))
			}
			for i := 0; i < f.Services().Len(); i++ {
				methods := f.Services().Get(i).Methods()
				for j := 0; j < methods.Len(); j++ {
					visit(methods.Get(j).Input())
					visit(methods.Get(j).Output())
				}
			}
		}
		return true
	})
	if rootErr != nil {
		return nil, rootErr
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no messages found for descriptor prefix %q", prefix)
	}
	// This format describes proto2/proto3 field semantics. Fail explicitly if
	// a future schema needs edition-specific feature handling.
	records := make([]messageRecord, 0, len(seen))
	for _, m := range seen {
		if m.Extensions().Len() != 0 || m.ExtensionRanges().Len() != 0 {
			return nil, fmt.Errorf("message extensions are not supported: %s", m.FullName())
		}
		if m.Syntax() != protoreflect.Proto2 && m.Syntax() != protoreflect.Proto3 {
			return nil, fmt.Errorf("unsupported syntax for %s: %s", m.FullName(), m.Syntax())
		}
		record := messageRecord{
			Name: string(m.FullName()), File: m.ParentFile().Path(), Syntax: m.Syntax().String(),
			MapEntry: m.IsMapEntry(), Fields: make([]fieldRecord, 0, m.Fields().Len()),
		}
		for i := 0; i < m.Fields().Len(); i++ {
			f := m.Fields().Get(i)
			field := fieldRecord{
				Number: int32(f.Number()), Name: string(f.Name()), Kind: f.Kind().String(),
				Cardinality: f.Cardinality().String(), Packed: f.IsPacked(),
			}
			if child := f.Message(); child != nil {
				field.Type = string(child.FullName())
			} else if enum := f.Enum(); enum != nil {
				field.Type = string(enum.FullName())
			}
			if oneof := f.ContainingOneof(); oneof != nil {
				field.Oneof = string(oneof.Name())
				field.Proto3Optional = oneof.IsSynthetic()
			}
			record.Fields = append(record.Fields, field)
		}
		sort.Slice(record.Fields, func(i, j int) bool { return record.Fields[i].Number < record.Fields[j].Number })
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			return nil, fmt.Errorf("encode inventory: %w", err)
		}
	}
	return out.Bytes(), nil
}
