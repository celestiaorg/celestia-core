# LazyLedger ipld-plugin

## Installation

Usually, a simple `make install` should build and install the plugin locally.

If you want to compile against a locally checked out version of IPFS, run:

```sh
cd plugin
./set-target.sh REPLACE_WITH_YOUR_LOCAL_PATH/go-ipfs
make install
```

Running ipfs (e.g. `ipfs init` or `ipfs daemon`) loads the plugin automatically.

Note, this plugin was only tested with IPFS [v0.7.0](https://github.com/ipfs/go-ipfs/releases/tag/v0.7.0).

