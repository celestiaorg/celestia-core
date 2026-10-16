# CLAUDE.md

## Project Overview

Celestia-core is a fork of CometBFT (formerly Tendermint) — a BFT consensus engine — with Celestia-specific modifications for data availability. The Go module path is `github.com/cometbft/cometbft`. It maintains minimal divergence from upstream CometBFT.

**Active branches:**
- `main`: Development for celestia-app
- `v0.42.x`: Current release line, used by celestia-app `v10.x` (no `-celestia` suffix)
- `v0.40.x`: Used by celestia-app `v9.x` (no `-celestia` suffix)
- `v0.39.x-celestia`: Used by celestia-app `v6.x` through `v8.x`

PRs should generally target `main` first, then be backported to the release branches the fix applies to. Mergify handles this via labels: `backport-to-v0.42.x`, `backport-to-v0.40.x`, `backport-to-v0.39.x`, `backport-to-v0.38.x`, `backport-to-v0.34.x`. See `.github/mergify.yml` for the full list.

Note that `main`, `v0.42.x`, and `v0.40.x` have diverged from the older release branches in ways that decide whether a backport is even possible — for example the CAT-only mempool (no `clist`) and the `RecvMessagePrecheck` p2p hook exist on `main`, `v0.42.x`, and `v0.40.x` but not on `v0.39.x-celestia`. Check that the code path exists on the target branch before labeling.

## Build Commands

```bash
make build                # Build binary to build/cometbft
make install              # Install to GOBIN
make test                 # Run all unit tests (go test -p 1 -tags deadlock)
make test_race            # Run tests with race detector
make test_cover           # Run tests with coverage
make test_integrations    # Full integration test suite (requires Docker)
make lint                 # Run golangci-lint
make format               # Run gofmt + goimports
make vulncheck            # Run govulncheck
make lint-typo            # Run codespell for typo checking
make proto-gen            # Generate Go code from .proto files
make proto-lint           # Lint protobuf files
make proto-format         # Format protobuf files (requires clang-format)
make mockery              # Generate test mocks
make metrics              # Generate metrics code
```

**Build configuration:** CGO_ENABLED=0 by default. Build tags: `cometbft` (default), `deadlock` (tests), `race`. PebbleDB is the only supported DB backend (since cometbft-db v1.0.4, no build tag needed).

## Code Generation

Celestia-specific ADRs live in `docs/celestia-architecture/`.

**Protobuf:** Definitions live in `proto/tendermint/`. Generated code uses `gogofaster` plugin via buf. After `make proto-gen`, ABCI types are moved to `abci/types/` and gRPC types to `rpc/grpc/`.

## Code Conventions

- Follow [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) as baseline
- Import alias conventions: `cmtcfg`, `cmttypes`, `cmtbits` etc. for internal packages; `dbm` for cometbft-db
- "Save"/"Load" for persistence; "Encode"/"Decode" for parsing bytes
- Functions returning functions get `Fn` suffix
- Follow [conventional commits](https://www.conventionalcommits.org/) for PR titles

## Pull Requests

Keep PR descriptions short. Add a "Note for reviewers" for consensus-affecting, breaking, or migration-required changes.

## Changelog

Do not create `CHANGELOG.md` or `.changelog/` entries; use the commit log and GitHub release notes. This overrides older upstream guidance in CONTRIBUTING.md or other docs. Delete changelog files encountered in a branch or PR rather than updating them.

## Security PRs

- For PRs that resolve Hacken bug bounty reports, do not include details about the bug in the PR description. Instead, link to a Linear issue that contains more details on the bug and the link to the Hacken bug bounty report.
