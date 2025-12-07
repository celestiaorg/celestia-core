package debug

import (
	"sync"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/conn"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

const (
	// ScraperChannel is an unused channel ID for the scraper reactor
	ScraperChannel = byte(0x70)
)

// ScraperReactor captures peer information during handshakes.
// It implements the p2p.Reactor interface but does minimal work -
// its primary purpose is to receive AddPeer callbacks when peers connect.
type ScraperReactor struct {
	p2p.BaseReactor

	tracer     trace.Tracer
	crawlRound int

	mu         sync.Mutex
	seenPeers  map[p2p.ID]time.Time
	totalPeers int
}

// NewScraperReactor creates a new scraper reactor that captures peer info.
func NewScraperReactor(tracer trace.Tracer, logger log.Logger) *ScraperReactor {
	r := &ScraperReactor{
		tracer:    tracer,
		seenPeers: make(map[p2p.ID]time.Time),
	}
	r.BaseReactor = *p2p.NewBaseReactor("SCRAPER", r)
	r.SetLogger(logger.With("module", "scraper"))
	return r
}

// GetChannels returns a minimal channel for the reactor.
// We don't actually use this channel, but we need one to be a valid reactor.
func (r *ScraperReactor) GetChannels() []*conn.ChannelDescriptor {
	return []*conn.ChannelDescriptor{
		{
			ID:                  ScraperChannel,
			Priority:            1,
			SendQueueCapacity:   1,
			RecvMessageCapacity: 1024,
			MessageType:         &tmp2p.Message{},
		},
	}
}

// AddPeer is called when a new peer successfully connects and handshakes.
// THIS IS WHERE WE CAPTURE THE PEER INFO - the primary purpose of this reactor.
func (r *ScraperReactor) AddPeer(peer p2p.Peer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalPeers++

	nodeInfo, ok := peer.NodeInfo().(p2p.DefaultNodeInfo)
	if !ok {
		r.Logger.Error("Could not cast NodeInfo", "peer", peer.ID())
		return
	}

	// Check if we've seen this peer before
	firstSeen := time.Now()
	if seen, exists := r.seenPeers[peer.ID()]; exists {
		firstSeen = seen
	} else {
		r.seenPeers[peer.ID()] = firstSeen
	}

	// Convert channels to int slice for readability
	channelInts := make([]int, len(nodeInfo.Channels))
	for i, ch := range nodeInfo.Channels {
		channelInts[i] = int(ch)
	}

	// Build discovery record
	discovery := schema.PeerDiscovery{
		PeerID:     string(nodeInfo.DefaultNodeID),
		RemoteAddr: peer.RemoteAddr().String(),
		SocketAddr: peer.SocketAddr().String(),
		ListenAddr: nodeInfo.ListenAddr,
		IsOutbound: peer.IsOutbound(),

		Channels:    channelInts,
		ChannelsHex: nodeInfo.Channels.String(),

		P2PVersion:   nodeInfo.ProtocolVersion.P2P,
		BlockVersion: nodeInfo.ProtocolVersion.Block,
		AppVersion:   nodeInfo.ProtocolVersion.App,
		Software:     nodeInfo.Version,

		Network: nodeInfo.Network,
		Moniker: nodeInfo.Moniker,

		TxIndex:    nodeInfo.Other.TxIndex,
		RPCAddress: nodeInfo.Other.RPCAddress,

		FirstSeen:  firstSeen.Unix(),
		CrawlRound: r.crawlRound,
	}

	// Write to trace
	schema.WritePeerDiscovery(r.tracer, discovery)

	r.Logger.Info("Discovered peer",
		"peer_id", nodeInfo.DefaultNodeID,
		"channels", channelInts,
		"moniker", nodeInfo.Moniker,
		"version", nodeInfo.Version,
		"remote_addr", peer.RemoteAddr().String(),
	)
}

// RemovePeer is called when a peer is disconnected.
func (r *ScraperReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	r.Logger.Debug("Peer disconnected", "peer", peer.ID(), "reason", reason)
}

// Receive is called when a message is received on our channel.
// We don't expect any messages on our channel.
func (r *ScraperReactor) Receive(e p2p.Envelope) {
	// We don't expect any messages on our channel
}

// IncrementCrawlRound increments the crawl round counter.
// This can be used to track different rounds of crawling.
func (r *ScraperReactor) IncrementCrawlRound() {
	r.mu.Lock()
	r.crawlRound++
	r.mu.Unlock()
}

// PeersDiscovered returns the total number of peer connections made.
func (r *ScraperReactor) PeersDiscovered() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalPeers
}

// UniquePeers returns the number of unique peers discovered.
func (r *ScraperReactor) UniquePeers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seenPeers)
}
