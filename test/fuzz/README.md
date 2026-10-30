# Fuzzing

Run native Go fuzz targets from the repository root. For example:

```sh
go test -run='^$' -fuzz='^FuzzMempool$' -fuzztime=1m -parallel=2 ./test/fuzz/tests
go test -run='^$' -fuzz='^FuzzConsensusMessage$' -fuzztime=1m -parallel=2 ./consensus
go test -run='^$' -fuzz='^FuzzPartFromProto$' -fuzztime=1m -parallel=2 ./types
```

Use `go test -run='^Fuzz' ./<package>` to replay seed corpora without mutation. See the
[Go fuzzing introduction](https://go.dev/doc/fuzz/) for more information.

## Decoder coverage inventory

Audited for [PROTOCO-3327](https://linear.app/celestia/issue/PROTOCO-3327)
against celestia-core main at `5f758c67451574702aad23b92ecd62af77fbc46d`,
including the targets merged in [#3512](https://github.com/celestiaorg/celestia-core/pull/3512).
The cross-repository inventory in
[PROTOCO-3321](https://linear.app/celestia/issue/PROTOCO-3321) is still pending;
this table records the core findings for that inventory. It describes exercised
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
| Evidence reactor envelope | EvidenceList protobuf envelope | No dedicated envelope target; evidence conversion is covered above | No |
| State-sync message decoding | Snapshot/chunk/light-block/params envelopes | No dedicated target | No |
| P2P packet framing and node-info handshake | Packet and node-info wire bytes | No dedicated target; SecretConnection does not cover these decoders | No |

The nightly decoder suite has **14 targets**. Nine of the twelve entry-point
groups above have direct targets; the remaining three are follow-up gaps.
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
`crypto/merkle` and `FuzzTxsToParts` / `FuzzTxsToParts_Panic` in
`consensus/propagation/types`. These test tree algorithms and reconstruction,
not raw network decoders, and are not in this nightly matrix. The reconstruction
harnesses also generate data from wall-clock-seeded randomness, so saved inputs
do not fully reproduce their generated blocks. `consensus/wal_fuzz.go` is a
legacy `gofuzz`-tagged WAL harness; `p2p/fuzz.go` simulates connection faults and
is not a native fuzz target. `test/fuzz/mempool/checktx.go` is a legacy byte-slice
harness, separate from the native CheckTx target. The OSS-Fuzz build script
still registers only its original three native targets.

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
