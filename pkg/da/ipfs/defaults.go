package ipfs

import (
	"os"

	ipfscfg "github.com/ipfs/go-ipfs-config"
	"github.com/ipfs/interface-go-ipfs-core/options"
)

// BootstrapPeers is a list of default bootstrap peers for IPFS network
// TODO(Wondertan): Change this to LL defaults at some point
var BootstrapPeers, _ = ipfscfg.DefaultBootstrapPeers()

// DefaultFullNodeConfig provides default embedded IPFS configuration for FullNode
func DefaultFullNodeConfig() (*ipfscfg.Config, error) {
	identity, err := ipfscfg.CreateIdentity(os.Stdout, []options.KeyGenerateOption{
		options.Key.Type(options.Ed25519Key),
	})
	if err != nil {
		return nil, err
	}

	var conf = &ipfscfg.Config{
		// ed25519 PKI identity for p2p communication
		// TODO(Wondertan): Any reason not reuse ed25519 key from consensus identity?
		Identity: identity,
		// List of libp2p peer addresses the node automatically connects on start
		Bootstrap: ipfscfg.BootstrapPeerStrings(BootstrapPeers),
		// Configure all the addresses the node listens to
		Addresses: ipfscfg.Addresses{
			// List of libp2p addresses to listen to
			Swarm: []string{
				// listen on all network interfaces.
				// NOTE: We don't use IPv6 for now, but we might add support for it at some point.
				"/ip4/0.0.0.0/tcp/16001",
			},
			API: []string{
				"/ip4/127.0.0.1/tcp/17001",
			},
			// List of public libp2p WAN address the Node can be connected with
			// NOTE: Should be configured by Node administrator manually
			Announce: nil,
			// List of libp2p address the Node listens to but not accessible from the WAN.
			// It's a good manner to fil this, as that preserves connected peers from trying connections to those
			// NOTE: Should be configured by Node administrator manually
			NoAnnounce: nil,
			// List of addresses IPFS API server listens to
			// we do not run IPFS Gateway
			Gateway: nil,
		},
		// Networking configuration
		Swarm: ipfscfg.SwarmConfig{
			// auto port forwarding is nice
			DisableNatPortMap: false,
			// metrics are useful
			DisableBandwidthMetrics: false,
			// relays are cool from engineering standpoint, but don't use them
			EnableRelayHop: false, EnableAutoRelay: false,
			// ConnMgr manager controls the amount of conns a Node holds. If the amount of conns hits HighWater, ConnMgr
			// trims amount of connections down to LowWater
			// NOTE: Connections can by prioritized using Tagging in ConnMgr in order to keep important conns alive
			ConnMgr: ipfscfg.ConnMgr{
				// we use defaults from IPFS for HighWater
				HighWater: 900,
				// we use defaults from IPFS for HighWater
				LowWater: 600,
				// defines how often ConnMgr should check conns for trimming, we use default from IPFS
				GracePeriod: "20s",
				// just a basic connection manager
				Type: "basic",
			},
			// Configuration for all networking protocols
			Transports: ipfscfg.Transports{
				Network: transport{
					// we default to TCP as it's most common, battle tested transport, but we may migrate to the one below
					TCP: ipfscfg.True,
					// we might default to QUIC at some point, but for now TCP is more common and reliable
					QUIC: ipfscfg.False,
					// we don't use Websocket as we don't target for browser nodes
					Websocket: ipfscfg.False,
					// we don't use relays as it's expensive in terms of traffic for relay nodes
					Relay: ipfscfg.False,
				},
				// Security protocol wraps IO conns in a private encrypted tunnel
				Security: security{
					// we default to Noise, as it's designed for p2p interactions in the first place and is 0-RTT
					Noise: ipfscfg.DefaultPriority,
					// we don't use SECIO as it's now deprecated and has know issues
					SECIO: ipfscfg.Disabled,
					// we don't allow TLS option as we use noise only
					TLS: ipfscfg.Disabled,
				},
				// Multiplexers provide ability to run multiple logical streams over one transport stream, e.g TCP conn
				Multiplexers: multiplexers{
					// we default to Yamux due to production readiness
					Yamux: ipfscfg.DefaultPriority,
					// we don't use Mplex as it's more like a toy multiplexer for testings and etc
					Mplex: ipfscfg.Disabled,
				},
			},
			// this ia manual peer blacklist
			AddrFilters: nil,
		},
		// Persistent KV store configuration
		Datastore: ipfscfg.Datastore{
			// Configuration for badger kv we default to
			Spec: map[string]interface{}{
				"type":   "measure",
				"prefix": "badger.datastore",
				"child": map[string]interface{}{
					"type":       "badgerds",
					"path":       "badgerds",
					"syncWrites": false,
					"truncate":   true,
				},
			},
			// we don't use bloom filtered blockstore
			BloomFilterSize: 0,
			// we don't use GC so values below does not have any affect
			StorageGCWatermark: 0,
			GCPeriod:           "",
			StorageMax:         "",
		},
		// Configuration for CID reproviding
		// In kDHT all records have TTL, thus we have to regularly(Interval) reprovide/reannounce stored CID to the
		// network. Otherwise information that the node stores something will be lost. Should be in tact with kDHT
		// record cleaning configuration
		// TODO(Wondertan) In case StrategicProviding is true, we have to implement reproviding manually.
		Reprovider: ipfscfg.Reprovider{
			Interval: "12h",
			Strategy: "all",
		},
		// List of all experimental IPFS features
		Experimental: ipfscfg.Experiments{
			// Disables BitSwap providing and reproviding in favour of manual providing.
			StrategicProviding: true,
		},
		Routing: ipfscfg.Routing{
			// Full node must be available from the WAN thus 'dhtserver'
			// Depending on the node type/use-case different modes should be used.
			Type: "dhtserver",
		},
		// Configuration for plugins
		Plugins: ipfscfg.Plugins{
			// Disable preloaded but unused plugins
			Plugins: map[string]ipfscfg.Plugin{
				"ds-flatfs": {
					Disabled: true,
				},
				"ds-level": {
					Disabled: true,
				},
				"ipld-git": {
					Disabled: true,
				},
			},
		},
		// Unused fields:
		// we currently don't use PubSub
		Pubsub: ipfscfg.PubsubConfig{},
		// bi-directional agreement between peers to hold connections with
		Peering: ipfscfg.Peering{},
		// local network discovery is useful, but there is no practical reason to have two FullNode in one LAN
		Discovery: ipfscfg.Discovery{},
		// we don't use AutoNAT service for now
		AutoNAT: ipfscfg.AutoNATConfig{},
		// we use API in some cases, but we do not need additional headers
		API: ipfscfg.API{},
		// we do not use FUSE and thus mounts
		Mounts: ipfscfg.Mounts{},
		// we do not use IPFS
		Ipns: ipfscfg.Ipns{},
		// we do not run Gateway
		Gateway: ipfscfg.Gateway{},
		// we don't use Pinning as we don't ues GC
		Pinning: ipfscfg.Pinning{},
		// unused in IPFS and/or deprecated
		Provider: ipfscfg.Provider{},
	}

	return conf, err
}

// anonymous type aliases to keep default configuration succinct
type (
	//nolint
	transport = struct {
		QUIC      ipfscfg.Flag `json:",omitempty"`
		TCP       ipfscfg.Flag `json:",omitempty"`
		Websocket ipfscfg.Flag `json:",omitempty"`
		Relay     ipfscfg.Flag `json:",omitempty"`
	}
	//nolint
	security = struct {
		TLS   ipfscfg.Priority `json:",omitempty"`
		SECIO ipfscfg.Priority `json:",omitempty"`
		Noise ipfscfg.Priority `json:",omitempty"`
	}
	//nolint
	multiplexers = struct {
		Yamux ipfscfg.Priority `json:",omitempty"`
		Mplex ipfscfg.Priority `json:",omitempty"`
	}
)
