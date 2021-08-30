package plugin

import (
	"github.com/ipfs/go-ipfs/core/coredag"
	"github.com/ipfs/go-ipfs/plugin"
	ipld "github.com/ipfs/go-ipld-format"
)

func EnableNMT() {
	_ = RegisterBlockDecoders(ipld.DefaultBlockDecoder)
	_ = RegisterInputEncParsers(coredag.DefaultInputEncParsers)
}

// Nmt is the IPLD plugin for NMT data structure.
type Nmt struct{}

func (l Nmt) RegisterBlockDecoders(dec ipld.BlockDecoder) error {
	return RegisterBlockDecoders(dec)
}

func RegisterBlockDecoders(dec ipld.BlockDecoder) error {
	dec.Register(NmtCodec, NmtNodeParser)
	return nil
}

func (l Nmt) RegisterInputEncParsers(iec coredag.InputEncParsers) error {
	return RegisterInputEncParsers(iec)
}

func RegisterInputEncParsers(iec coredag.InputEncParsers) error {
	iec.AddParser("raw", DagParserFormatName, DataSquareRowOrColumnRawInputParser)
	return nil
}

func (l Nmt) Name() string {
	return "NMT"
}

func (l Nmt) Version() string {
	return "0.0.0"
}

func (l Nmt) Init(_ *plugin.Environment) error {
	return nil
}

var _ plugin.Plugin = &Nmt{}
