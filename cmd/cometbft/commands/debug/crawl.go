package debug

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/pex"
	"github.com/cometbft/cometbft/version"
)

var (
	crawlSeeds     string
	crawlNetwork   string
	crawlDuration  time.Duration
	crawlOutputDir string
	crawlListen    string
)

var crawlCmd = &cobra.Command{
	Use:   "p2p-crawl",
	Short: "Crawl the P2P network and log all peer information",
	Long: `Connects to seed nodes and crawls the entire P2P network,
performing handshakes with all discoverable peers and logging
their NodeInfo (especially channel IDs) to a JSONL file.

Example:
  cometbft debug p2p-crawl \
    --seeds "abc123@seed1.example.org:26656" \
    --network "celestia" \
    --duration 1h \
    --output-dir ./traces`,
	RunE: runCrawl,
}

func init() {
	crawlCmd.Flags().StringVar(&crawlSeeds, "seeds", "",
		"Comma-separated list of seed nodes (id@host:port)")
	crawlCmd.Flags().StringVar(&crawlNetwork, "network", "celestia",
		"Network/chain ID to match")
	crawlCmd.Flags().DurationVar(&crawlDuration, "duration", 0,
		"How long to crawl (0 = until interrupted)")
	crawlCmd.Flags().StringVar(&crawlOutputDir, "output-dir", ".",
		"Directory to write JSONL output (traces written to {output-dir}/data/traces/)")
	crawlCmd.Flags().StringVar(&crawlListen, "listen", "tcp://0.0.0.0:26656",
		"Address to listen on for incoming connections")
}

func runCrawl(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("Received shutdown signal, stopping crawl...")
		cancel()
	}()

	// Generate ephemeral node key for this crawl session
	nodeKey := &p2p.NodeKey{
		PrivKey: ed25519.GenPrivKey(),
	}

	logger.Info("Starting P2P network crawler",
		"node_id", nodeKey.ID(),
		"network", crawlNetwork,
		"seeds", crawlSeeds,
		"duration", crawlDuration,
	)

	// Create config
	config := cfg.DefaultConfig()
	config.P2P.Seeds = crawlSeeds
	config.P2P.SeedMode = true
	config.P2P.MaxNumOutboundPeers = 100
	config.P2P.MaxNumInboundPeers = 100
	config.P2P.AllowDuplicateIP = true
	config.Instrumentation.TraceType = "local"
	config.Instrumentation.TracingTables = schema.PeerDiscoveryTable
	config.Instrumentation.TraceBufferSize = 1000
	config.RootDir = crawlOutputDir

	// Ensure output directory exists
	tracesDir := filepath.Join(crawlOutputDir, "data", "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create traces directory: %w", err)
	}

	// Create tracer for output
	tracer, err := trace.NewTracer(config, logger, crawlNetwork, string(nodeKey.ID()))
	if err != nil {
		return fmt.Errorf("failed to create tracer: %w", err)
	}
	defer tracer.Stop()

	// Create the scraper reactor
	scraper := NewScraperReactor(tracer, logger)

	// Build NodeInfo - advertise ALL channels to maximize compatibility
	nodeInfo := makeScraperNodeInfo(nodeKey, crawlNetwork)

	// Create transport
	mConnConfig := p2p.MConnConfig(config.P2P)
	transport := p2p.NewMultiplexTransport(nodeInfo, *nodeKey, mConnConfig, tracer)

	// Create Switch with scraper reactor
	sw := p2p.NewSwitch(config.P2P, transport, p2p.WithTracer(tracer))
	sw.SetLogger(logger.With("module", "switch"))
	sw.AddReactor("SCRAPER", scraper)
	sw.SetNodeInfo(nodeInfo)
	sw.SetNodeKey(nodeKey)

	// Create AddrBook
	addrBookFile := filepath.Join(crawlOutputDir, "addrbook.json")
	addrBook := pex.NewAddrBook(addrBookFile, false)
	addrBook.SetLogger(logger.With("module", "addrbook"))
	sw.SetAddrBook(addrBook)

	// Create and add PEX reactor in seed mode
	pexReactor := pex.NewReactor(addrBook, &pex.ReactorConfig{
		Seeds:                    splitAndTrimEmpty(crawlSeeds, ",", " "),
		SeedMode:                 true,
		SeedDisconnectWaitPeriod: 30 * time.Second, // Quick disconnect to maximize crawl rate
	})
	pexReactor.SetLogger(logger.With("module", "pex"))
	sw.AddReactor("PEX", pexReactor)

	// Start transport listener for inbound connections
	addr, err := p2p.NewNetAddressString(
		p2p.IDAddressString(nodeKey.ID(), crawlListen))
	if err != nil {
		return fmt.Errorf("failed to parse listen address: %w", err)
	}
	if err := transport.Listen(*addr); err != nil {
		logger.Error("Failed to start listener", "err", err)
		// Continue anyway - we can still dial out
	} else {
		logger.Info("Listening for incoming connections", "addr", crawlListen)
	}

	// Start the switch
	if err := sw.Start(); err != nil {
		return fmt.Errorf("failed to start switch: %w", err)
	}
	defer func() {
		if err := sw.Stop(); err != nil {
			logger.Error("Failed to stop switch", "err", err)
		}
		sw.Wait()
	}()

	logger.Info("Crawler started, press Ctrl+C to stop")

	// Wait for duration or signal
	if crawlDuration > 0 {
		select {
		case <-time.After(crawlDuration):
			logger.Info("Crawl duration reached")
		case <-ctx.Done():
		}
	} else {
		<-ctx.Done()
	}

	logger.Info("Crawl complete",
		"peers_discovered", scraper.PeersDiscovered(),
		"unique_peers", scraper.UniquePeers(),
	)

	return nil
}

func makeScraperNodeInfo(nodeKey *p2p.NodeKey, network string) p2p.DefaultNodeInfo {
	// Advertise ALL known channels for maximum compatibility
	// This ensures we can connect to nodes that filter for specific channels
	channels := []byte{
		0x00,             // PEX
		0x20, 0x21, 0x22, 0x23, // Consensus (State, Data, Vote, VoteSetBits)
		0x30, 0x31, 0x32, // Mempool (v0, CAT Data, CAT Wants)
		0x38,       // Evidence
		0x40,       // BlockSync
		0x50, 0x51, // Propagation (Data, Want)
		0x60, 0x61, // StateSync (Snapshot, Chunk)
	}

	return p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.NewProtocolVersion(
			version.P2PProtocol,
			version.BlockProtocol,
			0,
		),
		DefaultNodeID: nodeKey.ID(),
		ListenAddr:    crawlListen,
		Network:       network,
		Version:       version.TMCoreSemVer,
		Channels:      channels,
		Moniker:       "network-crawler",
		Other: p2p.DefaultNodeInfoOther{
			TxIndex:    "off",
			RPCAddress: "",
		},
	}
}

// splitAndTrimEmpty slices s into all subslices separated by sep and returns a
// slice of the string s with all leading and trailing Unicode code points
// contained in cutset removed. It also filters out empty strings.
func splitAndTrimEmpty(s, sep, cutset string) []string {
	if s == "" {
		return []string{}
	}

	spl := strings.Split(s, sep)
	nonEmptyStrings := make([]string, 0, len(spl))
	for i := 0; i < len(spl); i++ {
		element := strings.Trim(spl[i], cutset)
		if element != "" {
			nonEmptyStrings = append(nonEmptyStrings, element)
		}
	}
	return nonEmptyStrings
}
