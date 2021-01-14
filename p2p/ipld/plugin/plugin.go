package plugin

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/nmt"

	"github.com/ipfs/go-ipfs/core/coredag"
	"github.com/ipfs/go-ipfs/plugin"
	format "github.com/ipfs/go-ipld-format"
	node "github.com/ipfs/go-ipld-format"
	mh "github.com/multiformats/go-multihash"
)

// 0x77 seems to be free:
// https://github.com/multiformats/multicodec/blob/master/table.csv
const NMT = 0x77

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
	iec.AddParser("raw", "extended-square-row-or-col", DataSquareRowOrColumnRawInputParser)
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
// <share_0>| ... |<share_numOfShares>
// TODO: in case we want, we can later also encode the namespace size
// and the share size into the io.Reader. Currently, we use the same constants as defined in types (consts.go)
func DataSquareRowOrColumnRawInputParser(r io.Reader, _mhType uint64, _mhLen int) ([]node.Node, error) {
	br := bufio.NewReader(r)
	nodes := make([]node.Node, 0)
	nodeCollector := func(hash []byte, children ...[]byte) {
		cid := cidFromNamespacedSha256(hash)
		isLeaf := len(children) == 1
		if isLeaf {
			nodes = append(nodes, nmtLeafNode{
				cid:  cid,
				Data: children[0],
			})
		} else if len(children) == 2  {
			nodes = append(nodes, nmtNode{
				cid: cid,
				l:   children[0],
				r:   children[1],
			})
		} else {
			panic("expected a binary tree")
		}
	}
	nidSize := types.NamespaceSize // this could also be encoded into the Reader
	// but we don't need this to be variable
	n := nmt.New(
		sha256.New(),
		nmt.NamespaceIDSize(nidSize),
		nmt.NodeVisitor(nodeCollector),
	)
	shareSize := types.ShareSize
	for  {
		share := make([]byte, shareSize+nidSize)
		if _, err := io.ReadFull(br, share); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if err := n.Push(share[:nidSize], share[nidSize:]); err != nil {
			return nil, err
		}
	}
	_ = n.Root()
	return nodes, nil
}

func NmtNodeParser(block blocks.Block) (node.Node, error) {
	// TODO: this parser needs to differentiate between
	// leaves and inner nodes; does that mean the RawData methods need
	// to return the data that was hashed (prefixed etc)?
	panic("implement me")
}

var _ node.Node = (*nmtNode)(nil)
var _ node.Node = (*nmtLeafNode)(nil)

type nmtNode struct {
	cid  cid.Cid
	l, r []byte
}

func (n nmtNode) RawData() []byte {
	// TODO: do we need to return the preimage of the CID here?
	//fmt.Sprintf("inner-node-Data: %#v\n", append(innerPrefix, append(i.l, i.r...)...))
	// return append(innerPrefix, append(i.l, i.r...)...)
	return nil
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
		return &node.Link{Cid: cidFromNamespacedSha256(n.l)}, path[1:], nil
	case "1":
		return &node.Link{Cid: cidFromNamespacedSha256(n.r)}, path[1:], nil
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
		return nil, nil, fmt.Errorf("was not a link")
	}

	return lnk, rest, nil
}

func (n nmtNode) Copy() node.Node {
	panic("implement me")
}

func (n nmtNode) Links() []*node.Link {
	return []*node.Link{{Cid: cidFromNamespacedSha256(n.l)}, {Cid: cidFromNamespacedSha256(n.r)}}
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
	// TODO: do we need this?
	//return append(leafPrefix, l.Data...)
	return nil
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
		return nil, nil, fmt.Errorf("was not a link")
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

func cidFromNamespacedSha256(namespacedHash []byte) cid.Cid {
	buf, err := mh.Encode(namespacedHash, mh.SHA2_256)
	if err != nil {
		panic(err)
	}

	return cid.NewCidV1(NMT, mh.Multihash(buf))
}
