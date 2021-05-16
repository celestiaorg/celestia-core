package main

import (
	ipfsplugin "github.com/ipfs/go-ipfs/plugin"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
)

// Plugins is an exported list of plugins that will be loaded by go-ipfs.
//nolint:deadcode
var Plugins = []ipfsplugin.Plugin{
	plugin.Nmt{},
}
