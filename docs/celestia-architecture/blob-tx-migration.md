# Blob transaction migration

This is the first stage of [#3370](https://github.com/celestiaorg/celestia-core/issues/3370).
It separates consensus envelope extraction from application blob validation.
It does not complete the migration or remove the legacy wire definitions.

## Consensus compatibility

`types.ExtractBlobTx` returns the inner transaction only when the historical
decoder recognizes the envelope. Otherwise it returns the original bytes.
`Tx.Hash`, `Tx.Key`, normal block execution, transaction events, and
`ExecCommitBlock` use this API. Hash and Key retain their existing, different
IndexWrapper precedence.

The underlying generated decoder and recognition checks are unchanged: decoding
must succeed, the type ID must be `BLOB`, at least one blob must exist, and every
namespace ID must have the historical length. Namespace contents and versions,
share versions, blob data, and signer contents are not validated by this path.
This preserves the historical execution path at every height; there is no new
consensus upgrade boundary. Applications remain responsible for blob validation.

The go-square validating decoder cannot replace this path directly. It can return
`nil, true, err` for a recognized but invalid envelope. Even substituting its
protobuf decoder without domain validation changes recognition: its protobuf
runtime skips a namespace field with the wrong wire type, while core's generated
decoder rejects it. Signer is an unknown field in core's historical schema.

The fixture corpus in `internal/test/blobtx` uses go-square domain objects for
valid legacy and signer-bearing blobs, and go-square protobufs/raw wire fields
for invalid cases. It covers invalid namespaces and versions, empty data and
blobs, truncated protobufs, unknown and duplicate fields, and mismatched wire
types. The same corpus checks Hash/Key, normal FinalizeBlock, events, and replay.
A fuzz target compares extraction and hashing with the retained legacy API.
These are synthetic regression checks, not a replay of archived chain data.

## Dependency and caller inventory

Keep the existing go-square/v3 v3.0.2 dependency for this stage. The inspected
celestia-app main go.mod also includes v3.0.2, alongside v2.3.3 and v4.0.1, and
selects core v0.42.1. This is not approval to change the app's selected major
version or to backport to any release branch. Re-evaluate the target app/release
before replacing the compatibility decoder.

Core's production callers no longer consume the exported legacy BlobTx value.
The mempool removal test and execution fixtures construct valid blobs with
go-square. Tests of the retained legacy API intentionally continue to exercise
its helpers and generated types.

The local celestia-app checkout at
`e4dfbfeeb93d1ffc6a6fd0dffc53f960e7d4bcc9` still calls the legacy decoder in:

- `tools/chainbuilder/main.go`
- `app/test/{integration,process_proposal,prepare_proposal,check_tx,blob_ordering}_test.go`
- `test/util/blobfactory/payforblob_factory_test.go`

This is a scoped inventory, not an ecosystem-wide audit. Migrate blob consumers
to the go-square version selected by their app release, handling the error before
dereferencing the decoded transaction. Migrate envelope-only consumers to the
compatibility extraction API when historical recognition is required. These
downstream changes have not been submitted by this core PR.

## Remaining work before removal

- Coordinate and verify downstream migrations, including generated API and
  protobuf descriptor consumers.
- Design a shared extraction API backed by go-square wire definitions, preserving
  the historical decoder's acceptance rules. Extend differential fuzzing against
  a frozen legacy oracle and replay archived blocks before switching to it.
- If any recognition change is intentional, agree on a consensus upgrade height
  and keep historical behavior below that height, including replay.
- Remove the exported helpers and protobuf messages only in an appropriate
  breaking release after callers migrate. Regenerate protobuf code and inspect
  descriptor changes then. A major release alone does not authorize a consensus
  behavior change.

The legacy generated messages, registration, and descriptors remain intact in
this stage, so protobuf regeneration is unnecessary.
