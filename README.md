# Tendermint

![banner](docs/tendermint-core-image.jpg)

![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/celestiaorg/celestia-core)
[![license](https://img.shields.io/github/license/tendermint/tendermint.svg)](https://github.com/celestiaorg/celestia-core/blob/master/LICENSE)
[![Community](https://img.shields.io/discord/638338779505229824?color=7389D8&label=chat%20on%20discord&logo=6A7EC2)](https://discord.gg/YsnTPcSfWQ)
[![Community](https://img.shields.io/discourse/topics?label=forum&server=https%3A%2F%2Fforum.celestia.org%2F)](https://forum.celestia.org/)
[![Community](https://img.shields.io/twitter/follow/CelestiaOrg?style=social)](https://twitter.com/CelestiaOrg)

[![version](https://img.shields.io/github/tag/tendermint/tendermint.svg)](https://github.com/tendermint/tendermint/releases/latest)
[![API Reference](https://camo.githubusercontent.com/915b7be44ada53c290eb157634330494ebe3e30a/68747470733a2f2f676f646f632e6f72672f6769746875622e636f6d2f676f6c616e672f6764646f3f7374617475732e737667)](https://pkg.go.dev/github.com/tendermint/tendermint)
[![Go version](https://img.shields.io/badge/go-1.16-blue.svg)](https://github.com/moovweb/gvm)
[![Discord chat](https://img.shields.io/discord/669268347736686612.svg)](https://discord.gg/cosmosnetwork)
[![license](https://img.shields.io/github/license/tendermint/tendermint.svg)](https://github.com/tendermint/tendermint/blob/master/LICENSE)
[![tendermint/tendermint](https://tokei.rs/b1/github/tendermint/tendermint?category=lines)](https://github.com/tendermint/tendermint)
[![Sourcegraph](https://sourcegraph.com/github.com/tendermint/tendermint/-/badge.svg)](https://sourcegraph.com/github.com/tendermint/tendermint?badge)

| Branch | Tests                                                                                      | Coverage                                                                                                                             | Linting                                                                    |
|--------|--------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------|
| master | ![Tests](https://github.com/tendermint/tendermint/workflows/Tests/badge.svg?branch=master) | [![codecov](https://codecov.io/gh/tendermint/tendermint/branch/master/graph/badge.svg)](https://codecov.io/gh/tendermint/tendermint) | ![Lint](https://github.com/tendermint/tendermint/workflows/Lint/badge.svg) |

Tendermint Core is a Byzantine Fault Tolerant (BFT) middleware that takes a state transition machine - written in any programming language - and securely replicates it on many machines.

For protocol details, see [the specification](https://github.com/tendermint/spec).

For detailed analysis of the consensus protocol, including safety and liveness proofs,
see our recent paper, "[The latest gossip on BFT consensus](https://arxiv.org/abs/1807.04938)".

## Releases

Please do not depend on master as your production branch. Use [releases](https://github.com/tendermint/tendermint/releases) instead.

Tendermint has been in the production of private and public environments, most notably the blockchains of the Cosmos Network. we haven't released v1.0 yet since we are making breaking changes to the protocol and the APIs.
See below for more details about [versioning](#versioning).

In any case, if you intend to run Tendermint in production, we're happy to help. You can
contact us [over email](mailto:hello@interchain.berlin) or [join the chat](https://discord.gg/cosmosnetwork).

## Security

To report a security vulnerability, see our [bug bounty program](https://hackerone.com/cosmos).
For examples of the kinds of bugs we're looking for, see [our security policy](SECURITY.md).

We also maintain a dedicated mailing list for security updates. We will only ever use this mailing list
to notify you of vulnerabilities and fixes in Tendermint Core. You can subscribe [here](http://eepurl.com/gZ5hQD).

## Minimum requirements

| Requirement | Notes            |
|-------------|------------------|
| Go version  | Go1.16 or higher |

## Documentation

Complete documentation can be found on the [website](https://docs.tendermint.com/master/).

### Install

See the [install instructions](/docs/introduction/install.md).

### Quick Start

- [Single node](/docs/introduction/quick-start.md)
- [Local cluster using docker-compose](/docs/tools/docker-compose.md)
- [Remote cluster using Terraform and Ansible](/docs/tools/terraform-and-ansible.md)
- [Join the Cosmos testnet](https://cosmos.network/testnet)

## Contributing

Please abide by the [Code of Conduct](CODE_OF_CONDUCT.md) in all interactions.

Before contributing to the project, please take a look at the [contributing guidelines](CONTRIBUTING.md)
and the [style guide](STYLE_GUIDE.md). You may also find it helpful to read the
[specifications](https://github.com/tendermint/spec), watch the [Developer Sessions](/docs/DEV_SESSIONS.md),
and familiarize yourself with our
[Architectural Decision Records](https://github.com/tendermint/tendermint/tree/master/docs/architecture).

## Versioning

### Semantic Versioning

Tendermint uses [Semantic Versioning](http://semver.org/) to determine when and how the version changes.
According to SemVer, anything in the public API can change at any time before version 1.0.0

To provide some stability to users of 0.X.X versions of Tendermint, the MINOR version is used
to signal breaking changes across Tendermint's API. This API includes all
publicly exposed types, functions, and methods in non-internal Go packages as well as
the types and methods accessible via the Tendermint RPC interface.

Breaking changes to these public APIs will be documented in the CHANGELOG.

### Upgrades

In an effort to avoid accumulating technical debt prior to 1.0.0,
we do not guarantee that breaking changes (ie. bumps in the MINOR version)
will work with existing Tendermint blockchains. In these cases you will
have to start a new blockchain, or write something custom to get the old
data into the new chain. However, any bump in the PATCH version should be
compatible with existing blockchain histories.


For more information on upgrading, see [UPGRADING.md](./UPGRADING.md).

### Supported Versions

Because we are a small core team, we only ship patch updates, including security updates,
to the most recent minor release and the second-most recent minor release. Consequently,
we strongly recommend keeping Tendermint up-to-date. Upgrading instructions can be found
in [UPGRADING.md](./UPGRADING.md).

## Resources

### Tendermint Core

For more information on Tendermint Core and pointers to documentation for Tendermint visit
this [repository](https://github.com/tendermint/tendermint).

## Careers

We are hiring Go engineers! Join us in building the future of blockchain scaling and interoperability. [Apply here](https://angel.co/company/celestialabs/jobs).
