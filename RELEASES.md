# 🚀 CometBFT Release Process

CometBFT utilizes a modified **Semantic Versioning (SemVer)** strategy (`vX.Y.Z`). While on major version 0, **minor versions (Y)** are used to signal **breaking changes**. The `main` branch is for active development and is considered unstable.

---

## 1. Release Lines and Branch Management

Releases are built from **long-lived "backport" branches** (e.g., `v0.34.x`). Each release line has its own branch. CometBFT only actively maintains the **last two release lines** for patching and security fixes.

### 1.1 Backporting

Changes merged into `main` must be backported to supported release branches.

* **Automation:** Mergify's backport feature is used. To trigger, add the corresponding label (e.g., `S:backport-to-v0.38.x`) to the merged Pull Request (PR).
* **Conflict Resolution:** If Mergify fails, the original PR author is responsible for resolving conflicts in the newly created backport PR.

### 1.2 Creating a New Backport Branch (e.g., `v0.38.x`)

A new backport branch must be created for the first release candidate of a new minor version (e.g., `v0.38.0-rc1`).

1.  **Branch Setup:**
    * Start on `main`.
    * Create a branch protection rule on GitHub for the new branch.
    * Create and push the branch: `git checkout -b v0.38.x && git push origin v0.38.x`
2.  **Documentation Update PR:** Open a PR against the new backport branch to update all internal documentation links from `main` to `v0.38.x` (or `v0.38` for docs sites), **excluding** `README.md`, `CHANGELOG.md`, and `UPGRADING.md`.
    * **Exception:** The LICENSE link must **always** point to `main`.
3.  **Version Files:** Ensure the RPC docs' `version` field in `rpc/openapi/openapi.yaml` is updated.
4.  **Documentation Repo:** Update the `VERSIONS` file in the separate `cometbft-docs` repository.
5.  **Post-Creation `main` Updates:** Return to `main` to update CI/CD:
    * Create a new e2e nightly workflow for the branch.
    * Update `.github/mergify.yml` and `.github/dependabot.yml` to enable automation for the new branch.
    * **Crucial Go Mod Step:** Tag `main` with a "dev" tag greater than the backport branch tags (e.g., `v0.39.0-dev`) to ensure `go mod` correctly orders commits on `main` relative to the new release line.

---

## 2. Release Types

### 2.1 Pre-releases (Alpha, Beta, RC)

Pre-releases are built off the backport branch to allow early testing.

| Type | Example Tag | Stability | Purpose |
| :--- | :--- | :--- | :--- |
| **Alpha** | `v0.38.0-alpha.1` | Most Unstable | Early integration testing before proper QA. |
| **Beta** | `v0.38.0-beta.1` | More Stable | QA is better; minor breaking API changes possible if highly requested. |
| **Release Candidate (RC)** | `v0.38.0-rc1` | Highly Stable | QA complete; APIs are stable. Used for final bug fixes before official release. |

**Pre-release Steps:**

1.  Start on the backport branch (`v0.38.x`).
2.  Run all integration and E2E nightly tests.
3.  **Prepare Documentation & Versioning:**
    * Build `CHANGELOG.md` using `unclog` *without* creating an official release, ensuring entries remain under "Unreleased."
    * Update `UPGRADING.md` with notes on breaking changes.
    * Bump `TMVersionDefault` and potentially `P2P`, `Block`, and `ABCI` protocol versions in `version.go`.
4.  Open and merge a PR with these changes against the backport branch.
5.  Create and push the tag locally:
    ```sh
    git tag -a v0.38.0-rc1 -m "Release Candidate v0.38.0-rc1"
    git push origin v0.38.0-rc1
    ```

### 2.2 Minor Release (e.g., `v0.38.0`)

This assumes the release was preceded by RCs and the **Minor Release Checklist** (QA requirements) has been completed.

1.  Start on the backport branch (`v0.38.x`).
2.  Run all tests.
3.  **Finalize Release:**
    * Perform an official release using `unclog` (this finalizes the changelog).
    * Ensure `UPGRADING.md` is complete.
    * Bump all necessary protocol versions in `version.go`.
4.  Open and merge a PR with these finalized changes against the backport branch.
5.  Create and push the final tag:
    ```sh
    git tag -a v0.38.0 -m 'Release v0.38.0'
    git push origin v0.38.0
    ```
6.  Ensure `main` is updated with the latest `CHANGELOG.md`, `CHANGELOG_PENDING.md`, and `UPGRADING.md` files.

### 2.3 Patch Release (e.g., `v0.38.1`)

Patch releases are built directly off the long-lived backport branch and typically do not require RCs.

1.  Checkout the backport branch (`git checkout v0.38.x`).
2.  Run tests.
3.  **Prepare Patch:**
    * Perform an official release using `unclog`.
    * Bump `TMDefaultVersion` and potentially **`ABCI` protocol version** (only field additions are valid patch changes for ABCI).
4.  Open and merge a PR with these changes against `v0.38.x`.
5.  Create and push the patch tag locally.
6.  Create a separate PR back to `main` with only the `CHANGELOG` and version changes. **Do not merge the backport branch into main.**

---

## 3. Minor Release Quality Assurance and Testing

Before a minor version release, the software undergoes a strict quality assurance process to ensure stability.

### 3.1 Feature Freeze

For at least two weeks following the creation of the backport branch, the code enters a **Feature Freeze**. During this time, **no new features, dependency upgrades, or unrelated refactors** are merged. Focus shifts entirely to fixing broken or flaky tests, diagnosing E2E failures, and ensuring stability.

### 3.2 Testing Requirements

* **Nightly End-To-End Tests:** Automated tests run nightly on supported branches, simulating network stability/instability with containerized nodes. These must pass consistently.
* **Upgrade Harness:** Tests are run to verify the seamless upgrade workflow (stopping old version, starting new version) under various stop conditions.
* **Large Scale Testnets:** Used to "shake out emergent behaviors at scale," running on intentionally low-powered VMs to simulate production resource constraints.

| Testnet Type | Size & Purpose | Key Metrics Monitored |
| :--- | :--- | :--- |
| **200 Node Testnet** | 5 Seeds, 175 Validators, 20 Full Nodes. Runs for several days to test stability/performance in a typical scenario. | Consensus rounds, Peer connections, Memory/CPU utilization, Block production/minute. |
| **Rotating Node Testnet** | 10 Validators, 3 Seeds, and a rolling set of 25 Full Nodes constantly connecting and exiting. Tests network resilience to churn. | Connection rate, blocksync stability. |
| **Vote-extension Testnet** | 175 Validators. Configured with varying sizes of vote-extensions to measure the performance impact of this feature. | Latency, Seconds per consensus step (Propose, Precommit, etc.). |
| **Network Partition Testnet** | 100 Validators (equal stake). Tests the ability to stop block production when partitioned (50/50 stake split) and reliably recover once the partition is resolved. | Time to stop/resume block production. |
| **Absent Stake Testnet** | 150 Validators (67% total stake) run alongside 33% offline stake. Tests long-term stability when a significant portion of voting power is permanently offline. | Block production reliability over multiple days. |
