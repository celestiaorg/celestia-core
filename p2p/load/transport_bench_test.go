package load

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto/ed25519"
	"github.com/tendermint/tendermint/libs/log"
	cmtnet "github.com/tendermint/tendermint/libs/net"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/version"
)

var defaultProtocolVersion = p2p.NewProtocolVersion(
	version.P2PProtocol,
	version.BlockProtocol,
	0,
)

var sendTime time.Time

type TraceSetter interface {
	SetTrace(trace.Tracer)
}

func TestTransportBench(t *testing.T) {
	cfg := config.DefaultP2PConfig()

	reactor1 := NewMockReactor(defaultTestChannels, defaultMsgSizes)
	node1, err := newnode(*cfg, "test", reactor1)
	require.NoError(t, err)

	reactor2 := NewMockReactor(defaultTestChannels, defaultMsgSizes)
	node2, err := newnode(*cfg, "test", reactor2)
	require.NoError(t, err)

	err = node1.start()
	require.NoError(t, err)
	defer node1.stop()

	err = node2.start()
	require.NoError(t, err)
	defer node2.stop()
	time.Sleep(1 * time.Second) // wait for the nodes to startup

	err = node2.sw.DialPeerWithAddress(node1.addr)
	require.NoError(t, err)
	time.Sleep(1 * time.Second) // wait for the nodes to connect

	floodCount := 8

	var wg sync.WaitGroup
	for i := 0; i < floodCount; i++ {
		reactor1.FloodChannel(&wg, node2.id, time.Second*10, FirstChannel, SecondChannel, ThirdChannel, FourthChannel, FifthChannel, SixthChannel, SeventhChannel, EighthChannel)
		reactor2.FloodChannel(&wg, node1.id, time.Second*10, FirstChannel, SecondChannel, ThirdChannel, FourthChannel, FifthChannel, SixthChannel, SeventhChannel, EighthChannel)
	}

	time.Sleep(100 * time.Millisecond) // wait for the messages to start sending

	wg.Wait()

	time.Sleep(1 * time.Second) // wait for the messages to finish sending

	// VizBandwidth("test.png", reactor2.Traces)
	// VizTotalBandwidth("test2.png", reactor2.Traces)
}

/*
The next steps are to compare the rates of a prioritized channel that is also filled.

How to compare rate? We can compare the traces of the sending and receiving, that te

*/

type node struct {
	key ed25519.PrivKey
	id  p2p.ID
	// cfg    peerConfig
	p2pCfg config.P2PConfig
	addr   *p2p.NetAddress
	sw     *p2p.Switch
	mt     *p2p.MultiplexTransport
	tracer trace.Tracer
}

// newnode creates a new local peer with a random key.
func newnode(p2pCfg config.P2PConfig, chainID string, rs ...p2p.Reactor) (*node, error) {
	port, err := cmtnet.GetFreePort()
	if err != nil {
		return nil, err
	}
	p2pCfg.ListenAddress = fmt.Sprintf("tcp://localhost:%d", port)
	key := ed25519.GenPrivKey()

	id := p2p.PubKeyToID(key.PubKey())

	cfg := config.DefaultConfig()
	cfg.Instrumentation.TraceType = "local"
	cfg.Instrumentation.TracingTables = cfg.Instrumentation.TracingTables + ",transit"
	cfg.Instrumentation.TraceBufferSize = 10000

	tracer, err := trace.NewTracer(cfg, log.NewTMLogger(os.Stdout), chainID, string(id))
	if err != nil {
		return nil, err
	}

	n := &node{
		key: key,
		id:  id,
		// cfg:    cfg,
		p2pCfg: p2pCfg,
		tracer: tracer,
	}
	addr, err := p2p.NewNetAddressString(p2p.IDAddressString(n.id, p2pCfg.ListenAddress))
	if err != nil {
		return nil, err
	}
	n.addr = addr

	channelIDs := make([]byte, 0)
	for _, r := range rs {
		ch := r.GetChannels()
		for _, c := range ch {
			channelIDs = append(channelIDs, c.ID)
		}
		if setter, ok := r.(*MockReactor); ok {
			setter.SetTracer(tracer)
		}
	}

	nodeInfo := p2p.DefaultNodeInfo{
		ProtocolVersion: defaultProtocolVersion,
		ListenAddr:      p2pCfg.ListenAddress,
		DefaultNodeID:   n.id,
		Network:         "test",
		Version:         "1.2.3-rc0-deadbeef",
		Moniker:         "test",
		Channels:        channelIDs,
	}

	mt := p2p.NewMultiplexTransport(
		nodeInfo,
		p2p.NodeKey{PrivKey: key},
		tracer,
	)

	n.mt = mt

	sw := newSwitch(p2pCfg, mt, tracer, rs...)
	n.sw = sw
	return n, nil
}

func (n *node) start() error {
	err := n.mt.Listen(*n.addr)
	if err != nil {
		return err
	}

	if err := n.sw.Start(); err != nil {
		return err
	}
	return nil
}

func (n *node) stop() {
	_ = n.sw.Stop()
	_ = n.mt.Close()
	n.tracer.Stop()
}

func newSwitch(cfg config.P2PConfig, mt *p2p.MultiplexTransport, tracer trace.Tracer, rs ...p2p.Reactor) *p2p.Switch {
	sw := p2p.NewSwitch(&cfg, mt, p2p.WithTracer(tracer))
	for i, r := range rs {
		sw.AddReactor(fmt.Sprintf("reactor%d", i), r)
	}
	return sw
}
