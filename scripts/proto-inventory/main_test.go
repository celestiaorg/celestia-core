package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func fixture() *descriptorpb.FileDescriptorSet {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	field := func(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, label descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: kind.Enum(), Label: label.Enum()}
	}
	child := field("child", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, optional)
	child.TypeName, child.OneofIndex = proto.String(".dep.Payload"), proto.Int32(0)
	counts := field("counts", 2, descriptorpb.FieldDescriptorProto_TYPE_UINT64, repeated)
	labels := field("labels", 3, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, repeated)
	labels.TypeName = proto.String(".sample.Root.LabelsEntry")
	value := field("value", 4, descriptorpb.FieldDescriptorProto_TYPE_INT32, optional)
	value.OneofIndex, value.Proto3Optional = proto.Int32(1), proto.Bool(true)
	self := field("self", 5, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, optional)
	self.TypeName = proto.String(".sample.Root")
	unpacked := field("unpacked", 6, descriptorpb.FieldDescriptorProto_TYPE_FIXED32, repeated)
	unpacked.Options = &descriptorpb.FieldOptions{Packed: proto.Bool(false)}
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Root"),
		OneofDecl: []*descriptorpb.OneofDescriptorProto{
			{Name: proto.String("choice")}, {Name: proto.String("_value")},
		},
		Field: []*descriptorpb.FieldDescriptorProto{child, counts, labels, value, self, unpacked},
		NestedType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Empty")},
			{
				Name: proto.String("LabelsEntry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field: []*descriptorpb.FieldDescriptorProto{
					field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, optional),
					field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_BYTES, optional),
				},
			},
		},
	}
	packed := field("packed", 3, descriptorpb.FieldDescriptorProto_TYPE_INT64, repeated)
	packed.Options = &descriptorpb.FieldOptions{Packed: proto.Bool(true)}
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		{
			Name: proto.String("tendermint/sample.proto"), Package: proto.String("sample"), Syntax: proto.String("proto3"),
			Dependency: []string{"dep/payload.proto"}, MessageType: []*descriptorpb.DescriptorProto{root},
		},
		{
			Name: proto.String("dep/payload.proto"), Package: proto.String("dep"), Syntax: proto.String("proto2"),
			MessageType: []*descriptorpb.DescriptorProto{
				{
					Name: proto.String("Payload"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("name", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_LABEL_REQUIRED),
						field("values", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32, repeated), packed,
					},
				},
				{Name: proto.String("Unused")},
			},
		},
	}}
}

func marshalSet(t *testing.T, set *descriptorpb.FileDescriptorSet) []byte {
	t.Helper()
	b, err := proto.Marshal(set)
	require.NoError(t, err)
	return b
}

func TestInventory(t *testing.T) {
	b, err := inventory(marshalSet(t, fixture()), "tendermint/")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.Len(t, lines, 4)
	records := make(map[string]messageRecord)
	for _, line := range lines {
		var record messageRecord
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		records[record.Name] = record
	}
	require.NotContains(t, records, "dep.Unused")
	root := records["sample.Root"]
	require.Equal(t, "tendermint/sample.proto", root.File)
	require.Equal(t, "proto3", root.Syntax)
	require.Equal(t, []fieldRecord{
		{Number: 1, Name: "child", Kind: "message", Cardinality: "optional", Type: "dep.Payload", Oneof: "choice"},
		{Number: 2, Name: "counts", Kind: "uint64", Cardinality: "repeated", Packed: true},
		{Number: 3, Name: "labels", Kind: "message", Cardinality: "repeated", Type: "sample.Root.LabelsEntry"},
		{Number: 4, Name: "value", Kind: "int32", Cardinality: "optional", Oneof: "_value", Proto3Optional: true},
		{Number: 5, Name: "self", Kind: "message", Cardinality: "optional", Type: "sample.Root"},
		{Number: 6, Name: "unpacked", Kind: "fixed32", Cardinality: "repeated"},
	}, root.Fields)
	require.Empty(t, records["sample.Root.Empty"].Fields)
	require.Contains(t, string(b), `"fields":[]`)
	entry := records["sample.Root.LabelsEntry"]
	require.True(t, entry.MapEntry)
	require.Equal(t, "string", entry.Fields[0].Kind)
	require.Equal(t, "bytes", entry.Fields[1].Kind)
	payload := records["dep.Payload"]
	require.Equal(t, "proto2", payload.Syntax)
	require.Equal(t, "required", payload.Fields[0].Cardinality)
	require.False(t, payload.Fields[1].Packed)
	require.True(t, payload.Fields[2].Packed)
}

func TestInventoryDeterministic(t *testing.T) {
	set := fixture()
	before, err := inventory(marshalSet(t, set), "tendermint/")
	require.NoError(t, err)
	slices.Reverse(set.File[0].MessageType[0].Field)
	slices.Reverse(set.File[0].MessageType[0].NestedType)
	slices.Reverse(set.File[1].MessageType)
	set.File[0].SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{
		{Span: []int32{0, 0, 0}, LeadingComments: proto.String("comments do not change the inventory")},
	}}
	slices.Reverse(set.File)
	after, err := inventory(marshalSet(t, set), "tendermint/")
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestInventoryServiceImports(t *testing.T) {
	set := fixture()
	root := set.File[0]
	root.MessageType = nil
	root.Service = []*descriptorpb.ServiceDescriptorProto{{
		Name: proto.String("Service"), Method: []*descriptorpb.MethodDescriptorProto{{
			Name: proto.String("Call"), InputType: proto.String(".dep.Payload"), OutputType: proto.String(".dep.Payload"),
		}},
	}}
	b, err := inventory(marshalSet(t, set), "tendermint/")
	require.NoError(t, err)
	var record messageRecord
	require.NoError(t, json.Unmarshal(b, &record))
	require.Equal(t, "dep.Payload", record.Name)
}

func TestInventoryEnumAndGroup(t *testing.T) {
	set := fixture()
	payload := set.File[1].MessageType[0]
	payload.NestedType = []*descriptorpb.DescriptorProto{{Name: proto.String("Legacy")}}
	payload.EnumType = []*descriptorpb.EnumDescriptorProto{{
		Name: proto.String("Status"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("UNKNOWN"), Number: proto.Int32(0)}},
	}}
	payload.Field = append(payload.Field,
		&descriptorpb.FieldDescriptorProto{
			Name: proto.String("legacy"), Number: proto.Int32(4), Type: descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), TypeName: proto.String(".dep.Payload.Legacy"),
		},
		&descriptorpb.FieldDescriptorProto{
			Name: proto.String("status"), Number: proto.Int32(5), Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), TypeName: proto.String(".dep.Payload.Status"),
		},
	)
	b, err := inventory(marshalSet(t, set), "tendermint/")
	require.NoError(t, err)
	var record messageRecord
	require.NoError(t, json.Unmarshal([]byte(strings.Split(string(b), "\n")[0]), &record))
	require.Equal(t, fieldRecord{Number: 4, Name: "legacy", Kind: "group", Cardinality: "optional", Type: "dep.Payload.Legacy"}, record.Fields[3])
	require.Equal(t, fieldRecord{Number: 5, Name: "status", Kind: "enum", Cardinality: "optional", Type: "dep.Payload.Status"}, record.Fields[4])
}

func TestInventoryInvalidInput(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		_, err := inventory([]byte{0xff}, "")
		require.Error(t, err)
	})
	t.Run("missing import", func(t *testing.T) {
		set := fixture()
		set.File = set.File[:1]
		_, err := inventory(marshalSet(t, set), "tendermint/")
		require.Error(t, err)
	})
	t.Run("missing root", func(t *testing.T) {
		_, err := inventory(marshalSet(t, fixture()), "missing/")
		require.ErrorContains(t, err, "no messages")
	})
	t.Run("duplicate file", func(t *testing.T) {
		set := fixture()
		set.File = append(set.File, set.File[0])
		_, err := inventory(marshalSet(t, set), "tendermint/")
		require.Error(t, err)
	})
	t.Run("extensions", func(t *testing.T) {
		set := fixture()
		set.File[1].MessageType[0].ExtensionRange = []*descriptorpb.DescriptorProto_ExtensionRange{{Start: proto.Int32(100), End: proto.Int32(200)}}
		_, err := inventory(marshalSet(t, set), "tendermint/")
		require.ErrorContains(t, err, "extensions are not supported")
	})
	t.Run("editions", func(t *testing.T) {
		set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
			Name: proto.String("tendermint/edition.proto"), Package: proto.String("sample"),
			Syntax: proto.String("editions"), Edition: descriptorpb.Edition_EDITION_2023.Enum(),
			MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Message")}},
		}}}
		_, err := inventory(marshalSet(t, set), "tendermint/")
		require.ErrorContains(t, err, "unsupported syntax")
	})
}

func TestCheckMode(t *testing.T) {
	dir := t.TempDir()
	input, output := filepath.Join(dir, "descriptors.bin"), filepath.Join(dir, "schema.jsonl")
	set := fixture()
	require.NoError(t, os.WriteFile(input, marshalSet(t, set), 0o600))
	require.Error(t, run(input, output, "tendermint/", true))
	require.NoError(t, run(input, output, "tendermint/", false))
	require.NoError(t, run(input, output, "tendermint/", true))
	before, err := os.ReadFile(output)
	require.NoError(t, err)
	set.File[0].MessageType[0].Field[1].Name = proto.String("renamed_counts")
	require.NoError(t, os.WriteFile(input, marshalSet(t, set), 0o600))
	require.ErrorContains(t, run(input, output, "tendermint/", true), "stale")
	after, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, run(input, output, "tendermint/", false))
	require.NoError(t, run(input, output, "tendermint/", true))
}
