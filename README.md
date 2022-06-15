# Celestia Core

<!-- markdownlint-disable -->
<img src="docs/celestia-logo.png">
<!-- markdownlint-enable -->

![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/celestiaorg/celestia-core)
[![Community](https://img.shields.io/badge/chat%20on-discord-orange?&logo=discord&logoColor=ffffff&color=7389D8&labelColor=6A7EC2)](https://discord.gg/YsnTPcSfWQ)
[![license](https://img.shields.io/github/license/tendermint/tendermint.svg)](https://github.com/celestiaorg/celestia-core/blob/v0.35.x-celestia/LICENSE)

Celestia Core will power the Celestia main chain by leveraging Tendermint.

Celestia itself is a scale-out data availability-focused minimal blockchain.
It allows users to post arbitrary data on the chain, as well as define their own execution layers.
This data is ordered on-chain but not executed. This allows for the first scalable data layer for
decentralised applications, including optimistic rollup sidechains. Additionally, this design allows developers to
define their own execution environments.

Read this [blog post](https://blog.celestia.org/celestia-a-scalable-general-purpose-data-availability-layer-for-decentralized-apps-and-trust-minimized-sidechains/)
to learn more about what we are building.

## Documentation

The original [whitepaper](https://arxiv.org/abs/1905.09274) and the
[specification](https://github.com/celestiaorg/celestia-specs) which we are currently wrapping up can give you
a more detailed overview what to expect from this repository.

### Minimum requirements

| Requirement | Notes            |
|-------------|------------------|
| Go version  | Go1.17 or higher |

## Contributing

Before contributing to the project, please take a look at the [contributing guidelines](CONTRIBUTING.md)
and the [style guide](STYLE_GUIDE.md).

Join the community at [Telegram](https://t.me/CelestiaCommunity) or jump onto the [Forum](https://forum.celestia.org/)
to get more involved into discussions.

Learn more by reading the code and the
[specifications](https://github.com/celestiaorg/celestia-specs).

## Versioning

### Semantic Versioning

Celestia Core uses [Semantic Versioning](http://semver.org/) to determine when and how the version changes.
According to SemVer, anything in the public API can change at any time before version 1.0.0

## Resources

### Celestia (formerly LazyLedger)

- [Ethereum research post](https://ethresear.ch/t/a-data-availability-blockchain-with-sub-linear-full-block-validation/5503)
- [Academic paper](https://arxiv.org/abs/1905.09274)
- [Blog](https://blog.celestia.org)
- [Project web site](https://celestia.org)
- [Academic prototype](https://github.com/celestiaorg/lazyledger-prototype)
- [Follow Celestia on Twitter](https://twitter.com/CelestiaOrg)

### Tendermint Core

For more information on Tendermint Core and pointers to documentation for Tendermint visit
this [repository](https://github.com/tendermint/tendermint).

## Careers

We are hiring Go engineers! Join us in building the future of blockchain scaling and interoperability. [Apply here](https://jobs.lever.co/celestia).
