package main

import (
	"github.com/ipfs/go-ipfs/plugin"

	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
)

// Plugins is an exported list of plugins that will be loaded by go-ipfs.
//nolint:deadcode
var Plugins = []plugin.Plugin{
	&nodes.LazyLedgerPlugin{},
}
