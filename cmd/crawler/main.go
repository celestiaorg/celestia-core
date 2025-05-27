package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/service"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/conn"
	"github.com/tendermint/tendermint/p2p/pex"
	"github.com/tendermint/tendermint/pkg/trace"
)

const (
	// Maximum number of peers to connect to
	maxPeers = 1000

	// How often to print peer statistics
	statsInterval = 10 * time.Second

	// Seed nodes to bootstrap from
	seedNodes = "19b3ef846732c465cc32da3eaddb9c0fb41f57d2@consensus-full-seed-1.celestia-bootstrap.net:26656,cf7ac8b19ff56a9d47c75551bd4864883d1e24b5@consensus-full-seed-2.celestia-bootstrap.net:26656"
)

type Crawler struct {
	service.BaseService

	sw        *p2p.Switch
	pexR      *pex.Reactor
	addrBook  pex.AddrBook
	connected sync.Map // map[p2p.ID]*peerInfo
	conns     sync.Map
}

type peerInfo struct {
	addr      *p2p.NetAddress
	startTime time.Time
}

// CrawlerReactor is a custom reactor that handles peer disconnections
type CrawlerReactor struct {
	p2p.BaseReactor
	crawler *Crawler
}

func NewCrawlerReactor(crawler *Crawler) *CrawlerReactor {
	r := &CrawlerReactor{
		crawler: crawler,
	}
	r.BaseReactor = *p2p.NewBaseReactor("CrawlerReactor", r)
	return r
}

func (r *CrawlerReactor) GetChannels() []*conn.ChannelDescriptor {
	return []*conn.ChannelDescriptor{}
}

func (r *CrawlerReactor) AddPeer(peer p2p.Peer) {
	// Peer is already handled by the peer filter
}

func (r *CrawlerReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	r.crawler.Logger.Info("Peer disconnected",
		"id", peer.ID(),
		"addr", peer.RemoteAddr(),
		"reason", reason,
	)

	// Remove from connected peers
	r.crawler.connected.Delete(peer.ID())
}

func (r *CrawlerReactor) Receive(chID byte, peer p2p.Peer, msgBytes []byte) {
	// We don't need to handle any messages
}

func NewCrawler() (*Crawler, error) {
	// Create P2P config
	p2pConfig := config.DefaultP2PConfig()
	p2pConfig.MaxNumOutboundPeers = maxPeers
	p2pConfig.MaxNumInboundPeers = maxPeers
	p2pConfig.SeedMode = false
	p2pConfig.PexReactor = true
	p2pConfig.Seeds = seedNodes

	// Create node key
	nodeKey, err := p2p.LoadOrGenNodeKey("node_key.json")
	if err != nil {
		return nil, fmt.Errorf("failed to load or generate node key: %w", err)
	}

	// Create node info
	nodeInfo := p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.NewProtocolVersion(0, 11, 0),
		DefaultNodeID:   nodeKey.ID(),
		ListenAddr:      "tcp://0.0.0.0:26656",
		Network:         "celestia",
		Version:         "0.1.0",
		Channels:        []byte{pex.PexChannel},
		Moniker:         "crawler",
	}

	// Create MConn config
	mConfig := conn.DefaultMConnConfig()
	mConfig.SendRate = p2pConfig.SendRate
	mConfig.RecvRate = p2pConfig.RecvRate
	mConfig.MaxPacketMsgPayloadSize = p2pConfig.MaxPacketMsgPayloadSize
	mConfig.FlushThrottle = p2pConfig.FlushThrottleTimeout

	// Create crawler instance with logger first
	c := &Crawler{
		addrBook: pex.NewAddrBook("addrbook.json", true),
	}
	logger := log.NewTMLogger(log.NewSyncWriter(os.Stdout))
	logger = logger.With("module", "crawler")
	logger = log.NewFilter(logger, log.AllowInfo())
	c.BaseService = *service.NewBaseService(logger, "Crawler", c)

	// Create a new switch
	sw := p2p.NewSwitch(
		p2pConfig,
		p2p.NewMultiplexTransport(
			nodeInfo,
			*nodeKey,
			mConfig,
			trace.NoOpTracer(),
		),
		p2p.SwitchPeerFilters(func(peerSet p2p.IPeerSet, peer p2p.Peer) error {
			// Log successful connection
			c.Logger.Info("New peer connected",
				"id", peer.ID(),
				"addr", peer.RemoteAddr(),
				"outbound", peer.IsOutbound(),
			)

			// Store peer info
			addr := peer.SocketAddr()
			c.connected.Store(peer.ID(), &peerInfo{
				addr:      addr,
				startTime: time.Now(),
			})

			return nil
		}),
	)

	// Set up peer filters
	sw.SetLogger(c.Logger)
	sw.SetNodeInfo(nodeInfo)
	sw.SetNodeKey(nodeKey)

	// Create PEX reactor config
	pexConfig := &pex.ReactorConfig{
		SeedMode: true, // We want to be in seed mode to discover peers
		Seeds:    strings.Split(seedNodes, ","),
	}

	// Create PEX reactor
	pexR := pex.NewReactor(c.addrBook, pexConfig)

	// Create crawler reactor
	crawlerR := NewCrawlerReactor(c)

	// Add reactors to switch
	sw.AddReactor("PEX", pexR)
	sw.AddReactor("Crawler", crawlerR)

	c.sw = sw
	c.pexR = pexR

	return c, nil
}

func (c *Crawler) OnStart() error {
	// Start the switch
	if err := c.sw.Start(); err != nil {
		return fmt.Errorf("failed to start switch: %w", err)
	}

	// Start periodic stats reporting
	go c.reportStats()

	return nil
}

func (c *Crawler) OnStop() {
	if err := c.sw.Stop(); err != nil {
		c.Logger.Error("Error stopping switch", "err", err)
	}
}

func (c *Crawler) reportStats() {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	for range ticker.C {
		connected := 0
		c.connected.Range(func(_, _ interface{}) bool {
			connected++
			return true
		})

		c.Logger.Info("Current peer statistics",
			"connected", connected,
			"max_peers", maxPeers,
			"address_book_size", c.addrBook.Size(),
		)
	}
}

func main() {
	// Set up logging
	logger := log.NewTMLogger(log.NewSyncWriter(os.Stdout))
	logger = logger.With("module", "crawler")

	crawler, err := NewCrawler()
	if err != nil {
		logger.Error("Failed to create crawler", "err", err)
		os.Exit(1)
	}

	if err := crawler.Start(); err != nil {
		logger.Error("Failed to start crawler", "err", err)
		os.Exit(1)
	}

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	if err := crawler.Stop(); err != nil {
		logger.Error("Failed to stop crawler", "err", err)
		os.Exit(1)
	}
}
