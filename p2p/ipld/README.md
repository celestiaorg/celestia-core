# LazyLedger ipld-plugin

## Installation

Usually, a simple `make install` should build and install the plugin locally.

If you want to compile against a locally checked out version of IPFS, change the `replace` directive in [go.mod](./plugin/go.mod).

Running ipfs (e.g. `ipfs init` or `ipfs daemon`) loads the plugin automatically.

Note, this plugin was only tested with IPFS [v0.7.0](https://github.com/ipfs/go-ipfs/releases/tag/v0.7.0).
