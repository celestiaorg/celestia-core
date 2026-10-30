# Fuzzing

Run native Go fuzz targets from the repository root. For example:

```sh
go test -run='^$' -fuzz='^FuzzMempool$' -fuzztime=1m -parallel=2 ./test/fuzz/tests
go test -run='^$' -fuzz='^FuzzConsensusMessage$' -fuzztime=1m -parallel=2 ./consensus
go test -run='^$' -fuzz='^FuzzPartFromProto$' -fuzztime=1m -parallel=2 ./types
```

Use `go test -run='^Fuzz' ./<package>` to replay seed corpora without mutation. See the
[Go fuzzing introduction](https://go.dev/doc/fuzz/) for more information.

### Structured inputs

Decoder targets and the legacy `test/fuzz/tests` targets take two byte slices,
`(raw, edits)` or `(raw, ops)`. With an empty second argument they feed `raw`
straight to the decoder, as a peer's wire bytes would arrive. Otherwise they
start from a valid fixture (a signed vote, a block, a message envelope, a
JSON-RPC request), apply the field edits that `internal/fuzzinput` reads from
the second argument, re-sign or rehash where needed, and encode the result
before running the same checks. Raw mode covers parser robustness; structured
mode reaches validation, signature and round-trip checks that random bytes
rarely get past. Stateful targets read their whole operation sequence through
the same kind of typed decoding.

## Decoder coverage inventory

Audited for [PROTOCO-3327](https://linear.app/celestia/issue/PROTOCO-3327)
against celestia-core main at `5f758c67451574702aad23b92ecd62af77fbc46d`,
including the targets merged in [#3512](https://github.com/celestiaorg/celestia-core/pull/3512).
The cross-repository inventory in
[PROTOCO-3321](https://linear.app/celestia/issue/PROTOCO-3321) builds on this
table; the stateful targets that close its celestia-core gaps are listed under
[Entry-point coverage](#entry-point-coverage). It describes exercised
paths, not measured code coverage or a completed AI security scan. No AI scan
date has been recorded for these entries.

| Entry point / package | Untrusted input | Native fuzz target(s) | Nightly |
| --- | --- | --- | --- |
| `cat.TxPool.CheckTx` | Transactions from peers and RPC, forwarded to ABCI | `FuzzMempool` in `test/fuzz/tests` | Yes |
| `cat.validateMempoolBytes`, mempool `Message.Unmarshal` / `Unwrap` | CAT Txs, SeenTx, WantTx envelopes | `FuzzMempoolMessage` in `mempool/cat` | Yes |
| Consensus `Message.Unmarshal` / `Unwrap`, `MsgFromProto` | All nine consensus envelope variants, including block parts | `FuzzConsensusMessage` in `consensus` | Yes |
| `blocksync.validateBlockSyncBytes`, `ValidateMsg`, block conversion | Block request/response and status envelopes, signature-count precheck | `FuzzBlockSyncMessage` in `blocksync` | Yes |
| Propagation `MsgFromProto` and per-message helpers | CompactBlock, HaveParts, WantParts, RecoveryPart | `FuzzPropagationMessage` in `consensus/propagation/types` | Yes |
| `types` block-level `FromProto`, validation and part-set insertion | Blocks, parts/proofs, votes, proposals, commits, evidence | `FuzzBlockFromProto`, `FuzzPartFromProto`, `FuzzVoteFromProto`, `FuzzProposalFromProto`, `FuzzCommitFromProto`, `FuzzEvidenceFromProto` in `types` | Yes |
| `types.unwrapLegacyBlobTx`, `Tx.Hash` / `Key` | Legacy BlobTx wire fields | `FuzzUnwrapLegacyBlobTx` in `types` (differential against generated decoder) | Yes |
| `conn.SecretConnection.Read` / `Write` | Encrypted peer connection traffic | `FuzzP2PSecretConnection` in `test/fuzz/tests` | Yes |
| JSON-RPC server | RPC request bytes | `FuzzRPCJSONRPCServer` in `test/fuzz/tests` | Yes |
| Evidence reactor envelope | EvidenceList protobuf envelope | `FuzzEvidenceReactorReceive` in `evidence` | Yes |
| State-sync message decoding | Snapshot/chunk/light-block/params envelopes | `FuzzStateSyncReactor` in `statesync` | Yes |
| P2P packet framing and node-info handshake | Packet and node-info wire bytes | `FuzzMConnectionRecv` in `p2p/conn`, `FuzzTransportUpgrade` in `p2p` | Yes |

The original decoder suite has **14 targets**; the last three rows are now
covered by the entry-point targets below.
CheckTx uses the in-process kvstore app, so it exercises core admission and tx
hashing, not celestia-app ante handlers. Its pool, caches and app are recreated
for every input so a full pool or a previously seen tx cannot mask a decoder
failure. A small configured tx limit also exercises rejection above that limit.
The new envelope targets check accepted-message round trips and let panics fail
the test. They bound inputs to 1 MiB (512 KiB for consensus); they do not test
maximum-size blocks or a live reactor's full state machine.

### Other existing targets and upstream comparison

The upstream audit checked CometBFT
[`v0.38.17`](https://github.com/cometbft/cometbft/tree/v0.38.17/test/fuzz)
(`d03254d3599b973f979314e6383b89fa1802e679`) and
[`main`](https://github.com/cometbft/cometbft/tree/6aef1e12197f587d1e85bc4cf3b3db4bf8d1be43/test/fuzz)
(`6aef1e12197f587d1e85bc4cf3b3db4bf8d1be43`). Both have the three native
mempool, SecretConnection and JSON-RPC harnesses under `test/fuzz/tests`;
neither tree has dedicated reactor-envelope or block-level decoder fuzz files.
Core's CAT-specific precheck and propagation paths require local targets.

Outside the decoder suite, core also has `FuzzParallelImplementations` in
`crypto/merkle`, which tests tree algorithms rather than network decoders and
is not in the nightly matrix. It derives every tree and proof index from the fuzz
input, so saved inputs replay exactly. `FuzzTxsToParts` and `FuzzTxsToParts_Panic` in
`consensus/propagation/types` now derive their data from the fuzz input and run
nightly; see [Entry-point coverage](#entry-point-coverage). `consensus/wal_fuzz.go` is a
legacy `gofuzz`-tagged WAL harness; `p2p/fuzz.go` simulates connection faults and
is not a native fuzz target. `test/fuzz/mempool/checktx.go` is a legacy byte-slice
harness, separate from the native CheckTx target. The OSS-Fuzz build script
still registers only its original three native targets.

## Entry-point coverage

The decoder targets above check that bytes parse. The targets below drive the
stateful handlers behind them (reactor `Receive`, pools, verification and block
execution) with sequences of attacker-chosen messages, and assert invariants
after each sequence: no panics, bounded per-peer state, only verified data
stored or relayed, and that valid inputs are accepted. They cover the P1 and P2
celestia-core entry points (CORE-1 to CORE-43) of the
[PROTOCO-3321](https://linear.app/celestia/issue/PROTOCO-3321) inventory. All
run in the nightly matrix.

| Entry point | Untrusted input | Native fuzz target(s) |
| --- | --- | --- |
| Consensus state machine and reactor | Proposals, block parts, votes, round-state messages from peers | `FuzzConsensusStateHandleMsg`, `FuzzConsensusReactorReceive` in `consensus` |
| SecretConnection handshake and MConnection framing | Raw peer TCP bytes before and after authentication | `FuzzSecretConnectionHandshake`, `FuzzSecretConnectionRead`, `FuzzMConnectionRecv` in `p2p/conn` |
| Transport upgrade and peer dispatch | Node-info handshake, channel envelopes | `FuzzTransportUpgrade`, `FuzzPeerReceive` in `p2p` |
| CAT mempool reactor | SeenTx, WantTx and Txs sequences from several peers | `FuzzReactorReceive` in `mempool/cat` |
| Compact-block propagation reactor | CompactBlock, HaveParts, WantParts, RecoveryPart sequences | `FuzzPropagationReactor` in `consensus/propagation` |
| Combined part sets and tx-to-part mapping | Recovery parts, parity data, tx metadata | `FuzzCombinedPartSetDecode`, `FuzzTxsToPartsMetadata`, `FuzzTxsToParts`, `FuzzTxsToParts_Panic` in `consensus/propagation/types` |
| Evidence pool and reactor | Duplicate-vote and light-client-attack evidence | `FuzzEvidencePool`, `FuzzEvidenceReactorReceive` in `evidence` |
| Blocksync reactor and pool | Status, block request and response sequences | `FuzzBlockSyncReactor` in `blocksync` |
| Commit and validator-set verification | Commits, extended commits, light blocks, validator sets | `FuzzVerifyCommit`, `FuzzExtendedCommitFromProto`, `FuzzLightBlockFromProto`, `FuzzValidatorSetFromProto` in `types` |
| Light client | Light blocks from primary and witnesses; provider HTTP responses | `FuzzLightClientVerify` in `light`, `FuzzHTTPProviderLightBlock` in `light/provider/http` |
| State-sync reactor and syncer | Snapshots, chunks, light blocks, params | `FuzzStateSyncReactor` in `statesync` |
| PEX reactor and address book | Address requests and address lists | `FuzzPexReactor` in `p2p/pex` |
| Block validation and execution | Proposed blocks and vote extensions | `FuzzBlockExecValidateApply`, `FuzzVerifyVoteExtension` in `state` |
| RPC handlers and JSON-RPC server | URI, JSON-RPC (single, batch, websocket) requests | `FuzzRPCCoreHandlers`, `FuzzRPCCoreJSONRPC` in `rpc/core` |
| Legacy gRPC API | BlockAPI and BlobstreamAPI requests | `FuzzRPCGRPCAPI` in `rpc/core` |

With these **29 targets** the nightly matrix runs **43 targets**.

Known limits:

- A few invariants are disabled with an `Excluded pending PROTOCO-<n>` comment
  until the linked issue is fixed. Remove the exclusion together with the fix
  and add the reproducer as a regression seed at that time.
- Timer-driven routines (gossip goroutines, blocksync pool request routines)
  run only as far as the harness drives them; they are not fuzzed on their own
  schedule.
- `broadcast_tx_commit` over JSON-RPC is not exercised, because it blocks on
  block commit.
- The trace, pprof and Prometheus listeners (CORE-44) are operator-only and out
  of scope.

## CI and crash handling

[The Fuzz Tests workflow](../../.github/workflows/fuzz-nightly.yml) runs each
listed nightly target independently for ten minutes on the daily schedule and
manual dispatch, or thirty seconds on relevant pull requests. It uses two
workers per target, restores previously interesting inputs from the fuzz cache,
and uploads `testdata/fuzz/Fuzz<Target>/` on failure. Failures on main feed the
existing `notify-protocol-ci.yml` workflow.

For any crash or invariant failure:

1. Download the failing-input artifact and copy its files into the target
   package's `testdata/fuzz/Fuzz<Target>/` directory.
2. Replay with `go test -run='Fuzz<Target>/<input-hash>' ./<package>`.
3. File a ticket linked to PROTOCO-3327 with the commit, target, input, command
   and stack trace. Distinguish a production failure from a harness assumption.
4. Include the minimized input as a regression corpus entry with the fix and
   replay it in ordinary tests before rerunning the fuzzer.
