# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Celestia-core is a fork of CometBFT (formerly Tendermint) — a BFT consensus engine — with Celestia-specific modifications for data availability. The Go module path is `github.com/cometbft/cometbft`. It maintains minimal divergence from upstream CometBFT.

**Active branches:**
- `main`: Development for celestia-app

PRs should generally target `main` first, then backport to `v0.39.x-celestia` using the `backport-to-v0.39.x` label.

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

**Run a single test:**
```bash
go test -v -run TestName ./package/path/
```

**Run a single package's tests:**
```bash
go test -v ./consensus/...
```

**CI splits tests across parallel jobs using:**
```bash
NUM_SPLIT=4 make test-group-0  # Run test group 0 of 4
```

**Build configuration:** CGO_ENABLED=0 by default. Build tags: `cometbft` (default), `deadlock` (tests), `badgerdb`, `pebbledb`, `race`.

## Architecture

The codebase follows CometBFT's modular architecture. Key packages and their roles:

- **abci/**: Application Blockchain Interface — the boundary between the consensus engine and the application. Types are generated from `proto/tendermint/abci/`.
- **consensus/**: The core BFT state machine (propose, prevote, precommit rounds).
- **mempool/**: Transaction pool — holds unconfirmed transactions before they enter a block.
- **p2p/**: Peer-to-peer networking layer with reactor-based message routing.
- **state/**: Manages application state transitions and stores validated state.
- **store/**: Block storage and retrieval (backed by `cometbft-db`).
- **types/**: Core data types (Block, Vote, Validator, etc.) shared across packages.
- **rpc/**: JSON-RPC and gRPC server endpoints for external clients.
- **node/**: Orchestrates startup — wires together all components into a running node.
- **light/**: Light client implementation for header verification without full blocks.
- **evidence/**: Detects and manages evidence of Byzantine validator behavior.
- **proxy/**: ABCI proxy layer between consensus and the application.
- **crypto/**: Key types, signing, and hash functions.
- **libs/**: Internal utility libraries (pubsub, events, logging, async, etc.).
- **config/**: Configuration structs and TOML parsing.

**Celestia-specific additions:**
- Depends on `github.com/celestiaorg/go-square/v3` (data square utilities) and `github.com/celestiaorg/nmt` (Namespace Merkle Tree)
- Celestia ADRs in `docs/celestia-architecture/` describe block propagation, IPLD DA sampling, and data retrieval modifications

**Protobuf:** Definitions live in `proto/tendermint/`. Generated code uses `gogofaster` plugin via buf. After `make proto-gen`, ABCI types are moved to `abci/types/` and gRPC types to `rpc/grpc/`.

## Code Conventions

- Follow [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) as baseline
- Use `errors.New()` over `fmt.Errorf()` unless formatting with arguments; wrap errors with `%w`
- Use `testify/assert` and `testify/require` for test assertions; prefer table-driven tests
- Import alias conventions: `cmtcfg`, `cmttypes`, `cmtbits` etc. for internal packages; `dbm` for cometbft-db
- Separate imports into blocks: stdlib, external, application
- No anonymous dot imports
- "Save"/"Load" for persistence; "Encode"/"Decode" for parsing bytes
- Functions returning functions get `Fn` suffix
- Follow [conventional commits](https://www.conventionalcommits.org/) for PR titles

## Changelog

Every PR needs a changelog entry at `.changelog/unreleased/{category}/{issue-or-pr-number}-{description}.md` where category is one of: `improvements`, `breaking-changes`, `bug-fixes`, `features`.
