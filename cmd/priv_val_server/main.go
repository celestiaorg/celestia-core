package main

import (
	"flag"
	"os"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	cmtnet "github.com/cometbft/cometbft/libs/net"
	cmtos "github.com/cometbft/cometbft/libs/os"
	"github.com/cometbft/cometbft/privval"
)

/**
 * The Remote Private Validator (PV) isolates the private key from the validator node.
 * This script establishes a secure connection to the node to handle signing requests.
 */
func main() {
	var (
		// CLI Flags for flexible configuration
		addr             = flag.String("addr", ":26659", "Address of the node to connect to")
		chainID          = flag.String("chain-id", "mychain", "The ID of the blockchain")
		privValKeyPath   = flag.String("priv-key", "", "Path to the private validator key file (JSON)")
		privValStatePath = flag.String("priv-state", "", "Path to the private validator state file (JSON)")

		// Structured Logging
		logger = log.NewTMLogger(log.NewSyncWriter(os.Stdout)).With("module", "priv_val_service")
	)
	flag.Parse()

	// Validation: Ensure mandatory file paths are provided
	if *privValKeyPath == "" || *privValStatePath == "" {
		logger.Error("Key and State paths must be specified")
		os.Exit(1)
	}

	logger.Info("Initializing Private Validator Service", "addr", *addr, "chainID", *chainID)

	// Load the Private Validator (contains the Ed25519 signing key)
	pv := privval.LoadFilePV(*privValKeyPath, *privValStatePath)

	[Image of Tendermint Remote Signer architecture]

	// Connection Logic: Setup Dialer based on protocol (TCP or Unix Socket)
	var dialer privval.SocketDialer
	protocol, address := cmtnet.ProtocolAndAddress(*addr)

	switch protocol {
	case "unix":
		dialer = privval.DialUnixFn(address)
	case "tcp":
		// Security: Using a transient private key for the connection handshake (Secret Connection)
		const connectionTimeout = 5 * time.Second
		dialer = privval.DialTCPFn(address, connectionTimeout, ed25519.GenPrivKey())
	default:
		logger.Error("Unsupported protocol", "protocol", protocol)
		os.Exit(1)
	}

	// High-Level Service Setup
	// SignerDialerEndpoint: Manages the lifecycle of the connection
	// SignerServer: Handles the logic of signing votes and proposals
	sd := privval.NewSignerDialerEndpoint(logger, dialer)
	ss := privval.NewSignerServer(sd, *chainID, pv)

	if err := ss.Start(); err != nil {
		logger.Error("Failed to start Signer Server", "err", err)
		os.Exit(1)
	}

	[Image of CometBFT consensus engine message signing flow]

	// Graceful Shutdown Logic: Capture SIGTERM/CTRL-C
	cmtos.TrapSignal(logger, func() {
		if err := ss.Stop(); err != nil {
			logger.Error("Error during shutdown", "err", err)
		}
		logger.Info("Private Validator service stopped cleanly")
	})

	// Keep the process alive
	select {}
}
