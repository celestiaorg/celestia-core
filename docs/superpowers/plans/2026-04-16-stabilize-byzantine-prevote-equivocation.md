# Stabilize TestByzantinePrevoteEquivocation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drive `TestByzantinePrevoteEquivocation` (consensus/byzantine_test.go) to 0 flakes in 50 consecutive local runs by adding deterministic waits, replacing the bare `time.After(120s)` timeout with polling, and instrumenting the test enough to diagnose (and then fix) the underlying race.

**Architecture:** Two-phase approach. Phase 1 builds a local reproduction harness and adds structured instrumentation so the flake is reproducible and observable on a developer laptop. Phase 2 applies three concrete, independently-valuable stabilization fixes (wait-for-full-mesh, evidence-pool polling, `require.Eventually` replacement). Each phase is measured by re-running the harness and recording the flake rate, so we know whether each change actually helped.

**Tech Stack:** Go 1.24, testify, CometBFT consensus/evidence/p2p packages.

**Context files (for the executing agent):**
- `consensus/byzantine_test.go` — test under repair (lines 41–342 are `TestByzantinePrevoteEquivocation`)
- `consensus/reactor_test.go:461-532` — existing `require.Eventually` usage patterns to mirror
- `evidence/pool.go` — evidence pool API; `PendingEvidence(maxBytes)` and `Size()` are the polling hooks
- `p2p/test_util.go` — `MakeConnectedSwitches` / `Connect2Switches` behavior
- Related issue: https://github.com/celestiaorg/celestia-core/issues/2200
- Most recent failure on main: https://github.com/celestiaorg/celestia-core/actions/runs/23823601265

---

## File Structure

**Files modified:**
- `consensus/byzantine_test.go` — add instrumentation, replace `time.After` with `require.Eventually`, add a full-mesh wait helper, wait for evidence pool state before advancing.

**Files created:**
- `scripts/stress_test_flake.sh` — reusable stress harness. A one-liner wrapper is fine; it exists so every task can reproduce the measurement with identical flags.
- `.changelog/unreleased/bug-fixes/2200-fix-byzantine-prevote-equivocation-flake.md` — changelog entry (required per CLAUDE.md).

**Files not touched:** evidence pool, p2p layer, reactor code. If analysis in Task 4 points to a production bug, stop and re-plan rather than expanding scope here.

---

## Task 1: Build the stress harness

**Files:**
- Create: `scripts/stress_test_flake.sh`

- [ ] **Step 1: Write the harness script**

```bash
#!/usr/bin/env bash
# Usage: scripts/stress_test_flake.sh [COUNT] [TEST_NAME]
# Runs a single Go test N times and prints pass/fail counts.
# Exits 0 if all runs pass, 1 otherwise.

set -u

COUNT="${1:-50}"
TEST="${2:-TestByzantinePrevoteEquivocation}"
PKG="./consensus/..."
LOG_DIR="$(mktemp -d)"

echo "Running $TEST x$COUNT, logs in $LOG_DIR"

pass=0
fail=0
for i in $(seq 1 "$COUNT"); do
  log="$LOG_DIR/run-$i.log"
  if go test -tags deadlock -run "^${TEST}$" -count=1 -timeout=300s "$PKG" >"$log" 2>&1; then
    pass=$((pass+1))
    printf "."
  else
    fail=$((fail+1))
    printf "F"
  fi
done
echo
echo "Passed: $pass / $COUNT"
echo "Failed: $fail / $COUNT"
if [ "$fail" -gt 0 ]; then
  echo "First failing log: $(ls "$LOG_DIR"/run-*.log | while read f; do grep -l -E 'FAIL|panic' "$f" && break; done | head -1)"
  exit 1
fi
```

- [ ] **Step 2: Make it executable and sanity-check**

```bash
chmod +x scripts/stress_test_flake.sh
scripts/stress_test_flake.sh 1
```

Expected: one `.`, `Passed: 1 / 1` (a single run usually passes).

- [ ] **Step 3: Commit**

```bash
git add scripts/stress_test_flake.sh
git commit -m "chore(test): add stress harness for flaky test repro"
```

---

## Task 2: Measure baseline flake rate

- [ ] **Step 1: Run the harness at N=50**

```bash
scripts/stress_test_flake.sh 50 TestByzantinePrevoteEquivocation
```

Expected: some non-zero `Failed: X / 50`. Note: if the local machine passes all 50, bump to 100 and/or 200 and re-run. CI has shown ~40% fail rate under load; locally it often runs 5–10% on an idle macOS machine but is higher under CPU pressure. If you cannot reproduce at N=200, run with GOMAXPROCS=2 and a parallel `yes > /dev/null` in another terminal to simulate CI contention.

- [ ] **Step 2: Record the baseline in the plan**

Append one line to this plan file under a new `## Baseline` section at the bottom:

```
- N=50, GOMAXPROCS=<n>, pass=<p>, fail=<f>, first-failure-mode=<timeout | assertion | panic>
```

- [ ] **Step 3: Commit the baseline**

```bash
git add docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation.md
git commit -m "docs(plan): record baseline flake rate for TestByzantinePrevoteEquivocation"
```

---

## Task 3: Capture pool/store handles and add structured instrumentation

**Files:**
- Modify: `consensus/byzantine_test.go` — declare slices near line 52 (before the setup loop); populate inside the loop; use them in the block-watcher goroutines and in Task 6.

- [ ] **Step 0: Capture per-validator pool and store handles**

In the test, find the declaration block near line 52 (`css := make([]*State, nValidators)`) and add two sibling slices:

```go
css := make([]*State, nValidators)
evpools := make([]*evidence.Pool, nValidators)
blockStores := make([]*store.BlockStore, nValidators)
maxEvidenceBytes := int64(0)
```

Inside the setup loop, after `evpool, err := evidence.NewPool(...)` (around line 114), record the handle:

```go
evpools[i] = evpool
blockStores[i] = blockStore
if i == 0 {
    maxEvidenceBytes = state.ConsensusParams.Evidence.MaxBytes
}
```

Rationale: neither `State.blockExec.EvidencePool()` nor `State.blockStore` is a stable public accessor; capturing the handles at construction time avoids that dependency. `maxEvidenceBytes` is captured once because all validators share the same consensus params.

- [ ] **Step 1: Add a named log prefix and vote-delivery tracking**

Replace lines 181–211 with:

```go
bcs.doPrevote = func(height int64, round int32) {
    // allow first height to happen normally so that byzantine validator is no longer proposer
    if height == prevoteHeight {
        t.Logf("[byz] sending two conflicting prevotes at height=%d round=%d", height, round)
        prevote1, err := bcs.signVote(cmtproto.PrevoteType, bcs.rs.ProposalBlock.Hash(), bcs.rs.ProposalBlockParts.Header(), nil)
        require.NoError(t, err)
        prevote2, err := bcs.signVote(cmtproto.PrevoteType, nil, types.PartSetHeader{}, nil)
        require.NoError(t, err)
        peerList := reactors[byzantineNode].Switch.Peers().List()
        t.Logf("[byz] peer count at prevote time: %d", len(peerList))
        // send two votes to all peers (1st to one half, 2nd to another half)
        for i, peer := range peerList {
            v := prevote1
            if i >= len(peerList)/2 {
                v = prevote2
            }
            sent := peer.Send(p2p.Envelope{
                Message:   &cmtcons.Vote{Vote: v.ToProto()},
                ChannelID: VoteChannel,
            })
            t.Logf("[byz] send prevote to peer=%s hash=%X sent=%v", peer.ID(), v.BlockID.Hash, sent)
        }
    } else {
        bcs.defaultDoPrevote(height, round)
    }
}
```

Key additions vs. current code:
- `[byz]` prefix on all test log lines → grep-friendly.
- Capture `peer.Send` return value — `Send` returns `false` if the peer queue is full or the peer disconnected, which is a prime suspect for evidence never arriving.
- Log peer count at prevote time → if this is ever < 3 we have a connection race.

- [ ] **Step 2: Instrument the block-watcher goroutines**

Replace lines 299–326 with:

```go
for i := 0; i < nValidators; i++ {
    go func(i int) {
        blockCount := 0
        for msg := range blocksSubs[i].Out() {
            block := msg.Data().(types.EventDataNewBlock).Block
            blockCount++

            evCount := len(block.Evidence.Evidence)
            poolSize := evpools[i].Size()
            t.Logf("[val %d] block h=%d evidence_in_block=%d pending_pool_size=%d",
                i, block.Height, evCount, poolSize)

            if evCount != 0 {
                select {
                case done <- block.Evidence.Evidence[0]:
                default:
                }
                return
            }
            if blockCount >= 50 {
                t.Logf("[val %d] watched %d blocks without finding evidence", i, blockCount)
                return
            }
        }
    }(i)
}
```

The `evpools[i]` reference comes from the slice populated in Step 0.

- [ ] **Step 3: Run once and confirm logs render**

```bash
go test -tags deadlock -v -run '^TestByzantinePrevoteEquivocation$' -count=1 ./consensus/ 2>&1 | grep -E '\[byz\]|\[val'
```

Expected: several `[byz] send prevote...` and `[val N] block h=...` lines.

- [ ] **Step 4: Commit**

```bash
git add consensus/byzantine_test.go
git commit -m "test(consensus): add structured logging to byzantine prevote test"
```

---

## Task 4: Reproduce with instrumentation and classify the failure

- [ ] **Step 1: Run stress harness at 2x baseline N**

```bash
scripts/stress_test_flake.sh 100 TestByzantinePrevoteEquivocation
```

- [ ] **Step 2: Grep the first failing log for instrumentation output**

```bash
LOG_DIR=$(ls -dt /tmp/tmp.* | head -1)  # whatever the harness printed
grep -E '\[byz\]|\[val|FAIL|panic' "$LOG_DIR"/run-*.log | head -200 > /tmp/byzflake-traces.txt
```

- [ ] **Step 3: Classify the failure mode**

Read `/tmp/byzflake-traces.txt`. One of these patterns will be dominant. Append the classification as a `## Root Cause` section at the bottom of this plan.

| Pattern | Root cause | Fix task to prioritize |
|---|---|---|
| `peer count at prevote time: <3` | Peers not yet fully connected when byzantine node fires | Task 5 (mesh wait) |
| `send prevote ... sent=false` | Send queue full / peer churn | Task 5 (mesh wait) + possible retry |
| `pending_pool_size=0` on all validators across all blocks | Votes delivered but reactor never surfaced them to evidence pool | Out of scope — stop, open a production-code issue |
| `pending_pool_size>=1` but `evidence_in_block=0` for all 50 blocks | Proposer never includes pending evidence | Out of scope — stop, open a production-code issue |
| Timeout fires at exactly 120s with blocks still progressing | Timeout too tight / wrong polling approach | Task 6 (Eventually) |

- [ ] **Step 4: Commit classification**

```bash
git add docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation.md
git commit -m "docs(plan): classify byzantine prevote flake root cause"
```

**Important:** If the classification points to a production-code bug (rows 3 or 4 above), stop executing this plan. Open a new issue with the trace and ask the human for a new direction. Do not paper over a real evidence-pool bug with test waits.

---

## Task 5: Wait for full peer mesh before byzantine action

**Files:**
- Modify: `consensus/byzantine_test.go:170-175` (the `MakeConnectedSwitches` block)

- [ ] **Step 1: Add the full-mesh wait helper**

After the `MakeConnectedSwitches` call (around line 175), insert:

```go
// Wait for all validators to see (nValidators-1) peers. MakeConnectedSwitches
// dials synchronously but the p2p handshake and peer-registration happen
// asynchronously; without this wait, the byzantine node can reach its
// doPrevote override with a partial peer set and fire conflicting votes at
// <3 peers, which the evidence pool cannot reconstruct into DuplicateVoteEvidence.
require.Eventually(t, func() bool {
    for i := 0; i < nValidators; i++ {
        if reactors[i].Switch.Peers().Size() < nValidators-1 {
            return false
        }
    }
    return true
}, 10*time.Second, 50*time.Millisecond, "all validators should have (nValidators-1) peers before consensus starts")
```

- [ ] **Step 2: Run the full test once to confirm it still compiles and passes**

```bash
go test -tags deadlock -v -run '^TestByzantinePrevoteEquivocation$' -count=1 ./consensus/
```

Expected: PASS. If it fails with "all validators should have ... peers", the `Peers().Size()` API is wrong for this codebase — check `p2p/switch.go` for the actual method name and adjust.

- [ ] **Step 3: Measure flake rate with the mesh wait**

```bash
scripts/stress_test_flake.sh 100 TestByzantinePrevoteEquivocation
```

Append result to `## Baseline` section in this plan:

```
- After Task 5 (mesh wait): N=100, pass=<p>, fail=<f>
```

- [ ] **Step 4: Commit**

```bash
git add consensus/byzantine_test.go docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation.md
git commit -m "test(consensus): wait for full peer mesh before byzantine prevote"
```

---

## Task 6: Replace the 120s deadline with polling, including pool-state wait

**Files:**
- Modify: `consensus/byzantine_test.go:290-341` (the block-watching goroutines and the `select`/`time.After` block)

- [ ] **Step 1: Replace the select/time.After block with require.Eventually**

Delete the `done := make(chan types.Evidence, 1)` channel, the goroutine-per-validator loop, and the `select` block that ends at line 341. Replace with:

```go
pubkey, err := bcs.privValidator.GetPubKey()
require.NoError(t, err)

// First, wait for the evidence pool on any validator to contain the duplicate
// vote. This isolates "evidence was detected" from "evidence was committed"
// so that failures point at the right component.
var foundEvidence types.Evidence
require.Eventually(t, func() bool {
    for i := 0; i < nValidators; i++ {
        pending, _ := evpools[i].PendingEvidence(maxEvidenceBytes)
        for _, ev := range pending {
            if dve, ok := ev.(*types.DuplicateVoteEvidence); ok {
                if prevoteHeight == dve.Height() {
                    foundEvidence = dve
                    return true
                }
            }
        }
    }
    return false
}, 30*time.Second, 100*time.Millisecond, "evidence pool never received DuplicateVoteEvidence at height %d", prevoteHeight)

// Then wait for it to be committed in a block on any validator.
require.Eventually(t, func() bool {
    for i := 0; i < nValidators; i++ {
        for h := int64(1); h <= blockStores[i].Height(); h++ {
            b := blockStores[i].LoadBlock(h)
            if b != nil && len(b.Evidence.Evidence) > 0 {
                return true
            }
        }
    }
    return false
}, 60*time.Second, 200*time.Millisecond, "evidence was detected in pool but never committed in a block")

ev, ok := foundEvidence.(*types.DuplicateVoteEvidence)
require.True(t, ok, "Evidence should be DuplicateVoteEvidence")
assert.Equal(t, pubkey.Address(), ev.VoteA.ValidatorAddress)
assert.Equal(t, prevoteHeight, ev.Height())
t.Logf("Successfully found evidence: %v", ev)
```

Rationale for the split:
- First `Eventually` isolates "evidence detected by pool" from "evidence committed" — when this test fails in the future, the failure message will name the actual failing stage.
- 30s pool-detection timeout is generous; locally evidence pool sees the second vote in <1s once peers are connected.
- 60s commit timeout matches the 6-block look-ahead the old code had (6 × ~10s block time = 60s).
- Polls via the validators' own block stores rather than event subscriptions, removing the subscription-buffer-overflow source of flake.

- [ ] **Step 2: Remove the now-unused subscription loop**

The original test sets up `blocksSubs` via `eventBuses[i].Subscribe(...)` at line 161. Those subscriptions are no longer needed by this test. Delete the subscribe calls and the `blocksSubs` variable. Double-check `stopConsensusNet` does not require them (read `consensus/common_test.go` for the signature).

- [ ] **Step 3: Compile and run once**

```bash
go test -tags deadlock -v -run '^TestByzantinePrevoteEquivocation$' -count=1 ./consensus/
```

Expected: PASS with a `[val N] block h=...` and `Successfully found evidence` line.

- [ ] **Step 4: Commit**

```bash
git add consensus/byzantine_test.go
git commit -m "test(consensus): replace 120s deadline with Eventually polling in byzantine test"
```

---

## Task 7: Verify fix and record final flake rate

- [ ] **Step 1: Run stress harness at N=100 and N=200**

```bash
scripts/stress_test_flake.sh 100 TestByzantinePrevoteEquivocation
scripts/stress_test_flake.sh 200 TestByzantinePrevoteEquivocation
```

Target: `Failed: 0 / 200`.

- [ ] **Step 2: Run with CPU pressure**

In a second terminal: `yes > /dev/null & yes > /dev/null & yes > /dev/null &`

In the first: `scripts/stress_test_flake.sh 100 TestByzantinePrevoteEquivocation`

Then: `killall yes`

Target: `Failed: 0 / 100` even under contention. If any fail here, go back to Task 4 classification with new traces — do not ship a "passes on idle machine only" fix.

- [ ] **Step 3: Update baseline section with final numbers**

Append to `## Baseline`:

```
- Final (Task 7): N=200 idle, fail=<f>; N=100 under load, fail=<f>
```

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation.md
git commit -m "docs(plan): record post-fix flake rate for byzantine prevote test"
```

---

## Task 8: Trim instrumentation to a sustainable level

**Files:**
- Modify: `consensus/byzantine_test.go`

- [ ] **Step 1: Keep the useful instrumentation, drop the noisy bits**

Keep:
- The `[byz] peer count at prevote time` line — cheap and useful if this flakes again.
- The `[byz] send prevote ... sent=<bool>` line — catches the `peer.Send` returning false case.

Drop:
- Per-block `[val N] block h=...` line — only useful during investigation; noisy on every CI run.

Read `consensus/byzantine_test.go`, remove the per-block log line, keep the other two.

- [ ] **Step 2: Run once to confirm still passing**

```bash
go test -tags deadlock -v -run '^TestByzantinePrevoteEquivocation$' -count=1 ./consensus/
```

- [ ] **Step 3: Commit**

```bash
git add consensus/byzantine_test.go
git commit -m "test(consensus): trim byzantine prevote test instrumentation to essentials"
```

---

## Task 9: Changelog and close the tracking issue

**Files:**
- Create: `.changelog/unreleased/bug-fixes/2200-fix-byzantine-prevote-equivocation-flake.md`

- [ ] **Step 1: Write the changelog entry**

```markdown
- `[consensus]` Fix flakiness in `TestByzantinePrevoteEquivocation` by waiting for the full peer mesh before the byzantine node fires conflicting prevotes and replacing the 120s deadline with `require.Eventually` polling on evidence pool state and block store. ([\#2200](https://github.com/celestiaorg/celestia-core/issues/2200))
```

- [ ] **Step 2: Commit**

```bash
git add .changelog/unreleased/bug-fixes/2200-fix-byzantine-prevote-equivocation-flake.md
git commit -m "chore(changelog): add entry for byzantine prevote flake fix"
```

- [ ] **Step 3: Open PR per CLAUDE.md**

Use the `commit-push-pr` skill. PR description should:
- Say "Closes #2200" (and link the Linear issue if one exists — check first).
- Assign `rootulp`.
- Enable auto-merge.
- Include the before/after flake rates from the `## Baseline` section so reviewers can verify the fix landed.

---

## Baseline

_(This section is appended to by Tasks 2, 5, 7.)_

---

## Root Cause

_(This section is appended to by Task 4 after classification.)_
