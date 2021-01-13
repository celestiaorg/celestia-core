package plugin

import (
	"github.com/ipfs/go-ipfs/core/coredag"
	"github.com/ipfs/go-ipfs/plugin"
	format "github.com/ipfs/go-ipld-format"
)

// Plugins is an exported list of plugins that will be loaded by go-ipfs.
var Plugins = []plugin.Plugin{
	&LazyLedgerPlugin{},
}

var _ plugin.PluginIPLD = &LazyLedgerPlugin{}

type LazyLedgerPlugin struct{}

func (l LazyLedgerPlugin) RegisterBlockDecoders(dec format.BlockDecoder) error {
	panic("implement me")
}

func (l LazyLedgerPlugin) RegisterInputEncParsers(iec coredag.InputEncParsers) error {
	panic("implement me")
}

func (l LazyLedgerPlugin) Name() string {
	return "LazyLedger"
}

func (l LazyLedgerPlugin) Version() string {
	return "0.0.0"
}

func (l LazyLedgerPlugin) Init(env *plugin.Environment) error {
	return nil
}
