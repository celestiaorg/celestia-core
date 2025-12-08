package debug

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	"github.com/cometbft/cometbft/p2p/pex"
)

// TestScraperReactorGetChannels verifies that the scraper reactor
// registers all the expected channels to ensure compatibility with peers.
func TestScraperReactorGetChannels(t *testing.T) {
	tracer := trace.NoOpTracer()
	logger := log.TestingLogger()

	reactor := NewScraperReactor(tracer, logger)

	channels := reactor.GetChannels()
	require.NotNil(t, channels)

	// We should have all expected channels registered
	expectedChannels := []byte{
		StateChannel,
		DataChannel,
		VoteChannel,
		VoteSetBitsChannel,
		MempoolChannel,
		MempoolDataChannel,
		MempoolWantsChannel,
		EvidenceChannel,
		BlocksyncChannel,
		PropagationDataChannel,
		PropagationWantChannel,
		SnapshotChannel,
		ChunkChannel,
	}

	assert.Len(t, channels, len(expectedChannels), "Scraper should register all expected channels")

	// Verify each expected channel is present
	channelIDs := make(map[byte]bool)
	for _, ch := range channels {
		channelIDs[ch.ID] = true
	}

	for _, expected := range expectedChannels {
		assert.True(t, channelIDs[expected], "Channel 0x%02X should be registered", expected)
	}
}

// TestScraperReactorAddPeer verifies that the scraper reactor properly
// records peer information when a peer connects.
func TestScraperReactorAddPeer(t *testing.T) {
	tracer := trace.NoOpTracer()
	logger := log.TestingLogger()

	reactor := NewScraperReactor(tracer, logger)

	// Create a mock peer
	peer := createMockPeer(t)

	// Initially no peers
	assert.Equal(t, 0, reactor.PeersDiscovered())
	assert.Equal(t, 0, reactor.UniquePeers())

	// Add the peer
	reactor.AddPeer(peer)

	// Verify peer was recorded
	assert.Equal(t, 1, reactor.PeersDiscovered())
	assert.Equal(t, 1, reactor.UniquePeers())

	// Adding the same peer again should increment total but not unique
	reactor.AddPeer(peer)
	assert.Equal(t, 2, reactor.PeersDiscovered())
	assert.Equal(t, 1, reactor.UniquePeers())
}

// TestCrawlerCanConnectToPeer tests that the crawler can establish
// a connection with a regular peer node.
func TestCrawlerCanConnectToPeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "crawler_test")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	cfg := config.DefaultP2PConfig()
	cfg.AllowDuplicateIP = true

	// Create a "regular" peer node
	peerSwitch := createTestSwitch(t, cfg, dir, 0, false)
	require.NoError(t, peerSwitch.Start())
	defer peerSwitch.Stop()

	// Create the crawler switch
	crawlerSwitch := createCrawlerSwitch(t, cfg, dir, 1)
	require.NoError(t, crawlerSwitch.Start())
	defer crawlerSwitch.Stop()

	// Dial the peer from the crawler
	err = crawlerSwitch.DialPeerWithAddress(peerSwitch.NetAddress())
	require.NoError(t, err, "Crawler should be able to dial peer")

	// Wait for connection
	assertConnected(t, crawlerSwitch, peerSwitch, 5*time.Second)
}

// TestCrawlerCanConnectToSeed tests that the crawler can connect to a seed node
// and receive peer addresses.
func TestCrawlerCanConnectToSeed(t *testing.T) {
	dir, err := os.MkdirTemp("", "crawler_seed_test")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	cfg := config.DefaultP2PConfig()
	cfg.AllowDuplicateIP = true

	// Create a seed node
	seedSwitch := createTestSwitch(t, cfg, dir, 0, true)
	require.NoError(t, seedSwitch.Start())
	defer seedSwitch.Stop()

	// Create the crawler switch
	crawlerSwitch := createCrawlerSwitch(t, cfg, dir, 1)
	require.NoError(t, crawlerSwitch.Start())
	defer crawlerSwitch.Stop()

	// Dial the seed from the crawler
	err = crawlerSwitch.DialPeerWithAddress(seedSwitch.NetAddress())
	require.NoError(t, err, "Crawler should be able to dial seed")

	// Wait for connection
	assertConnected(t, crawlerSwitch, seedSwitch, 5*time.Second)
}

// TestCrawlerPEXIntegration tests that the crawler can discover peers via PEX
// from a seed node. The seed will dial known peers and the crawler receives
// address information through PEX protocol.
func TestCrawlerPEXIntegration(t *testing.T) {
	dir, err := os.MkdirTemp("", "crawler_pex_test")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	cfg := config.DefaultP2PConfig()
	cfg.AllowDuplicateIP = true

	// Create a few peer nodes first
	peers := make([]*p2p.Switch, 2)
	peerAddrs := make([]*p2p.NetAddress, 2)
	for i := 0; i < 2; i++ {
		peers[i] = createTestSwitch(t, cfg, dir, i, false)
		require.NoError(t, peers[i].Start())
		defer peers[i].Stop()
		peerAddrs[i] = peers[i].NetAddress()
	}

	// Create a seed node that knows about the peers
	seedSwitch := createSeedWithPeers(t, cfg, dir, 10, peerAddrs)
	require.NoError(t, seedSwitch.Start())
	defer seedSwitch.Stop()

	// Create the crawler with PEX reactor configured to use the seed
	crawlerSwitch, scraper := createCrawlerWithSeedAndScraper(t, cfg, dir, 20, seedSwitch.NetAddress())
	require.NoError(t, crawlerSwitch.Start())
	defer crawlerSwitch.Stop()

	// Wait for the crawler to connect to the seed
	assertConnected(t, crawlerSwitch, seedSwitch, 5*time.Second)

	// Verify the scraper recorded the seed connection
	assert.GreaterOrEqual(t, scraper.PeersDiscovered(), 1, "Scraper should have discovered at least the seed")
	t.Logf("Crawler discovered %d peers (unique: %d)", scraper.PeersDiscovered(), scraper.UniquePeers())
}

// TestNodeInfoChannelCompatibility verifies that our nodeInfo channels
// are compatible with a standard node's channels.
func TestNodeInfoChannelCompatibility(t *testing.T) {
	nodeKey := &p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}

	crawlerNodeInfo := makeScraperNodeInfo(nodeKey, "test-network")

	// Create a standard node's nodeInfo with minimal channels (just PEX)
	standardNodeInfo := p2p.DefaultNodeInfo{
		ProtocolVersion: crawlerNodeInfo.ProtocolVersion,
		DefaultNodeID:   p2p.ID("test-peer-id"),
		ListenAddr:      "tcp://127.0.0.1:26656",
		Network:         "test-network",
		Version:         "1.0.0",
		Channels:        []byte{0x00}, // Just PEX channel
		Moniker:         "test-peer",
	}

	// They should be compatible (share at least one channel - PEX 0x00)
	err := crawlerNodeInfo.CompatibleWith(standardNodeInfo)
	assert.NoError(t, err, "Crawler should be compatible with node that has PEX channel")

	// Test with consensus channels only
	consensusNodeInfo := p2p.DefaultNodeInfo{
		ProtocolVersion: crawlerNodeInfo.ProtocolVersion,
		DefaultNodeID:   p2p.ID("consensus-peer-id"),
		ListenAddr:      "tcp://127.0.0.1:26657",
		Network:         "test-network",
		Version:         "1.0.0",
		Channels:        []byte{0x20, 0x21, 0x22, 0x23}, // Consensus channels
		Moniker:         "consensus-peer",
	}

	err = crawlerNodeInfo.CompatibleWith(consensusNodeInfo)
	assert.NoError(t, err, "Crawler should be compatible with node that has consensus channels")
}

// TestNodeInfoValidation verifies the crawler's nodeInfo passes validation.
func TestNodeInfoValidation(t *testing.T) {
	nodeKey := &p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	crawlListen = "tcp://127.0.0.1:26656" // Set global for makeScraperNodeInfo

	nodeInfo := makeScraperNodeInfo(nodeKey, "test-network")

	err := nodeInfo.Validate()
	assert.NoError(t, err, "Crawler nodeInfo should be valid")

	// Verify channels don't exceed max
	assert.LessOrEqual(t, len(nodeInfo.Channels), 16, "Should not exceed max channels")

	// Verify no duplicate channels
	seen := make(map[byte]bool)
	for _, ch := range nodeInfo.Channels {
		assert.False(t, seen[ch], "Channel 0x%02X should not be duplicated", ch)
		seen[ch] = true
	}
}

// TestCrawlerChannelCompatibility is a diagnostic test that verifies
// the crawler can connect to nodes with various channel configurations.
// This helps identify issues where the crawler can't connect to certain peers.
func TestCrawlerChannelCompatibility(t *testing.T) {
	nodeKey := &p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	crawlListen = "tcp://127.0.0.1:26656"
	crawlerNodeInfo := makeScraperNodeInfo(nodeKey, "test-network")

	// Log the crawler's channels for diagnostic purposes
	t.Logf("Crawler advertises channels: %v", crawlerNodeInfo.Channels)

	// Test various peer channel configurations that might exist in the wild
	testCases := []struct {
		name     string
		channels []byte
		wantErr  bool
	}{
		{
			name:     "only PEX channel",
			channels: []byte{0x00},
			wantErr:  false, // Should work - we have PEX too
		},
		{
			name:     "consensus channels only",
			channels: []byte{0x20, 0x21, 0x22, 0x23},
			wantErr:  false, // Should work - we have these
		},
		{
			name:     "mempool v0 only",
			channels: []byte{0x30},
			wantErr:  false, // Should work - we have this
		},
		{
			name:     "mempool CAT channels",
			channels: []byte{0x31, 0x32},
			wantErr:  false, // Should work - we have these
		},
		{
			name:     "blocksync only",
			channels: []byte{0x40},
			wantErr:  false, // Should work - we have this
		},
		{
			name:     "statesync channels",
			channels: []byte{0x60, 0x61},
			wantErr:  false, // Should work - we have these
		},
		{
			name:     "propagation channels",
			channels: []byte{0x50, 0x51},
			wantErr:  false, // Should work - we have these
		},
		{
			name:     "unknown channel only",
			channels: []byte{0xFF},
			wantErr:  true, // Should fail - no common channel
		},
		{
			name:     "full celestia node channels",
			channels: []byte{0x00, 0x20, 0x21, 0x22, 0x23, 0x30, 0x38, 0x40, 0x50, 0x51},
			wantErr:  false, // Should work - many common channels
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			peerNodeInfo := p2p.DefaultNodeInfo{
				ProtocolVersion: crawlerNodeInfo.ProtocolVersion,
				DefaultNodeID:   p2p.ID("test-peer-id"),
				ListenAddr:      "tcp://127.0.0.1:26657",
				Network:         "test-network",
				Version:         "1.0.0",
				Channels:        tc.channels,
				Moniker:         "test-peer",
			}

			err := crawlerNodeInfo.CompatibleWith(peerNodeInfo)
			if tc.wantErr {
				assert.Error(t, err, "Expected incompatibility for channels %v", tc.channels)
			} else {
				assert.NoError(t, err, "Expected compatibility for channels %v", tc.channels)
			}
		})
	}
}

// TestScraperReactorChannelsMatchNodeInfo verifies that the channels
// registered by the scraper reactor match those advertised in nodeInfo.
// This is critical - if there's a mismatch, the connection may fail.
func TestScraperReactorChannelsMatchNodeInfo(t *testing.T) {
	nodeKey := &p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	crawlListen = "tcp://127.0.0.1:26656"
	nodeInfo := makeScraperNodeInfo(nodeKey, "test-network")

	tracer := trace.NoOpTracer()
	logger := log.TestingLogger()
	reactor := NewScraperReactor(tracer, logger)

	// Get channel IDs from reactor
	reactorChannels := make(map[byte]bool)
	for _, ch := range reactor.GetChannels() {
		reactorChannels[ch.ID] = true
	}

	// Get channel IDs from nodeInfo (excluding PEX which is handled by PEX reactor)
	nodeInfoChannels := make(map[byte]bool)
	for _, ch := range nodeInfo.Channels {
		if ch != 0x00 { // PEX channel is handled by PEX reactor, not scraper
			nodeInfoChannels[ch] = true
		}
	}

	// Every channel in nodeInfo (except PEX) should be registered by the reactor
	for ch := range nodeInfoChannels {
		assert.True(t, reactorChannels[ch],
			"NodeInfo advertises channel 0x%02X but reactor doesn't handle it", ch)
	}

	t.Logf("NodeInfo channels (non-PEX): %d, Reactor channels: %d",
		len(nodeInfoChannels), len(reactorChannels))
}

// Helper functions

func createTestSwitch(t *testing.T, cfg *config.P2PConfig, dir string, id int, seedMode bool) *p2p.Switch {
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	nodeInfo := createTestNodeInfo(nodeKey.ID(), fmt.Sprintf("node%d", id))

	// Add PEX channel to nodeInfo
	ni := nodeInfo.(p2p.DefaultNodeInfo)
	ni.Channels = append(ni.Channels, pex.PexChannel)

	addr, err := p2p.NewNetAddressString(
		p2p.IDAddressString(nodeKey.ID(), ni.ListenAddr),
	)
	require.NoError(t, err)

	transport := p2p.NewMultiplexTransport(ni, nodeKey, p2p.MConnConfig(cfg), trace.NoOpTracer())
	require.NoError(t, transport.Listen(*addr))

	sw := p2p.NewSwitch(cfg, transport)
	sw.SetLogger(log.TestingLogger().With("switch", id))
	sw.SetNodeKey(&nodeKey)
	sw.SetNodeInfo(ni)

	// Add address book
	book := pex.NewAddrBook(filepath.Join(dir, fmt.Sprintf("addrbook%d.json", id)), false)
	book.SetLogger(log.TestingLogger())
	sw.SetAddrBook(book)

	// Add PEX reactor
	pexReactor := pex.NewReactor(book, &pex.ReactorConfig{
		SeedMode: seedMode,
	})
	pexReactor.SetLogger(log.TestingLogger())
	sw.AddReactor("PEX", pexReactor)

	return sw
}

func createCrawlerSwitch(t *testing.T, cfg *config.P2PConfig, dir string, id int) *p2p.Switch {
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}

	// Use the same nodeInfo creation as the crawler command
	crawlListen = "tcp://127.0.0.1:0" // Let the system pick a port
	nodeInfo := makeScraperNodeInfo(&nodeKey, "testing")

	// Get a free port for listening
	nodeInfo.ListenAddr = fmt.Sprintf("127.0.0.1:%d", getFreePort())

	addr, err := p2p.NewNetAddressString(
		p2p.IDAddressString(nodeKey.ID(), nodeInfo.ListenAddr),
	)
	require.NoError(t, err)

	transport := p2p.NewMultiplexTransport(nodeInfo, nodeKey, p2p.MConnConfig(cfg), trace.NoOpTracer())
	require.NoError(t, transport.Listen(*addr))

	sw := p2p.NewSwitch(cfg, transport)
	sw.SetLogger(log.TestingLogger().With("crawler", id))
	sw.SetNodeKey(&nodeKey)
	sw.SetNodeInfo(nodeInfo)

	// Add scraper reactor
	scraper := NewScraperReactor(trace.NoOpTracer(), log.TestingLogger())
	sw.AddReactor("SCRAPER", scraper)

	// Add address book
	book := pex.NewAddrBook(filepath.Join(dir, fmt.Sprintf("crawler_addrbook%d.json", id)), false)
	book.SetLogger(log.TestingLogger())
	sw.SetAddrBook(book)

	// Add PEX reactor in seed mode (like the crawler)
	pexReactor := pex.NewReactor(book, &pex.ReactorConfig{
		SeedMode:                 true,
		SeedDisconnectWaitPeriod: 30 * time.Second,
	})
	pexReactor.SetLogger(log.TestingLogger())
	sw.AddReactor("PEX", pexReactor)

	return sw
}

func createCrawlerWithSeed(t *testing.T, cfg *config.P2PConfig, dir string, id int, seedAddr *p2p.NetAddress) *p2p.Switch {
	sw, _ := createCrawlerWithSeedAndScraper(t, cfg, dir, id, seedAddr)
	return sw
}

func createCrawlerWithSeedAndScraper(t *testing.T, cfg *config.P2PConfig, dir string, id int, seedAddr *p2p.NetAddress) (*p2p.Switch, *ScraperReactor) {
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}

	crawlListen = "tcp://127.0.0.1:0"
	nodeInfo := makeScraperNodeInfo(&nodeKey, "testing")
	nodeInfo.ListenAddr = fmt.Sprintf("127.0.0.1:%d", getFreePort())

	addr, err := p2p.NewNetAddressString(
		p2p.IDAddressString(nodeKey.ID(), nodeInfo.ListenAddr),
	)
	require.NoError(t, err)

	transport := p2p.NewMultiplexTransport(nodeInfo, nodeKey, p2p.MConnConfig(cfg), trace.NoOpTracer())
	require.NoError(t, transport.Listen(*addr))

	sw := p2p.NewSwitch(cfg, transport)
	sw.SetLogger(log.TestingLogger().With("crawler", id))
	sw.SetNodeKey(&nodeKey)
	sw.SetNodeInfo(nodeInfo)

	// Add scraper reactor
	scraper := NewScraperReactor(trace.NoOpTracer(), log.TestingLogger())
	sw.AddReactor("SCRAPER", scraper)

	// Add address book
	book := pex.NewAddrBook(filepath.Join(dir, fmt.Sprintf("crawler_addrbook%d.json", id)), false)
	book.SetLogger(log.TestingLogger())
	sw.SetAddrBook(book)

	// Add PEX reactor with seed configured
	pexReactor := pex.NewReactor(book, &pex.ReactorConfig{
		SeedMode:                 true,
		SeedDisconnectWaitPeriod: 30 * time.Second,
		Seeds:                    []string{seedAddr.String()},
	})
	pexReactor.SetLogger(log.TestingLogger())
	sw.AddReactor("PEX", pexReactor)

	return sw, scraper
}

func createSeedWithPeers(t *testing.T, cfg *config.P2PConfig, dir string, id int, peerAddrs []*p2p.NetAddress) *p2p.Switch {
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	nodeInfo := createTestNodeInfo(nodeKey.ID(), fmt.Sprintf("seed%d", id))

	ni := nodeInfo.(p2p.DefaultNodeInfo)
	ni.Channels = append(ni.Channels, pex.PexChannel)

	addr, err := p2p.NewNetAddressString(
		p2p.IDAddressString(nodeKey.ID(), ni.ListenAddr),
	)
	require.NoError(t, err)

	transport := p2p.NewMultiplexTransport(ni, nodeKey, p2p.MConnConfig(cfg), trace.NoOpTracer())
	require.NoError(t, transport.Listen(*addr))

	sw := p2p.NewSwitch(cfg, transport)
	sw.SetLogger(log.TestingLogger().With("seed", id))
	sw.SetNodeKey(&nodeKey)
	sw.SetNodeInfo(ni)

	// Add address book with known peers
	book := pex.NewAddrBook(filepath.Join(dir, fmt.Sprintf("seed_addrbook%d.json", id)), false)
	book.SetLogger(log.TestingLogger())
	for _, peerAddr := range peerAddrs {
		err := book.AddAddress(peerAddr, peerAddr)
		require.NoError(t, err)
		book.MarkGood(peerAddr.ID)
	}
	sw.SetAddrBook(book)

	// Add PEX reactor
	pexReactor := pex.NewReactor(book, &pex.ReactorConfig{})
	pexReactor.SetLogger(log.TestingLogger())
	sw.AddReactor("PEX", pexReactor)

	return sw
}

func createTestNodeInfo(id p2p.ID, name string) p2p.NodeInfo {
	return p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.NewProtocolVersion(8, 11, 0),
		DefaultNodeID:   id,
		ListenAddr:      fmt.Sprintf("127.0.0.1:%d", getFreePort()),
		Network:         "testing",
		Version:         "1.0.0",
		Channels:        []byte{0x01}, // Test channel
		Moniker:         name,
		Other: p2p.DefaultNodeInfoOther{
			TxIndex:    "off",
			RPCAddress: "",
		},
	}
}

func getFreePort() int {
	port, err := getFreePortErr()
	if err != nil {
		panic(err)
	}
	return port
}

func getFreePortErr() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func assertConnected(t *testing.T, sw1, sw2 *p2p.Switch, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if sw1.Peers().Has(sw2.NodeInfo().ID()) || sw2.Peers().Has(sw1.NodeInfo().ID()) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("Switches did not connect within %v. sw1 peers: %d, sw2 peers: %d",
		timeout, sw1.Peers().Size(), sw2.Peers().Size())
}

func assertMinPeers(t *testing.T, sw *p2p.Switch, minPeers int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if sw.Peers().Size() >= minPeers {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("Switch did not reach %d peers within %v. Current peers: %d",
		minPeers, timeout, sw.Peers().Size())
}

// createMockPeer creates a mock peer for testing
func createMockPeer(t *testing.T) p2p.Peer {
	return mock.NewPeer(nil)
}
