# celestia-core

celestia-core is a fork of [cometbft/cometbft](https://github.com/cometbft/cometbft), an implementation of the Tendermint protocol, with the following changes:

1. Early adoption of the ABCI++ methods: `PrepareProposal` and `ProcessProposal` because they haven't yet landed in a CometBFT release.
1. Modifications to how `DataHash` in the block header is determined. In CometBFT, `DataHash` is based on the transactions included in a block. In Celestia, block data (including transactions) are erasure coded into a data square to enable data availability sampling. In order for the header to contain a commitment to this data square, `DataHash` was modified to be the Merkle root of the row and column roots of the erasure coded data square. See [ADR 008](https://github.com/celestiaorg/celestia-core/blob/v0.34.x-celestia/docs/celestia-architecture/adr-008-updating-to-tendermint-v0.35.x.md?plain=1#L20) for the motivation or [celestia-app/pkg/da/data_availability_header.go](https://github.com/celestiaorg/celestia-app/blob/2f89956b22c4c3cfdec19b3b8601095af6f69804/pkg/da/data_availability_header.go) for the implementation. Note on the implementation: celestia-app computes the hash in prepare_proposal and returns it to CometBFT via [`blockData.Hash`](https://github.com/celestiaorg/celestia-app/blob/5bbdac2d3f46662a34b2111602b8f964d6e6fba5/app/prepare_proposal.go#L78) so a modification to celestia-core isn't strictly necessary but [comments](https://github.com/celestiaorg/celestia-core/blob/2ec23f804691afc196d0104616e6c880d4c1ca41/types/block.go#L1041-L1042) were added.
1. The GRPC server exposed by CometBFT was modified to expose Celestia-specific APIs:
    - `BlockAPI`
    - `BlobstreamAPI`
1. A [propagation reactor](./consensus/propagation/) was added to support Celestia's block propagation needs. It gossips compact blocks, tracks which proposal parts peers have or want, and helps recover missing proposal parts during consensus.


See [./docs/celestia-architecture](./docs/celestia-architecture/) for architecture decision records (ADRs) on Celestia modifications.

## Diagram

```ascii
                ^  +-------------------------------+  ^
                |  |                               |  |
                |  |  State-machine = Application  |  |
                |  |                               |  |   celestia-app (built with Cosmos SDK)
                |  |            ^      +           |  |
                |  +----------- | ABCI | ----------+  v
Celestia        |  |            +      v           |  ^
validator or    |  |                               |  |
full consensus  |  |           Consensus           |  |
node            |  |                               |  |
                |  +-------------------------------+  |   celestia-core (fork of CometBFT)
                |  |                               |  |
                |  |           Networking          |  |
                |  |                               |  |
                v  +-------------------------------+  v
```

## Install

See <https://github.com/celestiaorg/celestia-app#install>

## Usage

See <https://github.com/celestiaorg/celestia-app#usage>

## Contributing

This repo intends on preserving the minimal possible diff with [cometbft/cometbft](https://github.com/cometbft/cometbft) to make fetching upstream changes easy. If the proposed contribution is

- **specific to Celestia**: consider if [celestia-app](https://github.com/celestiaorg/celestia-app) is a better target
- **not specific to Celestia**: consider making the contribution upstream in CometBFT

1. [Install Go](https://go.dev/doc/install) 1.23.6+
2. Fork this repo
3. Clone your fork
4. Find an issue to work on (see [good first issues](https://github.com/celestiaorg/celestia-core/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22))
5. Work on a change in a branch on your fork
6. When your change is ready, push your branch and create a PR that targets this repo

PR titles follow [conventional commits](https://www.conventionalcommits.org/en/v1.0.0/) and must start the subject with `[high]`, `[medium]`, or `[low]` to indicate the review thoroughness needed. For example: `fix(mempool)!: [high] reject oversized txs` or `docs: [low] clarify setup instructions`. Don't repeat the tag in the PR description; the title already carries it.

- `[high]`: Deep review for consensus, state transitions, security, or other changes with significant risk. Examples: consensus, mempool/CAT, p2p, ABCI, state and block execution, light client, crypto, and breaking changes.
- `[medium]`: Standard review for code changes with limited impact and well-understood behavior.
- `[low]`: Focused review for straightforward docs, tests, or tooling changes with low risk.

Choose the higher level when unsure. A small diff can still need deep review. CI checks the title format; reviewers should confirm the chosen level. Dependabot uses `[high]` for Go module updates and `[medium]` for GitHub Actions updates. Mergify backport PRs reuse the original PR's title, so they keep its tag. The [required review policy](.github/review-policy.md) documents how the tier sets the number of independent approvals.

### Helpful Commands

```sh
# Build a new CometBFT binary and output to build/comet
make build

# Install CometBFT binary
make install

# Run tests
make test

# If you modified any protobuf definitions in a `*.proto` file then
# you may need to lint, format, and generate updated `*.pb.go` files
make proto-lint
make proto-format
make proto-gen
```

## Branches

`main` is the canonical branch for development. Most PRs should target `main` and optionally be backported to a release branch.

`main` is the development line for the next celestia-app major version. The next `v0.X.x` release branch is cut from `main` for it, so `main` will become `v0.43.x` for celestia-app `v11.x`. Changes intended only for an upcoming celestia-app version, such as behavior gated on a new app version, belong on `main`. Release branches only take backports of fixes that also apply to the celestia-app versions they serve.

| Branch             | Based on CometBFT | Release format          | Used by celestia-app  |
| ------------------ | ----------------- | ----------------------- | --------------------- |
| `main`             | `v0.38.x`         | `v0.43.0` (unreleased)  | `v11.x` (`main`)      |
| `v0.42.x`          | `v0.38.x`         | `v0.42.0`               | `v10.x`               |
| `v0.40.x`          | `v0.38.x`         | `v0.40.0`               | `v9.x`                |
| `v0.39.x-celestia` | `v0.38.x`         | `v0.39.0`               | `v6.x` through `v8.x` |
| `v0.38.x-celestia` | `v0.38.x`         | `v1.54.1-tm-v0.38.17`   | `v4.x` and `v5.x`     |
| `v0.34.x-celestia` | `v0.34.x`         | `v1.52.1-tm-v0.34.35`   | `v1.x` through `v3.x` |

Branches cut from `main` after `v0.39.x-celestia` have no `-celestia` suffix. Newer celestia-app versions still depend on `v0.34.x-celestia` to multiplex ABCI v1 requests, via a `github.com/tendermint/tendermint` replace directive.

Until the next release branch is cut, celestia-app `main` may temporarily pin a release from the previous branch (for example a `v0.42.x` release).

The `replace` directives in [celestia-app's `go.mod`](https://github.com/celestiaorg/celestia-app/blob/main/go.mod) are the source of truth for which celestia-core version a given celestia-app branch consumes.

## Releases

Previous releases were formatted: `v<CELESTIA_CORE_VERSION>-tm-v<COMETBFT_VERSION>`.
For example: [`v1.4.0-tm-v0.34.20`](https://github.com/celestiaorg/celestia-core/releases/tag/v1.4.0-tm-v0.34.20) is celestia-core version `1.4.0` based on CometBFT `0.34.20`. `CELESTIA_CORE_VERSION` strives to adhere to [Semantic Versioning](http://semver.org/).

Recent releases after `v0.39.0` do not include the CometBFT version.
