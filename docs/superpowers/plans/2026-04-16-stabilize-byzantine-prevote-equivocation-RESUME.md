# Resume Instructions — Stabilize TestByzantinePrevoteEquivocation

> This file is a handoff document. Read it, then paste the "Resume Prompt" section into a fresh Claude Code session to continue the plan.

## What this work is

Stabilize `TestByzantinePrevoteEquivocation` in `consensus/byzantine_test.go` — the most frequent flake in celestia-core CI (8 of the last 19 failed `Test` workflow runs on main between 2026-02-09 and 2026-04-15). Tracked in issue [#2200](https://github.com/celestiaorg/celestia-core/issues/2200).

The full plan is at `docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation.md`. Read that before resuming.

## Why this was paused

The plan's Tasks 2, 4, 5, 7 run a stress harness (`scripts/stress_test_flake.sh`) that executes the flaky Go test 50–200 times. With a 10% local flake rate and ~120s per flaky run, each invocation of the harness takes ~12 min; Task 7 alone runs it 3 times (N=200 idle, N=100 under CPU pressure). Total local CPU budget across the plan is ~1–2 hours. The user requested moving execution to a server so it doesn't lock up their laptop.

## Git state at handoff

- **Repo:** `celestiaorg/celestia-core`
- **Branch:** `worktree-lazy-wobbling-steele`
- **HEAD:** `cc7b02a94` (`chore(test): harden stress harness shell-safety`)
- **Base branch:** `main`
- **Branch is not yet pushed to origin.** Push it before cloning on the server. From the worktree root:
  ```bash
  git push -u origin worktree-lazy-wobbling-steele
  ```

Commits on this branch (newest first):
1. `cc7b02a94` — harden stress harness shell-safety
2. `9b34c1cac` — add stress harness for flaky test repro
3. `84c850e51` — add plan doc

## Task status

| # | Task | Status |
|---|---|---|
| 1 | Build stress harness (`scripts/stress_test_flake.sh`) | ✅ DONE — spec + quality reviewed, shell-safety hardened |
| 2 | Measure baseline flake rate | ⏭️ Next |
| 3 | Capture pool/store handles + structured instrumentation | ⏳ Pending |
| 4 | Reproduce with instrumentation and classify root cause | ⏳ Pending |
| 5 | Wait for full peer mesh before byzantine action | ⏳ Pending |
| 6 | Replace 120s deadline with `require.Eventually` polling | ⏳ Pending |
| 7 | Verify fix and record final flake rate | ⏳ Pending |
| 8 | Trim instrumentation to sustainable level | ⏳ Pending |
| 9 | Changelog and PR | ⏳ Pending |

## Notes and context the resuming agent should know

**Execution approach in progress:** subagent-driven-development (fresh implementer per task + spec review + code-quality review). Use the same skill on resume: `superpowers:subagent-driven-development`.

**Known issues discovered during Task 1 code review** (already addressed in `cc7b02a94`, but good context for future tasks):
- Original plan's harness used `while read f` inside a subshell — replaced with a safer `for f in ... *.log` loop plus a fallback message when no log matches the `FAIL|panic` grep.
- `mktemp -d` is now guarded.
- The harness does NOT use `set -e` (intentional — the loop must tolerate individual test failures).

**Stress-harness wall-clock estimate on a beefy Linux server:**
- A single passing run of `TestByzantinePrevoteEquivocation` ≈ 3–5 s.
- A flaking run ≈ 60–120 s (hits the test's internal timeout).
- If server flake rate mirrors CI (~40%), N=50 ≈ 25 × 5 + 25 × 120 ≈ 52 min. If the server is CPU-rich and flakes less, much faster.
- Budget at least 2 h for the full plan, possibly 3 h if Task 7's CPU-pressure step reproduces the flake.

**Task 4 has a stop-and-escalate rule.** If the trace classification points to a production-code bug (evidence pool not accepting the second vote, or proposer not including pending evidence), the resuming agent must stop the plan and report back rather than papering over it with test waits.

**Per-CLAUDE.md constraints for the final PR (Task 9):**
- Assign `rootulp`.
- Enable auto-merge.
- PR description should say "Closes #2200" (and link a Linear issue if one exists — search first).
- Add a changelog entry at `.changelog/unreleased/bug-fixes/2200-fix-byzantine-prevote-equivocation-flake.md`.

## How to set up on the server

1. Clone the repo and check out the branch:
   ```bash
   # On local machine first, if not yet pushed:
   git push -u origin worktree-lazy-wobbling-steele

   # On the server:
   git clone https://github.com/celestiaorg/celestia-core.git
   cd celestia-core
   git fetch origin worktree-lazy-wobbling-steele
   git checkout worktree-lazy-wobbling-steele
   ```
2. Install Go 1.24+ (or whatever `go.mod` specifies) and verify:
   ```bash
   go version
   go build ./...   # warm the build cache so stress runs don't include compile time
   ```
3. Quick sanity run to confirm the harness works and produces expected output:
   ```bash
   scripts/stress_test_flake.sh 1
   # Expected: one `.`, `Passed: 1 / 1` (occasionally `F`, `Failed: 1 / 1` — that's the flake).
   ```
4. Start a Claude Code session in the repo root. Paste the "Resume Prompt" below as the first message.

## Resume Prompt

> Copy everything between the lines below into the new Claude Code session.

---

I'm resuming an in-progress plan to fix the most frequent flake in celestia-core CI (`TestByzantinePrevoteEquivocation`).

**Working directory:** This repo (celestiaorg/celestia-core), branch `worktree-lazy-wobbling-steele`, HEAD should be `cc7b02a94` (`chore(test): harden stress harness shell-safety`). Verify with `git log -1` before starting.

**Plan file:** `docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation.md`.
**Handoff doc:** `docs/superpowers/plans/2026-04-16-stabilize-byzantine-prevote-equivocation-RESUME.md` (this file — read it for context, known issues, and task statuses).

**Task 1 is DONE.** Resume at **Task 2**.

Continue using `superpowers:subagent-driven-development`: one implementer subagent per task, then spec-compliance review, then code-quality review, then mark complete. Do NOT re-do Task 1. Before Task 2, run `scripts/stress_test_flake.sh 1` as a sanity check that the harness still works on this machine.

**Stop-and-escalate rule for Task 4:** If the log classification points at a production-code bug (evidence pool not storing the second vote, or proposer never including pending evidence), stop the plan and report back to me — do not paper over a production bug with test waits.

**For Task 9 (PR):** follow CLAUDE.md — assign rootulp, enable auto-merge, say "Closes #2200" in the description, add a changelog entry.

Start by reading the plan file and confirming git state, then proceed to Task 2.
