# celestia-core

[![Go Reference](https://img.shields.io/badge/godoc-reference-blue.svg)](https://pkg.go.dev/github.com/celestiaorg/celestia-core)
[![GitHub Release](https://img.shields.io/github/v/release/celestiaorg/celestia-core)](https://github.com/celestiaorg/celestia-core/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/celestiaorg/celestia-core)](https://goreportcard.com/report/github.com/celestiaorg/celestia-core)
[![Build](https://github.com/celestiaorg/celestia-core/actions/workflows/build.yml/badge.svg)](https://github.com/celestiaorg/celestia-core/actions/workflows/build.yml)
[![Lint](https://github.com/celestiaorg/celestia-core/actions/workflows/lint.yml/badge.svg)](https://github.com/celestiaorg/celestia-core/actions/workflows/lint.yml)
[![Tests](https://github.com/celestiaorg/celestia-core/actions/workflows/tests.yml/badge.svg)](https://github.com/celestiaorg/celestia-core/actions/workflows/tests.yml)

celestia-core is a fork of [tendermint/tendermint](https://github.com/tendermint/tendermint) with the following changes:

1. Early adoption of the ABCI++ methods: `PrepareProposal` and `ProcessProposal` because they haven't yet landed in a Tendermint release.

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
                |  +-------------------------------+  |   celestia-core (fork of Tendermint Core)
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

This repo intends on preserving the minimal possible diff with [tendermint/tendermint](https://github.com/tendermint/tendermint) to make fetching upstream changes easy. If the proposed contribution is

- **specific to Celestia**: consider if [celestia-app](https://github.com/celestiaorg/celestia-app) is a better target
- **not specific to Celestia**: consider making the contribution upstream in Tendermint

1. [Install Go](https://go.dev/doc/install) 1.17+
2. Fork this repo
3. Clone your fork
4. Find an issue to work on (see [good first issues](https://github.com/celestiaorg/celestia-core/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22)) 
5. Work on a change in a branch on your fork
6. When your change is ready, push your branch and create a PR that targets this repo

### Helpful Commands

```sh
# Build a new tendermint binary and output to build/tendermint
make build

# Install tendermint binary
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

The canonical branches in this repo are based on Tendermint releases. For example: [`v0.34.x-celestia`](https://github.com/celestiaorg/celestia-core/tree/v0.34.x-celestia) is based on the Tendermint `v0.34.x` release branch and contains Celestia-specific changes.

## Versioning

Releases are formatted: `v<CELESTIA_CORE_VERSION>-tm-v<TENDERMINT_CORE_VERSION>`
For example: [`v1.4.0-tm-v0.34.20`](https://github.com/celestiaorg/celestia-core/releases/tag/v1.4.0-tm-v0.34.20) is celestia-core version `1.4.0` based on Tendermint `0.34.20`.
`CELESTIA_CORE_VERSION` strives to adhere to [Semantic Versioning](http://semver.org/).

## Careers

We are hiring Go engineers! Join us in building the future of blockchain scaling and interoperability. [Apply here](https://jobs.lever.co/celestia).
