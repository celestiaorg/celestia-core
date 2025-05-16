# Celestia Core

A fork of [CometBFT](https://github.com/cometbft/cometbft) with Celestia-specific modifications for use in the [Celestia network](https://celestia.org/).

[![Version][version-badge]][version-url]
[![API Reference][api-badge]][api-url]
[![Go version][go-badge]][go-url]
[![Discord chat][discord-badge]][discord-url]
[![License][license-badge]][license-url]
[![Sourcegraph][sg-badge]][sg-url]

| Branch  | Tests                                          | Linting                                     |
|---------|------------------------------------------------|---------------------------------------------|
| main    | [![Tests][tests-badge]][tests-url]             | [![Lint][lint-badge]][lint-url]             |
| v0.38.x | [![Tests][tests-badge-v038x]][tests-url-v038x] | [![Lint][lint-badge-v038x]][lint-url-v038x] |
| v0.37.x | [![Tests][tests-badge-v037x]][tests-url-v037x] | [![Lint][lint-badge-v037x]][lint-url-v037x] |
| v0.34.x | [![Tests][tests-badge-v034x]][tests-url-v034x] | [![Lint][lint-badge-v034x]][lint-url-v034x] |

## What is Celestia Core?

Celestia Core is a fork of CometBFT, which itself is a fork of Tendermint Core. It implements the CometBFT consensus algorithm with Celestia-specific modifications to support Celestia's data availability and scalability features.

Celestia is a modular consensus and data network, designed to enable anyone to easily deploy their own blockchain with minimal overhead.

## Key Modifications from CometBFT

Celestia Core includes several important modifications from the upstream CometBFT repository:

1. **Specialized Mempool**: A priority-based mempool implementation for transaction management that prioritizes transactions by assigned priority values. See [Priority Mempool documentation](./mempool/priority/README.md) for details.

2. **Data Availability Extensions**: Integration with Celestia's data availability layer, including:
   - Namespace Merkle Tree (NMT) support using `github.com/celestiaorg/nmt`
   - Integration with square data structures via `github.com/celestiaorg/go-square`
   - Support for data blob handling and verification

3. **Celestia-specific RPC Endpoints**: Additional API endpoints designed for Celestia's unique requirements.

4. **Tracing Support**: Enhanced tracing functionality specifically for Celestia nodes.

5. **Block Data Validation**: Specialized validation for Celestia's unique data structures.

## Documentation

Complete documentation for CometBFT can be found on the [CometBFT website](https://docs.cometbft.com/).

For Celestia-specific documentation, please refer to the [Celestia documentation](https://docs.celestia.org/).

## Releases

Please do not depend on `main` as your production branch. Use
[releases](https://github.com/celestiaorg/celestia-core/releases) instead.

If you intend to run Celestia Core in production, we're happy to help. To contact
us, in order of preference:

- [Join the Celestia Discord](https://discord.com/invite/YsnTPcSfWQ)
- [Create an issue on the Celestia Core repository](https://github.com/celestiaorg/celestia-core/issues)

## Security

To report a security vulnerability, please follow the security policies established by the Celestia team.

## Minimum requirements

| Version       | Requirement | Notes             |
|---------------|-------------|-------------------|
| v0.38.x-celestia | Go version  | Go 1.22 or higher |

### Install

See the original [install guide](./docs/guides/install.md) from CometBFT.

### Quick Start

- [Single node](./docs/guides/quick-start.md)
- [Local cluster using docker-compose](./docs/networks/docker-compose.md)

## Contributing

Please abide by the [Code of Conduct](CODE_OF_CONDUCT.md) in all interactions.

Before contributing to the project, please take a look at the [contributing
guidelines](CONTRIBUTING.md) and the [style guide](STYLE_GUIDE.md).

## Versioning

Celestia Core follows CometBFT's [Semantic Versioning](http://semver.org/) approach, with the addition of the `-celestia` suffix to indicate this is a fork with Celestia-specific modifications.

## Resources

### Related Projects

- [Celestia App](https://github.com/celestiaorg/celestia-app) - The Celestia blockchain application
- [Celestia Node](https://github.com/celestiaorg/celestia-node) - The Celestia light node implementation
- [Quantum Gravity Bridge](https://github.com/celestiaorg/quantum-gravity-bridge) - Bridge between Celestia and Ethereum

### Research

Below are links to research papers and resources about Celestia:

- [Celestia: A Scalable Byzantine Machine](https://arxiv.org/abs/1906.01799)
- [Block-STM: Scaling Blockchain Execution by Turning Ordering Curse to a Performance Blessing](https://arxiv.org/abs/2203.06871)
- [LazyLedger: A Distributed Data Availability Ledger With Client-Side Smart Contracts](https://arxiv.org/abs/1905.09274)

## Join us

To learn more about Celestia and get involved:

- [Website](https://celestia.org/)
- [Discord](https://discord.com/invite/YsnTPcSfWQ)
- [Twitter](https://twitter.com/CelestiaOrg)
- [Blog](https://blog.celestia.org/)

[bft]: https://en.wikipedia.org/wiki/Byzantine_fault_tolerance
[smr]: https://en.wikipedia.org/wiki/State_machine_replication
[Blockchain]: https://en.wikipedia.org/wiki/Blockchain
[version-badge]: https://img.shields.io/github/v/release/cometbft/cometbft.svg
[version-url]: https://github.com/cometbft/cometbft/releases/latest
[api-badge]: https://camo.githubusercontent.com/915b7be44ada53c290eb157634330494ebe3e30a/68747470733a2f2f676f646f632e6f72672f6769746875622e636f6d2f676f6c616e672f6764646f3f7374617475732e737667
[api-url]: https://pkg.go.dev/github.com/cometbft/cometbft
[go-badge]: https://img.shields.io/badge/go-1.22-blue.svg
[go-url]: https://github.com/moovweb/gvm
[discord-badge]: https://img.shields.io/discord/669268347736686612.svg
[discord-url]: https://discord.gg/interchain
[license-badge]: https://img.shields.io/github/license/cometbft/cometbft.svg
[license-url]: https://github.com/cometbft/cometbft/blob/main/LICENSE
[sg-badge]: https://sourcegraph.com/github.com/cometbft/cometbft/-/badge.svg
[sg-url]: https://sourcegraph.com/github.com/cometbft/cometbft?badge
[tests-url]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml
[tests-url-v038x]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml?query=branch%3Av0.38.x
[tests-url-v037x]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml?query=branch%3Av0.37.x
[tests-url-v034x]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml?query=branch%3Av0.34.x
[tests-badge]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml/badge.svg?branch=main
[tests-badge-v038x]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml/badge.svg?branch=v0.38.x
[tests-badge-v037x]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml/badge.svg?branch=v0.37.x
[tests-badge-v034x]: https://github.com/cometbft/cometbft/actions/workflows/tests.yml/badge.svg?branch=v0.34.x
[lint-badge]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml/badge.svg?branch=main
[lint-badge-v034x]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml/badge.svg?branch=v0.34.x
[lint-badge-v037x]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml/badge.svg?branch=v0.37.x
[lint-badge-v038x]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml/badge.svg?branch=v0.38.x
[lint-url]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml
[lint-url-v034x]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml?query=branch%3Av0.34.x
[lint-url-v037x]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml?query=branch%3Av0.37.x
[lint-url-v038x]: https://github.com/cometbft/cometbft/actions/workflows/lint.yml?query=branch%3Av0.38.x
[tm-core]: https://github.com/tendermint/tendermint
