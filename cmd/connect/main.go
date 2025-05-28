package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto/ed25519"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/pex"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/version"
)

var (
	addrBookPath = flag.String("addrbook", "", "Path to the address book file")
)

// addrBookJSON represents the structure of the address book file
type addrBookJSON struct {
	Key   string          `json:"key"`
	Addrs []*knownAddress `json:"addrs"`
}

// knownAddress represents a known address in the address book
type knownAddress struct {
	Addr        *p2p.NetAddress `json:"addr"`
	Buckets     []int           `json:"buckets"`
	BucketType  byte            `json:"bucket_type"`
	LastAttempt time.Time       `json:"last_attempt"`
	LastSuccess time.Time       `json:"last_success"`
	Attempts    int             `json:"attempts"`
}

// getAllAddressesFromFile reads all addresses directly from the address book file
func getAllAddressesFromFile(filePath string) ([]*p2p.NetAddress, error) {
	// Read the file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read address book file: %w", err)
	}

	// Parse the JSON
	var addrBook addrBookJSON
	if err := json.Unmarshal(data, &addrBook); err != nil {
		return nil, fmt.Errorf("failed to parse address book file: %w", err)
	}

	// Extract all unique addresses
	addresses := make([]*p2p.NetAddress, 0, len(addrBook.Addrs))
	seen := make(map[string]struct{})
	for _, ka := range addrBook.Addrs {
		if ka.Addr == nil {
			continue
		}
		addrStr := ka.Addr.String()
		if _, ok := seen[addrStr]; !ok {
			addresses = append(addresses, ka.Addr)
			seen[addrStr] = struct{}{}
		}
	}

	return addresses, nil
}

func main() {
	flag.Parse()

	if *addrBookPath == "" {
		fmt.Println("Please provide path to address book file using -addrbook flag")
		os.Exit(1)
	}

	logger := log.NewTMLogger(log.NewSyncWriter(os.Stdout))
	logger = logger.With("module", "connect")
	logger = log.NewFilter(logger, log.AllowInfo())

	// Get all addresses directly from the file
	allAddrs, err := getAllAddressesFromFile(*addrBookPath)
	if err != nil {
		logger.Error("Failed to read addresses from file", "err", err)
		os.Exit(1)
	}

	if len(allAddrs) == 0 {
		logger.Info("No addresses found in address book")
		os.Exit(0)
	}

	logger.Info("Found addresses in address book", "count", len(allAddrs))

	// Create address book for the switch
	addrBook := pex.NewAddrBook(*addrBookPath, true)
	addrBook.SetLogger(logger)

	// Start address book
	if err := addrBook.Start(); err != nil {
		logger.Error("Failed to start address book", "err", err)
		os.Exit(1)
	}
	defer addrBook.Stop()

	// Create P2P config
	p2pConfig := config.DefaultP2PConfig()
	p2pConfig.ListenAddress = "tcp://0.0.0.0:0"   // Use random port
	p2pConfig.MaxNumInboundPeers = 2000           // Increase from default 40
	p2pConfig.MaxNumOutboundPeers = 2000          // Increase from default 10
	p2pConfig.HandshakeTimeout = 30 * time.Second // Increase from default 20s
	p2pConfig.DialTimeout = 15 * time.Second      // Increase from default 3s
	p2pConfig.AllowDuplicateIP = true             // Allow multiple connections from same IP

	// Create node key
	nodeKey := &p2p.NodeKey{
		PrivKey: ed25519.GenPrivKey(),
	}

	// Create node info
	nodeInfo := p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.NewProtocolVersion(
			version.P2PProtocol,
			version.BlockProtocol,
			0,
		),
		DefaultNodeID: nodeKey.ID(),
		ListenAddr:    p2pConfig.ListenAddress,
		Network:       "celestia",
		Version:       version.TMCoreSemVer,
		Channels:      []byte{pex.PexChannel},
		Moniker:       "connect",
	}

	// Create transport
	transport := p2p.NewMultiplexTransport(
		nodeInfo,
		*nodeKey,
		p2p.MConnConfig(p2pConfig),
		trace.NoOpTracer(),
	)

	// Create switch
	sw := p2p.NewSwitch(
		p2pConfig,
		transport,
	)
	sw.SetLogger(logger)
	sw.SetNodeInfo(nodeInfo)
	sw.SetNodeKey(nodeKey)
	sw.SetAddrBook(addrBook)

	// Create and add PEX reactor
	pexReactor := pex.NewReactor(addrBook, &pex.ReactorConfig{
		SeedMode: false,
	})
	pexReactor.SetLogger(logger)
	sw.AddReactor("PEX", pexReactor)

	// Start switch
	if err := sw.Start(); err != nil {
		logger.Error("Failed to start switch", "err", err)
		os.Exit(1)
	}
	defer sw.Stop()

	// Function to attempt connection to a single peer
	connectToPeer := func(addr *p2p.NetAddress) bool {
		err := sw.DialPeerWithAddress(addr)
		if err != nil {
			logger.Error("Failed to connect to peer", "addr", addr, "err", err)
			return false
		}
		logger.Info("Connected to peer", "addr", addr)
		return true
	}

	reconnect := func() {
		var wg sync.WaitGroup
		for _, addr := range allAddrs {
			wg.Add(1)
			go func(addr *p2p.NetAddress) {
				defer wg.Done()
				if !sw.IsDialingOrExistingAddress(addr) {
					connectToPeer(addr)
				}
			}(addr)
		}
		wg.Wait()
	}

	// Retry loop for failed connections
	retryTicker := time.NewTicker(20 * time.Second)
	defer retryTicker.Stop()

	for {
		reconnect()

		logger.Info("Connection stats", "number connected", sw.Peers().Size(), "number total", len(allAddrs))
		<-retryTicker.C
	}
}
