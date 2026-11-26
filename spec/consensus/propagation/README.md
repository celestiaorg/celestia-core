# Compact-block propagation <-> consensus hand-off (Quint)

A Quint model of the hand-off between the compact-block propagation reactor
(`consensus/propagation`) and the consensus state machine
(`consensus/state.go`), with the properties it is checked against. It was
written to reproduce PROTOCO-3430 (fixed in `ab6a3df`, "forward each compact
block to consensus once") and to check the fixed code. The readers and
maintainers are agents: this file is a reference, not a tutorial.

| File | Content |
|---|---|
| `propagation.qnt` | the parametric model (module `propagation`) |
| `instances.qnt` | configurations: `fixed`, `fixedCap2`, `prefix`, `forwardOnceOnly`, `asyncOnly`, `live`, and `*Traced` twins |
| `propagation_test.qnt` | scenario `run` tests, including both counterexamples |
| `Makefile`, `expect_violation.sh`, `tlc.json` | CI entry points |
| `traces/` | ITF traces (`make traces`): one per scenario test (`prefix_waitCycleTest` = PROTOCO-3430 cycle, `prefix_duplicateForwardTest` = duplicate forward, `forwardOnceOnly_rsMtxCycleTest` = cycle through `cs.rsMtx` with only half (a) applied, `fixed_*` = the same inputs and other scenarios with the fix), plus three random runs of `fixedTraced` |

## Scope

One height `H` (the fuzz harness's height 5), rounds `0..MAX_ROUND` (1), the
local node, one proposer key and two peers `p1`, `p2`. `p1` is honest for L1;
`p2` has no fairness and may send anything a peer can send. Blocks:

| block | meaning | fuzz fixture |
|---|---|---|
| `A` | honest proposal; same block at rounds 0 and 1 | `fx.blocks[0]` (5/0), `fx.blocks[2]` (5/1) |
| `B` | another part set, signed by a byzantine proposer | `fx.blocks[1]` (has 2 parts, not 4; see caveats) |
| `X` | `A`'s part set header with another block hash, signed by a byzantine proposer | op 1, mutation 4 (`BlockID.Hash`) |

`BLOCKS` is the set the proposer key signs (`{A}` = honest proposer).
`COMMITS` is the set of part set headers consensus may report through
`AddCommitment`; one round gets at most one header.

Parameters (constants of `propagation`): `K` original parts (4; `2K` with
parity), `MEMPOOL` (originals `0..MEMPOOL-1` rebuildable from the mempool),
`CAP_PROP`, `CAP_PART`, `CAP_PMQ` (capacities of `proposalChan`, `partChan`,
`peerMsgQueue`; 1 or 2), `VOTES` (the consensus reactor may queue messages
in `peerMsgQueue`), `FORWARD_ONCE` and `ASYNC_DELIVERY` (the two halves of
`ab6a3df`), `TRACE`, `LOG_CAP`.

## Processes

Each is one Go goroutine. A step of a process is atomic. A blocking Go send
is a program counter where the process stays until the channel has room.

| process | Go code | blocking points | locks held while blocked |
|---|---|---|---|
| CONS | `State.receiveRoutine` (consumer of `peerMsgQueue`) and the propagator calls it makes under `lockAll` | pre-fix only: `proposalChan` send and `partChan` sends in `handleCachedCompactBlock` (pc `sendProp`, `sendParts`) | `cs.mtx`, `cs.stateMtx`, `cs.rsMtx` |
| SYNC | `State.syncData` (consumer of `proposalChan`, `partChan`) | `cs.rsMtx.RLock` (pc `needRs`), `peerMsgQueue` send (pc `push`) | none |
| RECV | the reactor's receive goroutine (`BaseReactor` processor; one for all peers) | `proposalChan` send in `processValidatedCompactBlock`; `partChan` sends in `handleRecoveryPart`, `publishLocalParts` | none |
| DELIV(r) | `deliverCachedCompactBlock` (post-fix), at most one per round | `proposalChan`, `partChan` sends | none |
| retry | `retryWants` (ticker goroutine and `go retryWants()`) | none (only `TrySend`) | modelled as one atomic step |

The reactor holds no mutex across a channel send: `pmtx`, `mtx` and the
peer-state mutexes guard short sections (checked by reading every send site:
`processValidatedCompactBlock`, `handleRecoveryPart`, `publishLocalParts`,
`deliverCachedCompactBlock`, and pre-fix `handleCachedCompactBlock`). So
each `pmtx` section is folded into the step that runs it.

Call sites of the propagator in `consensus/state.go`, all on the receive
routine under `lockAll` (or in `OnStart` before it runs):
`SetConsensusState` (`enterNewRound`, `OnStart`, `NewState`),
`AddCommitment` (`enterPrecommit` unlock path, `enterCommit`,
`addVote` polka, `OnStart` lock restore), `Prune` (`finalizeCommit`),
`ProposeBlock` (`enterPropose`, `reproposeValidBlock`), `AddBlock`,
`AddProposedBlock`, `GetProposal`, `IsCatchingUp`, `SetBlockMaxBytes`; and
from `consensus/reactor.go`: `StartProcessing` (`go retryWants()`),
`IsCatchingUp`.
Post-fix none of them blocks: `SetConsensusState` hands cached proposals to a
goroutine; `AddCommitment`, `AddBlock`, `Prune` spawn goroutines;
`ProposeBlock` calls `handleCompactBlock(.., proposer=true)`, which never
sends to `proposalChan` and only `TrySend`s.

## State

| variable | content |
|---|---|
| `prop` | reactor: `round` (`ProposalCache.round`), `entries` (round -> entry), `unverified` (peer -> cached compact block), `reqO`/`reqP` ((peer, round) requested), `gossiped` |
| `cons` | consensus round, CONS pc and the cached block being applied, `committed` (header per round given to `AddCommitment`) |
| `syncd`, `recv`, `deliv` | pcs of SYNC, RECV, DELIV |
| `propChan`, `partChan`, `pmq` | bounded channels (FIFO lists) |
| `hist` | property history: `fwd` (round, block) sent into `proposalChan`; flags `dupForward`, `s3Decision`, `s3Gossip`, `s3Deliver`, `delivOverwrite` |
| `obs` | `propLog` (sends into `proposalChan`), `consLog` (proposals consensus took from `peerMsgQueue`), capped at `LOG_CAP` |
| `lastAction` | the step just taken (trace schema below) |

An entry is `{kind, block, psh, forwarded, catchup, orig, par}`. `kind`
`full` = built from a compact block (`AddProposal`, has a parity set);
`stub` = built by `AddCommitment` (zero `BlockID`, identity is the part set
header only, no parity set, cannot decode); `none`. `orig` = held original
indexes, `par` = number of held parity parts.

## Abstractions and what each could hide

| # | abstraction | could hide |
|---|---|---|
| 1 | One height; `Prune(H)` only clears unverified proposals. No height change, no block store contents | bugs across heights, `serveCommitted`, `handleStoredWants`, catch-up marking on height change |
| 2 | Signatures: only proposer-signed compact blocks exist; validation fails only on the round. A peer cannot forge. Forged compact blocks (which `StoreUnverifiedProposal` also caches) are not sent | a forged block overwriting the sender's own unverified slot (affects only that peer's slot) |
| 3 | Erasure coding: "any `K` of `2K` parts of a full entry decode it"; decoding rebuilds all originals and parity at once. Completing the originals without decoding also adds the parity in the same step (`withParity`); in Go `buildParity` runs after the parked `partChan` sends finish | encoder-specific failures, `IsDecoding` races |
| 4 | Part identity: a part is genuine or corrupt; corrupt parts always fail verification. Parts carry only the round into `partChan` | parts of an orphaned (replaced) part set that is decoded later still being sent to consensus (`handleRecoveryPart` decodes the part set pointer it read; the model decodes the current entry only) |
| 5 | Canonical part indexes: the environment sends the highest missing original outside the mempool set, then parity in increasing order (indexes of one kind are interchangeable; mempool overlap is kept minimal, which maximises sends) | orders where the mempool overlaps parts already received (fewer sends) |
| 6 | Requests: `reqO`/`reqP` flags per (peer, round): "the missing originals / parity were requested". Haves set them at once (the `requestFromPeer` goroutine is folded in); `ConcurrentRequestLimit`, `ReqLimit`, request expiry and per-index request bits are not modelled | liveness problems caused by request limits or expiry; over-admission of parts (sound for safety, optimistic for L1) |
| 7 | Push allowance (`GrantPushAllowance`) not modelled: unrequested parts are dropped | the push path's extra `partChan` sends (same blocking behaviour as requested parts) |
| 8 | `handleWants` leaves the modelled state unchanged (it only `TrySend`s and parks wants) | bugs in serving wants |
| 9 | Haves gossip, `SetHasProposal`, consensus peer state, `ResetRequestCounts`, tracing: not modelled | none for S1-S3 |
| 10 | `processValidatedCompactBlock`'s `AddProposal`, `conflictsWith`, `claimForward` are one step (three `pmtx` sections in Go). `claimForward` re-checks the identity, so the merge removes no unsafe interleaving for the forward decision. The send and the later broadcast/mempool step are separate steps | TOCTOU between `AddProposal` and `conflictsWith` when FORWARD_ONCE is off |
| 11 | `SetConsensusState` + the `GetProposal` check + the peer pick are one step; `handleCachedCompactBlock` is the next CONS step (RECV may run between them, as in Go). If RECV adds the entry first, the model still runs `handleCachedCompactBlock` (with `forward = false` post-fix) where Go may return early | nothing for S1-S3: an extra non-forwarding delivery goroutine |
| 12 | syncData's `newHeightOrRoundChan` branch is not modelled; its filters on `ProposalBlockParts`, commit and lock rounds are not modelled (parts of the current round are always pushed) | a cycle through that branch needs CONS parked, which post-fix never happens (it takes the same lock and queue as the `partChan` branch) |
| 13 | The consensus reactor's own `peerMsgQueue` producer (`VOTES`) is an environment step that adds only when there is room | its goroutine blocking (it holds no lock) |
| 14 | `AddCommitment`'s and `deliverCachedCompactBlock`'s `go retryWants()` are left to `envRetry` (DELIV runs it inline at its end, as in Go) | none for safety; timing of requests |
| 15 | All peers have a consensus height >= H-1 (`retryWants` asks every peer) | fuzz op 12 effects |
| 16 | Go map iteration order (peers in `applyCachedProposalIfAvailable`) is a free choice | none |
| 17 | The reactor is started (`started = true`, as the fuzz harness sets it); before `StartProcessing` haves, wants and parts are dropped | behaviour during block sync |
| 18 | `TRACE = false` keeps `lastAction`, `obs`, `gossiped` and the contents of `proposalChan`/`peerMsgQueue` constant (only their length matters). No guard reads them, so the other variables' behaviours are unchanged; this is what makes the exhaustive runs fit | none |

## Reductions for exhaustive checking

The full configuration (`fixed`: byzantine proposer with `A`, `B`, `X`,
commitments for `A` or `B`, parts, mempool, votes) has more than 10^8
distinct states (TLC reached 8.2 M distinct states at depth 14 after 7 min
with the queue still growing), so the safety check is split:

| configuration | what is on | what is off | what the split could hide |
|---|---|---|---|
| `fixedIdentity` | byzantine proposer (`A`, `B`, `X`), commitments `A`/`B`, votes, both forward paths, async delivery | parts (`PARTS = false`, `MEMPOOL = 0`): no haves, no recovery parts, no `partChan` traffic | interleavings where RECV or DELIV is busy sending parts while identities change. Part steps never read or write identity, `forwarded` or the unverified cache; they only delay RECV/DELIV |
| `fixedParts` | parts, decoding, mempool recovery (`partChan` traffic from RECV and DELIV), votes, both forward paths | honest proposer only (`BLOCKS = COMMITS = {A}`) | part traffic combined with conflicting identities |

Both run with `TRACK_REQUESTS = false`: every genuine part is admitted, an
over-approximation of `admitPart` that only adds behaviours (sound for
invariants).

The combined `fixed` configuration is covered only by random simulation
(`make simulate` runs `fixedTraced`, which also tracks requests).

L1 runs with `TRACK_REQUESTS = true` and `step = liveStep`, the honest
environment: `p1` relays `A`, answers requests and sends haves; `p2` is
silent; consensus may enter rounds, report commitments for `A` and prune at
any time; the retry ticker runs. `live` covers rounds 0 and 1 (47 min);
`liveOneRound` covers round 0 only (CI). With `p2` active the two-round
liveness state space did not finish in 28 min on 4 cores; a byzantine `p2`
is therefore excluded from the L1 runs (it is included in all safety runs).
What this could hide: a byzantine peer starving progress, for example by
making the node request parts only from it. In the model `retryWants` asks
every peer, so this needs the request limits that abstraction 6 removes.

## Properties

| name | kind | statement |
|---|---|---|
| `S1_noWaitCycle` | invariant | the wait-for graph has no cycle. A process is blocked if parked on a full channel or on `cs.rsMtx` held by CONS; it waits on the channel's only consumer (CONS for `peerMsgQueue`, SYNC for `proposalChan`/`partChan`) or the lock holder. `deadlocked` is the greatest set of blocked processes all of whose waitees are in the set; S1 says it is empty. Environment steps are always enabled, so TLC's own deadlock check cannot see this. |
| `S2_forwardOnce` | invariant | no (round, block) enters `proposalChan` twice |
| `S3_decision` | invariant | a forward is never decided for a compact block whose identity differs from the entry stored for its round at decision time |
| `S2_strictPerRound` | invariant (informational) | at most one proposal of any identity per round enters `proposalChan`. Not expected to hold: an `AddCommitment` for another header replaces the entry, and the committed block's compact block is then forwarded once more, by design |
| `sanity` | invariant | channel bounds, part counts, no DELIV slot reused |
| `L1_eventualCompletion` | temporal | `fairness implies eventually(holdsBlock)`: with an honest proposer, the node eventually holds all `K` originals of some round |
| `L1_withoutFairness` | temporal (vacuity check) | `eventually(holdsBlock)`; must fail, so L1 depends on the fairness assumptions |

Fairness for L1 (`fairness` in the model):
- weak fairness on `internalStep` (the disjunction of all goroutine steps).
  Without new input every internal run is finite (each step consumes a
  queued item or advances a pc; forward-once bounds the proposals), so this
  lets every goroutine finish its work; TLC would report a starving run.
- strong fairness on `p1Useful`: a `p1` message that makes progress (the
  compact block that creates the entry at the reactor's round, or a part the
  node adds). The p2p incoming queue is FIFO and RECV is idle infinitely
  often. Each useful message strictly increases a bounded measure.
- weak fairness on `envRetry` (the ticker fires every `RetryTime`).
- consensus calls and `p2` get none.

Per-action strong fairness (26 conjuncts) made TLC's liveness check take
7 min for 11 k states; the three conjuncts above check the same goal in
seconds per checkpoint.

## Trace schema (ITF)

Traces come from the `*Traced` instances and the test modules. Variable
names in ITF are qualified (`fixedTraced::propagation::prop`); strip up to
the last `::`. Integers are `{"#bigint": "n"}`, sets `{"#set": [...]}`,
tuples `{"#tup": [...]}`, maps `{"#map": [[k, v], ...]}`.

`lastAction` = `{kind, env, peer, round, block, psh, part, idx, corrupt, proof, accepted}`.
Unused fields are `""`, `-1` or `false`. `env = true` marks environment
actions (replay them); `env = false` marks internal scheduling steps (the
Go runtime does these; a replay only waits for quiescence). `accepted`
reports the action's effect (block validated and not conflicting; part
added; header replaced an entry), so a replay can check it.

| kind | env | Go entry point | fuzz op (`reactor_fuzz_test.go` `step`) | parameters |
|---|---|---|---|---|
| `compactBlock` | yes | `Receive` -> `handleCompactBlock` | op 0 (`block = A`, fixture by round); op 1 (re-signed; `B` = fixture 1, `X` = mutation 4) | peer, round, block, psh |
| `have` | yes | `handleHaves` (+ `requestFromPeer`) | op 3, one entry | peer, round, part (`orig`/`parity`), idx |
| `want` | yes | `handleWants` | op 4 | peer, round |
| `part` | yes | `handleRecoveryPart` | op 6 (genuine; mask `1<<idx`, `prove = proof`); op 5 if `corrupt` | peer, round, part, idx, corrupt, proof |
| `enterRound` | yes | `SetConsensusState(H, round, proposer)` | op 8 (round same or +1, correct key) | round, peer (cached block applied, if any) |
| `addCommitment` | yes | `AddCommitment(H, round, psh)` | op 10 | round, psh |
| `prune` | yes | `Prune(H)` | no op (op 9 also moves to H+1): direct call | |
| `retryWants` | yes | `retryWants()` | op 11 | |
| `vote` | yes | consensus reactor into `peerMsgQueue` | none (consensus side) | |
| `recvSendProp`, `recvGossip`, `recvSendPart` | no | RECV continuation | | round, block |
| `consApply`, `consSendProp`, `consSendPart`, `consHandle` | no | CONS steps | | |
| `syncTakeProp`, `syncTakePart`, `syncReadRs`, `syncPush` | no | syncData steps | | |
| `delivSendProp`, `delivRecover`, `delivSendPart` | no | `deliverCachedCompactBlock` | | round |

Observables a replay can compare with the Go reactor after each environment
action and quiescence: `obs.propLog` (what the fuzz harness drains from
`proposalChan`, with `from` = peer or `self`), `prop.entries[r]` (`kind`,
identity via `block`/`psh`, `forwarded`, `catchup`, held parts =
`orig` + parity `K..K+par-1`), `prop.gossiped`, `prop.unverified`,
`prop.round`. Mapping caveats: fixture 1 (`B`) has 2 parts, not `K = 4`, so
a part-level replay of `B` needs a 4-part conflicting fixture; part indexes
in the model are canonical (abstraction 5); the mempool mask of the harness
maps to transactions, not part indexes (`MEMPOOL` = 1 means "original 0 is
rebuildable").

## Conformance (trace replay)

`consensus/propagation/mbt_itf_test.go` and `mbt_replay_test.go` replay ITF
traces of this model against the real reactor, through the fuzz harness of
`reactor_fuzz_test.go`. The header of `mbt_replay_test.go` states exactly
which observables are compared after each step and which are not.

| test | input | check |
|---|---|---|
| `TestMBTReplayFixedTraces` | `traces/fixed_*.itf.json` | conformance: every step is compared; a trace that diverges from Go's schedule fails |
| `TestMBTRevertedTraces` | `traces/{prefix,forwardOnceOnly,asyncOnly}_*.itf.json` | regression, not conformance: the fixed code forwards each (round, identity) at most once and no consensus call blocks |
| `TestMBTReplayTraceDir` | `$MBT_TRACE_DIR/*.itf.json` (skipped when unset) | conformance on fresh random traces; `make replay` generates them |

The first two run in the regular `go test`. `make replay` and `make all`
run nightly (`.github/workflows/formal-verification-nightly.yml`).

`traces/fixed_witness_*` are scenarios the random runs do not reach
(a conflicting cached compact block, a second forward after a commitment,
decoding by parity, a corrupt part on a stub, `X` forwarded through a stub).
`make traces` regenerates them from fixed seeds.

## How to run

```
make deps        # checks quint 0.34.0, installs the Rust evaluator v0.8.0 (sha256-checked)
make typecheck
make test        # scenario tests, including both counterexamples
make simulate    # random simulation of the fixed model
make verify      # TLC, exhaustive: S1, S1q, S2, S3_decision, sanity on fixedIdentity and fixedParts
make gate        # ab6a3df reverted (fully and each half): requires S1 and S2 violations
make liveness    # TLC: L1 on liveOneRound, plus the vacuity check
make all         # all of the above (about 13 min)

make verify-cap2      # verify with capacities 2 (wants a larger machine; see results)
make liveness-full    # L1 on rounds 0 and 1 (about 47 min)
make traces           # rewrites traces/
make replay           # conformance: fresh random traces and traces/ replayed against the Go reactor
```

Pinned: quint 0.34.0, Apalache 0.62.4 (its jar carries the TLC that
`quint verify --backend tlc` runs), Rust evaluator v0.8.0. `tlc.json` sets
the JVM heap (10 GB) and workers (`auto`).

## Results

Machine: 4 cores, 15 GB, TLC with 4 workers. Times are wall clock of the
make target (including quint's compilation, about 10 s per TLC run).

| target | check | coverage | result | time |
|---|---|---|---|---|
| `typecheck` | 3 files | | ok | 12 s |
| `test` | 12 scenario tests | the listed runs | all pass | 18 s |
| `simulate` | S1, S1q, S2, S3_decision, sanity on `fixedTraced` | random: 20000 runs x 60 steps | no violation | 29 s |
| `verify` | S1, S1q, S2, S3_decision, sanity on `fixedIdentity` | exhaustive: 3,404,364 states, depth 31 | hold | 144 s |
| | same on `fixedParts` | exhaustive: 7,047,792 states, depth 58 | hold | 463 s |
| `verify` total | | | | 639 s |
| `gate` | `prefix`: S1 (shortest cycle closes through `cs.rsMtx`) | BFS | violated, 9 states | 20 s |
| | `prefix`: S1q (PROTOCO-3430 cycle through `peerMsgQueue`) | BFS | violated, 10 states | 21 s |
| | `prefix`: S2 | BFS | violated, 8 states | 18 s |
| | `forwardOnceOnly`: S1 | BFS | violated | 19 s |
| | `asyncOnly`: S2 | BFS | violated | 18 s |
| `gate` total | | | | 96 s |
| `liveness` | L1 on `liveOneRound` | exhaustive: 3,201 states | holds | 18 s |
| | vacuity: L1 without fairness | | violated (expected) | 16 s |
| `verify-cap2` | `fixedIdentityCap2` (48 cores, 200 GB heap) | exhaustive: 6,454,488 states, depth 31 | hold | 40 s |
| | `fixedPartsCap2` (same machine) | exhaustive: 22,691,085 states, depth 58 | hold | 188 s |
| `liveness-full` | L1 on `live` (rounds 0, 1) | exhaustive: 7,733,064 states, depth 60 | holds | 47 min (3 workers) |

S1q = `S1_noQueueCycle`.
`S2_strictPerRound` fails by design (see Properties).

Coverage statement: S1, S2 and S3 (the forward decision) hold for
every reachable state of the two bounded configurations above (one height,
two rounds, `K = 4`, two peers), with channel capacities 1 and 2, under the
abstractions listed here. The combined configuration is only simulated. L1 holds for the honest
environment with a silent second peer.
