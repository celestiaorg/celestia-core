# celestia-core

[![Go Reference](https://img.shields.io/badge/godoc-reference-blue.svg)](https://pkg.go.dev/github.com/celestiaorg/celestia-core)
[![GitHub Release](https://img.shields.io/github/v/release/celestiaorg/celestia-core)](https://github.com/celestiaorg/celestia-core/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/celestiaorg/celestia-core)](https://goreportcard.com/report/github.com/celestiaorg/celestia-core)
[![Build](https://github.com/celestiaorg/celestia-core/actions/workflows/build.yml/badge.svg)](https://github.com/celestiaorg/celestia-core/actions/workflows/build.yml)
[![Lint](https://github.com/celestiaorg/celestia-core/actions/workflows/lint.yml/badge.svg)](https://github.com/celestiaorg/celestia-core/actions/workflows/lint.yml)
[![Tests](https://github.com/celestiaorg/celestia-core/actions/workflows/tests.yml/badge.svg)](https://github.com/celestiaorg/celestia-core/actions/workflows/tests.yml)

celestia-core is a fork of [cometbft/cometbft](https://github.com/cometbft/cometbft), an implementation of the Tendermint protocol, with the following changes:

1. Early adoption of the ABCI++ methods: `PrepareProposal` and `ProcessProposal` because they haven't yet landed in a CometBFT release.
1. Modifications to how `DataHash` in the block header is determined. In CometBFT, `DataHash` is based on the transactions included in a block. In Celestia, block data (including transactions) are erasure coded into a data square to enable data availability sampling. In order for the header to contain a commitment to this data square, `DataHash` was modified to be the Merkle root of the row and column roots of the erasure coded data square. See [ADR 008](https://github.com/celestiaorg/celestia-core/blob/v0.34.x-celestia/docs/celestia-architecture/adr-008-updating-to-tendermint-v0.35.x.md?plain=1#L20) for the motivation or [celestia-app/pkg/da/data_availability_header.go](https://github.com/celestiaorg/celestia-app/blob/2f89956b22c4c3cfdec19b3b8601095af6f69804/pkg/da/data_availability_header.go) for the implementation. Note on the implementation: celestia-app computes the hash in prepare_proposal and returns it to CometBFT via [`blockData.Hash`](https://github.com/celestiaorg/celestia-app/blob/5bbdac2d3f46662a34b2111602b8f964d6e6fba5/app/prepare_proposal.go#L78) so a modification to celestia-core isn't strictly necessary but [comments](https://github.com/celestiaorg/celestia-core/blob/2ec23f804691afc196d0104616e6c880d4c1ca41/types/block.go#L1041-L1042) were added.


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

1. [Install Go](https://go.dev/doc/install) 1.22.6+
2. Fork this repo
3. Clone your fork
4. Find an issue to work on (see [good first issues](https://github.com/celestiaorg/celestia-core/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22))
5. Work on a change in a branch on your fork
6. When your change is ready, push your branch and create a PR that targets this repo

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

The canonical branches in this repo are based on CometBFT releases. For example: [`v0.34.x-celestia`](https://github.com/celestiaorg/celestia-core/tree/v0.34.x-celestia) is based on the CometBFT `v0.34.x` release branch and contains Celestia-specific changes.

## Versioning

Releases are formatted: `v<CELESTIA_CORE_VERSION>-tm-v<TENDERMINT_CORE_VERSION>`
For example: [`v1.4.0-tm-v0.34.20`](https://github.com/celestiaorg/celestia-core/releases/tag/v1.4.0-tm-v0.34.20) is celestia-core version `1.4.0` based on CometBFT `0.34.20`.
`CELESTIA_CORE_VERSION` strives to adhere to [Semantic Versioning](http://semver.org/).

## Careers

We are hiring Go engineers! Join us in building the future of blockchain scaling and interoperability. [Apply here](https://jobs.lever.co/celestia).
