# Protobuf schema inventory

Run `make proto-inventory` to generate `proto/schema.jsonl` from the repository's
pinned Buf compiler. `make proto-inventory-check` verifies the checked-in output
without modifying it. `make proto-gen` also regenerates the inventory, so the
existing generated-code CI check detects drift.

Each JSON line describes one message. Messages are sorted by fully qualified
name and fields by number. Records include all fields, empty and nested messages,
synthetic map entries, oneof membership, proto3 optional fields, and effective
packing. An omitted `packed` property means false. Packing records the schema's
serialization preference; it does not imply the decoder rejects the alternative
packed or unpacked representation. `optional` cardinality does not impose a
wire-occurrence limit.

All messages declared under the descriptor path prefix `tendermint/` are roots.
Service request/response types and message-field references bring imported
payload types into the inventory. Unreferenced annotation-only imports are
excluded. The traversal handles recursive message references. Enum fields retain
their fully qualified type name; enum values are not listed.

The tool reads a trusted, compiled `FileDescriptorSet` including imports:

```sh
go run ./scripts/proto-inventory -input descriptors.binpb -output schema.jsonl
```

Use `-prefix` to select another descriptor root (empty selects all files).
Invalid or unresolved descriptors, an empty selection, editions, and payload
extension declarations/ranges fail explicitly rather than producing a partial
inventory. Protobuf options, including custom Go representation options, are not
recorded in this structural format.

This inventory does not classify ingress trust, define resource limits, or change
runtime decoding. Those policies require separate review alongside the generated
Go representation and the code that receives and converts each message.
