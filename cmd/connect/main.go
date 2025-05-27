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
	"github.com/tendermint/tendermint/version"
)

var (
	addrBookPath = flag.String("addrbook", "", "Path to the address book file")
)

type connectionStats struct {
	mu            sync.Mutex
	connected     int
	failed        int
	totalAttempts int
}

func (cs *connectionStats) incrementConnected() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.connected++
	cs.totalAttempts++
}

func (cs *connectionStats) incrementFailed() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.failed++
	cs.totalAttempts++
}

func (cs *connectionStats) getStats() (int, int, int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.connected, cs.failed, cs.totalAttempts
}

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
	p2pConfig.ListenAddress = "tcp://0.0.0.0:0" // Use random port

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
		nil,
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

	// Create stats tracker
	stats := &connectionStats{}

	// Start stats display goroutine
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			connected, failed, total := stats.getStats()
			logger.Info("Connection statistics",
				"connected", connected,
				"failed", failed,
				"total_attempts", total,
				"remaining", len(allAddrs)-total)
		}
	}()

	// Connect to all addresses
	var wg sync.WaitGroup
	for _, addr := range allAddrs {
		wg.Add(1)
		go func(addr *p2p.NetAddress) {
			defer wg.Done()

			err := sw.DialPeerWithAddress(addr)
			if err != nil {
				logger.Error("Failed to connect to peer",
					"addr", addr,
					"err", err)
				stats.incrementFailed()
			} else {
				logger.Info("Successfully connected to peer",
					"addr", addr)
				stats.incrementConnected()
			}
		}(addr)
	}

	// Wait for all connection attempts to complete
	wg.Wait()

	// Display final statistics
	connected, failed, total := stats.getStats()
	logger.Info("Final connection statistics",
		"connected", connected,
		"failed", failed,
		"total_attempts", total)
}
