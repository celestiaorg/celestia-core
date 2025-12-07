package schema

import "github.com/cometbft/cometbft/libs/trace"

const (
	// PeerDiscoveryTable is the name of the table that stores peer discovery data.
	PeerDiscoveryTable = "peer_discovery"
)

// PeerDiscovery captures complete handshake information for network scraping.
type PeerDiscovery struct {
	// Identity
	PeerID     string `json:"peer_id"`
	RemoteAddr string `json:"remote_addr"`
	SocketAddr string `json:"socket_addr"`
	ListenAddr string `json:"listen_addr"`
	IsOutbound bool   `json:"is_outbound"`

	// Channel info - the primary target data
	Channels    []int  `json:"channels"`     // Channel IDs as integers for readability
	ChannelsHex string `json:"channels_hex"` // Original hex representation

	// Version info
	P2PVersion   uint64 `json:"p2p_version"`
	BlockVersion uint64 `json:"block_version"`
	AppVersion   uint64 `json:"app_version"`
	Software     string `json:"software_version"`

	// Network info
	Network string `json:"network"`
	Moniker string `json:"moniker"`

	// Other
	TxIndex    string `json:"tx_index"`
	RPCAddress string `json:"rpc_address"`

	// Metadata
	FirstSeen  int64 `json:"first_seen_unix"`
	CrawlRound int   `json:"crawl_round"`
}

// Table returns the table name for the PeerDiscovery struct.
func (PeerDiscovery) Table() string {
	return PeerDiscoveryTable
}

// WritePeerDiscovery writes a tracing point for a peer discovery event.
func WritePeerDiscovery(client trace.Tracer, pd PeerDiscovery) {
	client.Write(pd)
}
