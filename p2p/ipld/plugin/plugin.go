package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/lazyledger/nmt"

	"github.com/ipfs/go-ipfs/core/coredag"
	"github.com/ipfs/go-ipfs/plugin"
	format "github.com/ipfs/go-ipld-format"
	node "github.com/ipfs/go-ipld-format"
	mh "github.com/multiformats/go-multihash"
)

const (
	// NMT is the codec used for this plugin.
	// 0x77 seems to be free:
	// https://github.com/multiformats/multicodec/blob/master/table.csv
	NMT = 0x1077
	// DagParserFormatName can be used when putting into the IPLD Dag
	DagParserFormatName = "extended-square-row-or-col"

	// FIXME: These are the same as types.ShareSize and types.NamespaceSize.
	// Repeated here to avoid a dependency to the wrapping repo as this makes
	// it hard to compile and use the plugin against a local ipfs version.
	namespaceSize = 8
	shareSize     = 256
)

// Plugins is an exported list of plugins that will be loaded by go-ipfs.
var Plugins = []plugin.Plugin{
	&LazyLedgerPlugin{},
}

var _ plugin.PluginIPLD = &LazyLedgerPlugin{}

type LazyLedgerPlugin struct{}

func (l LazyLedgerPlugin) RegisterBlockDecoders(dec format.BlockDecoder) error {
	dec.Register(NMT, NmtNodeParser)
	return nil
}

func (l LazyLedgerPlugin) RegisterInputEncParsers(iec coredag.InputEncParsers) error {
	iec.AddParser("raw", DagParserFormatName, DataSquareRowOrColumnRawInputParser)
	return nil
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

// DataSquareRowOrColumnRawInputParser reads the raw shares and extract the IPLD nodes from the NMT tree.
// Note, to parse without any error the input has to be of the form:
//
// <share_0>| ... |<share_numOfShares>
//
// To determine the share and the namespace size the constants
// types.ShareSize and types.NamespaceSize are used.
func DataSquareRowOrColumnRawInputParser(r io.Reader, _mhType uint64, _mhLen int) ([]node.Node, error) {
	const extendedSquareSize = 256
	br := bufio.NewReader(r)
	nodes := make([]node.Node, 0, extendedSquareSize)
	nodeCollector := func(hash []byte, children ...[]byte) {
		cid, err := cidFromNamespacedSha256(hash)
		if err != nil {
			panic(fmt.Sprintf("nmt lib created a malformed hash: %s", err))
		}
		switch len(children) {
		case 1:
			prependNode(nmtLeafNode{
				cid:  cid,
				Data: children[0],
			}, &nodes)
		case 2:
			prependNode(nmtNode{
				cid: cid,
				l:   children[0],
				r:   children[1],
			}, &nodes)
		default:
			panic("expected a binary tree")
		}
	}
	n := nmt.New(
		sha256.New(),
		nmt.NamespaceIDSize(namespaceSize),
		nmt.NodeVisitor(nodeCollector),
	)
	for {
		share := make([]byte, shareSize+namespaceSize)
		if _, err := io.ReadFull(br, share); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if err := n.Push(share[:namespaceSize], share[namespaceSize:]); err != nil {
			return nil, err
		}
	}
	// to trigger the collection of nodes:
	_ = n.Root()
	return nodes, nil
}

func prependNode(newNode node.Node, nodes *[]node.Node) {
	*nodes = append(*nodes, node.Node(nil))
	copy((*nodes)[1:], *nodes)
	(*nodes)[0] = newNode
}

func NmtNodeParser(block blocks.Block) (node.Node, error) {
	const (
		// length of the domain separator for leaf and inner nodes:
		prefixOffset = 1
		// nmtHashSize = flagSize+sha256.Size
		nmtHashSize = namespaceSize*2 + sha256.Size
	)
	var (
		leafPrefix  = []byte{nmt.LeafPrefix}
		innerPrefix = []byte{nmt.NodePrefix}
	)
	data := block.RawData()
	if len(data) == 0 {
		return &nmtLeafNode{
			cid:  block.Cid(),
			Data: nil,
		}, nil
	}
	domainSeparator := data[:prefixOffset]
	if bytes.Equal(domainSeparator, leafPrefix) {
		return &nmtLeafNode{
			cid:  block.Cid(),
			Data: data[prefixOffset:],
		}, nil
	}
	if bytes.Equal(domainSeparator, innerPrefix) {
		return nmtNode{
			cid: block.Cid(),
			l:   data[prefixOffset : prefixOffset+nmtHashSize],
			r:   data[prefixOffset+nmtHashSize:],
		}, nil
	}
	return nil, fmt.Errorf(
		"expected first byte of block to be either the leaf or inner node prefix: (%x, %x), got: %x)",
		leafPrefix,
		innerPrefix,
		domainSeparator,
	)
}

var _ node.Node = (*nmtNode)(nil)
var _ node.Node = (*nmtLeafNode)(nil)

type nmtNode struct {
	// TODO(ismail): we might want to export these later
	cid  cid.Cid
	l, r []byte
}

func (n nmtNode) RawData() []byte {
	return append([]byte{nmt.NodePrefix}, append(n.l, n.r...)...)
}

func (n nmtNode) Cid() cid.Cid {
	return n.cid
}

func (n nmtNode) String() string {
	return fmt.Sprintf(`
node {
	hash: %x,
	l: %x,
	r: %x"
}`, n.cid.Hash(), n.l, n.r)
}

func (n nmtNode) Loggable() map[string]interface{} {
	return nil
}

func (n nmtNode) Resolve(path []string) (interface{}, []string, error) {
	switch path[0] {
	case "0":
		left, err := cidFromNamespacedSha256(n.l)
		if err != nil {
			return nil, nil, err
		}
		return &node.Link{Cid: left}, path[1:], nil
	case "1":
		right, err := cidFromNamespacedSha256(n.r)
		if err != nil {
			return nil, nil, err
		}
		return &node.Link{Cid: right}, path[1:], nil
	default:
		return nil, nil, errors.New("invalid path for inner node")
	}
}

func (n nmtNode) Tree(path string, depth int) []string {
	if path != "" || depth == 0 {
		return nil
	}

	return []string{
		"0",
		"1",
	}
}

func (n nmtNode) ResolveLink(path []string) (*node.Link, []string, error) {
	obj, rest, err := n.Resolve(path)
	if err != nil {
		return nil, nil, err
	}

	lnk, ok := obj.(*node.Link)
	if !ok {
		return nil, nil, errors.New("was not a link")
	}

	return lnk, rest, nil
}

func (n nmtNode) Copy() node.Node {
	panic("implement me")
}

func (n nmtNode) Links() []*node.Link {
	leftCid, err := cidFromNamespacedSha256(n.l)
	if err != nil {
		panic(fmt.Errorf("malformed nmtNode, cidFromNamespacedSha256(): %w", err))
	}
	rightCid, err := cidFromNamespacedSha256(n.r)
	if err != nil {
		panic(fmt.Errorf("malformed nmtNode, cidFromNamespacedSha256(): %w", err))
	}
	return []*node.Link{{Cid: leftCid}, {Cid: rightCid}}
}

func (n nmtNode) Stat() (*node.NodeStat, error) {
	return &node.NodeStat{}, nil
}

func (n nmtNode) Size() (uint64, error) {
	return 0, nil
}

type nmtLeafNode struct {
	cid  cid.Cid
	Data []byte
}

func (l nmtLeafNode) RawData() []byte {
	return append([]byte{nmt.LeafPrefix}, l.Data...)
}

func (l nmtLeafNode) Cid() cid.Cid {
	return l.cid
}

func (l nmtLeafNode) String() string {
	return fmt.Sprintf(`
leaf {
	hash: 		%x,
	len(Data): 	%v
}`, l.cid.Hash(), len(l.Data))
}

func (l nmtLeafNode) Loggable() map[string]interface{} {
	return nil
}

func (l nmtLeafNode) Resolve(path []string) (interface{}, []string, error) {
	if path[0] == "Data" {
		// TODO: store the data separately
		// currently nmtLeafNode{Data:} contains the actual data
		// instead, there should be a link in the leaf to the actual data
		return nil, nil, nil
	} else {
		return nil, nil, errors.New("invalid path for leaf node")
	}
}

func (l nmtLeafNode) Tree(path string, depth int) []string {
	if path != "" || depth == 0 {
		return nil
	}

	return []string{
		"Data",
	}
}

func (l nmtLeafNode) ResolveLink(path []string) (*node.Link, []string, error) {
	obj, rest, err := l.Resolve(path)
	if err != nil {
		return nil, nil, err
	}

	lnk, ok := obj.(*node.Link)
	if !ok {
		return nil, nil, errors.New("was not a link")
	}
	return lnk, rest, nil
}

func (l nmtLeafNode) Copy() node.Node {
	panic("implement me")
}

func (l nmtLeafNode) Links() []*node.Link {
	return []*node.Link{{Cid: l.Cid()}}
}

func (l nmtLeafNode) Stat() (*node.NodeStat, error) {
	return &node.NodeStat{}, nil
}

func (l nmtLeafNode) Size() (uint64, error) {
	return 0, nil
}

func cidFromNamespacedSha256(namespacedHash []byte) (cid.Cid, error) {
	if got, want := len(namespacedHash), 2*namespaceSize+sha256.Size; got != want {
		return cid.Cid{}, fmt.Errorf("invalid namespaced hash lenght, got: %v, want: %v", got, want)
	}
	buf, err := mh.Encode(namespacedHash, mh.SHA2_256)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(NMT, mh.Multihash(buf)), nil
}
