package protoresource_test

import (
	"errors"
	"testing"

	"github.com/cometbft/cometbft/libs/protoresource"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/protobuf/encoding/protowire"
	v2proto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func limits() protoresource.Limits {
	return protoresource.Limits{Bytes: 1 << 20, Fields: 1000, Messages: 100, Elements: 100, CopyBytes: 1 << 20, Depth: 10, AllocationUnits: 1 << 30}
}
func profile(t testing.TB, name string, l protoresource.Limits, caps protoresource.FieldLimits) *protoresource.Profile {
	t.Helper()
	d, err := proto.HybridResolver.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		t.Fatal(err)
	}
	p, err := protoresource.New(proto.HybridResolver, d.FullName(), l, caps)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func bytesField(data []byte, num protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(data, num, protowire.BytesType), value)
}
func scalar(data []byte, num protowire.Number, value uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(data, num, protowire.VarintType), value)
}

func TestCumulativeOverwrittenMessages(t *testing.T) {
	// A final status does not undo work performed decoding earlier block responses.
	l := limits()
	l.Messages = 3
	p := profile(t, "tendermint.blocksync.Message", l, nil)
	var wire []byte
	for range 3 {
		wire = bytesField(wire, 3, bytesField(nil, 1, nil))
	}
	wire = bytesField(wire, 5, scalar(nil, 1, 1))
	var got bcproto.Message
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(wire); !errors.Is(err, protoresource.ErrBudget) {
		t.Fatalf("want budget, got %v", err)
	}
}

func TestRepeatedAndSplitFields(t *testing.T) {
	l := limits()
	p := profile(t, "tendermint.propagation.Message", l, protoresource.FieldLimits{"tendermint.propagation.CompactBlock.blobs": 2})
	// Resolve the field numbers from descriptors so the test also detects schema changes.
	d, _ := proto.HybridResolver.FindDescriptorByName("tendermint.propagation.Message")
	root := d.(protoreflect.MessageDescriptor)
	outer := root.Fields().ByName("compact_block").Number()
	child := root.Fields().ByName("compact_block").Message().Fields().ByName("blobs").Number()
	wire := bytesField(nil, outer, bytesField(nil, child, nil))
	wire = bytesField(wire, outer, bytesField(nil, child, nil))
	if err := p.Check(wire); err != nil {
		t.Fatal(err)
	}
	wire = bytesField(wire, outer, bytesField(nil, child, nil))
	if err := p.Check(wire); !errors.Is(err, protoresource.ErrBudget) {
		t.Fatalf("want cumulative field budget, got %v", err)
	}
}

func TestCopyAndFieldBudgets(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*protoresource.Limits)
		wire   []byte
	}{
		{"bytes", func(l *protoresource.Limits) { l.Bytes = 1 }, scalar(nil, 100, 1)},
		{"unknown copies", func(l *protoresource.Limits) { l.CopyBytes = 1 }, bytesField(nil, 100, []byte{1, 2})},
		{"unknown fields", func(l *protoresource.Limits) { l.Fields = 1 }, scalar(scalar(nil, 100, 1), 101, 2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			l := limits()
			test.modify(&l)
			p := profile(t, "tendermint.blocksync.Message", l, nil)
			if !errors.Is(p.Check(test.wire), protoresource.ErrBudget) {
				t.Fatal("expected budget rejection")
			}
		})
	}
}

func packedDescriptor(t testing.TB) *protoregistry.Files {
	t.Helper()
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{Name: v2proto.String("budget_test.proto"), Syntax: v2proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{{Name: v2proto.String("Lists"), Field: []*descriptorpb.FieldDescriptorProto{
		{Name: v2proto.String("numbers"), Number: v2proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_UINT64.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()},
		{Name: v2proto.String("fixed"), Number: v2proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_FIXED32.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()},
	}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := new(protoregistry.Files)
	if err := files.RegisterFile(fd); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestPackedUnpackedMixtures(t *testing.T) {
	l := limits()
	l.Elements = 3
	p, err := protoresource.New(packedDescriptor(t), "Lists", l, protoresource.FieldLimits{"Lists.numbers": 3})
	if err != nil {
		t.Fatal(err)
	}
	wire := scalar(nil, 1, 1)
	wire = bytesField(wire, 1, []byte{2, 3})
	if err := p.Check(wire); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.Check(scalar(wire, 1, 4)), protoresource.ErrBudget) {
		t.Fatal("split packed/unpacked counters reset")
	}
	if err := p.Check(bytesField(nil, 2, []byte{0, 0, 0})); err == nil {
		t.Fatal("accepted truncated packed fixed32")
	}
	if err := p.Check(bytesField(nil, 1, []byte{0x80})); err == nil {
		t.Fatal("accepted truncated packed varint")
	}
}

func TestUnknownGroupsAndMalformed(t *testing.T) {
	p := profile(t, "tendermint.blocksync.Message", limits(), nil)
	wire := protowire.AppendTag(nil, 100, protowire.StartGroupType)
	wire = bytesField(wire, 2, []byte("unknown"))
	wire = protowire.AppendTag(wire, 100, protowire.EndGroupType)
	if err := p.Check(wire); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{{0}, {0x80}, {0x0f}, wire[:len(wire)-1], bytesField(nil, 3, []byte{0x80}), protowire.AppendTag(nil, 100, protowire.EndGroupType)} {
		if err := p.Check(data); err == nil {
			t.Fatalf("accepted malformed %x", data)
		}
	}
	l := limits()
	l.Depth = 2
	p = profile(t, "tendermint.blocksync.Message", l, nil)
	nested := protowire.AppendTag(nil, 100, protowire.StartGroupType)
	nested = append(nested, wire...)
	nested = protowire.AppendTag(nested, 100, protowire.EndGroupType)
	if !errors.Is(p.Check(nested), protoresource.ErrBudget) {
		t.Fatal("unknown groups bypassed depth budget")
	}
}

func TestSetupFailsClosed(t *testing.T) {
	d, _ := proto.HybridResolver.FindDescriptorByName("tendermint.blocksync.Message")
	root := d.FullName()
	if _, err := protoresource.New(proto.HybridResolver, root, protoresource.Limits{}, nil); err == nil {
		t.Fatal("accepted zero limits")
	}
	if _, err := protoresource.New(proto.HybridResolver, root, limits(), protoresource.FieldLimits{"missing.field": 1}); err == nil {
		t.Fatal("accepted stale policy")
	}
}

func TestValidMessages(t *testing.T) {
	for _, msg := range []proto.Message{&bcproto.Message{Sum: &bcproto.Message_StatusResponse{StatusResponse: &bcproto.StatusResponse{Height: 42, Base: 1}}}, &propproto.Message{}} {
		wire, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		p := profile(t, proto.MessageName(msg), limits(), nil)
		if err := p.Check(wire); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzCheck(t *testing.F) {
	t.Add([]byte{})
	t.Add(bytesField(nil, 5, scalar(nil, 1, 1)))
	t.Add([]byte{0x1a, 0x02, 0x0a, 0x00})
	t.Add(bytesField(nil, 3, bytesField(nil, 1, bytesField(nil, 1, bytesField(nil, 4, scalar(nil, 2, 1000000000))))))
	p := profile(t, "tendermint.blocksync.Message", limits(), nil)
	t.Fuzz(func(t *testing.T, data []byte) {
		err := p.Check(data)
		if errors.Is(err, protoresource.ErrBudget) {
			return
		}
		var m bcproto.Message
		// Preflight does not replace custom decoder semantic checks (for example,
		// timestamp ranges). Preserve every decoder-supported encoding within budget.
		if decodeErr := proto.Unmarshal(data, &m); decodeErr == nil && err != nil {
			t.Fatalf("guard rejected decoder-supported input: %v", err)
		}
	})
}

func BenchmarkCheck(b *testing.B) {
	p := profile(b, "tendermint.blocksync.Message", limits(), nil)
	wire := bytesField(nil, 5, scalar(nil, 1, 100))
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	for b.Loop() {
		if err := p.Check(wire); err != nil {
			b.Fatal(err)
		}
	}
}

func TestProductionWireCompatibility(t *testing.T) {
	p := profile(t, "tendermint.blocksync.StatusResponse", limits(), nil)
	for _, wire := range [][]byte{
		{8, 0x81, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 2},
		{0xa3, 6, 0xac, 6}, {0x80, 0x80, 0x80, 0x80, 0x10, 0},
		{0x88, 0x80, 0x80, 0x80, 0x80, 1, 1},
	} {
		var msg bcproto.StatusResponse
		if err := msg.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if err := p.Check(wire); err != nil {
			t.Fatalf("rejected decoder-supported input %x: %v", wire, err)
		}
	}
}

func TestMapsAndRecursion(t *testing.T) {
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: v2proto.String("map_budget.proto"), Syntax: v2proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{{
			Name: v2proto.String("Node"), NestedType: []*descriptorpb.DescriptorProto{{Name: v2proto.String("ItemsEntry"), Options: &descriptorpb.MessageOptions{MapEntry: v2proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{
				{Name: v2proto.String("key"), Number: v2proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()},
				{Name: v2proto.String("value"), Number: v2proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()},
			}}}, Field: []*descriptorpb.FieldDescriptorProto{
				{Name: v2proto.String("items"), Number: v2proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: v2proto.String(".Node.ItemsEntry"), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()},
				{Name: v2proto.String("child"), Number: v2proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: v2proto.String(".Node"), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()},
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := new(protoregistry.Files)
	if err := files.RegisterFile(fd); err != nil {
		t.Fatal(err)
	}
	l := limits()
	l.Elements = 1
	l.Depth = 2
	p, err := protoresource.New(files, "Node", l, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.Check(bytesField(bytesField(nil, 1, nil), 1, nil)), protoresource.ErrBudget) {
		t.Fatal("map entries bypassed element budget")
	}
	if !errors.Is(p.Check(bytesField(nil, 2, bytesField(nil, 2, nil))), protoresource.ErrBudget) {
		t.Fatal("recursive schema bypassed depth budget")
	}
}

func TestAllocationUnits(t *testing.T) {
	l := limits()
	l.AllocationUnits = 4096
	p := profile(t, "tendermint.blocksync.StatusResponse", l, nil)
	if err := p.Check(nil); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.Check(scalar(nil, 1, 1)), protoresource.ErrBudget) {
		t.Fatal("field work not charged")
	}
	l.AllocationUnits = 1
	p = profile(t, "tendermint.blocksync.StatusResponse", l, nil)
	if !errors.Is(p.Check(nil), protoresource.ErrBudget) {
		t.Fatal("root work not charged")
	}
}
