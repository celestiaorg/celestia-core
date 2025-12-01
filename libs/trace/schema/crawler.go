package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

// CrawlerTables returns the list of tables that are used for crawler tracing.
func CrawlerTables() []string {
	return []string{
		CrawlerPeersTable,
	}
}

const (
	// CrawlerPeersTable is the name of the table that stores crawler peer data.
	CrawlerPeersTable = "crawler_peers"
)

// CrawlerPeer describes the schema for the "crawler_peers" table.
type CrawlerPeer struct {
	PeerID             string `json:"peer_id"`
	IP                 string `json:"ip"`
	Port               int    `json:"port"`
	Moniker            string `json:"moniker"`
	Version            string `json:"version"`
	ChainID            string `json:"chain_id"`
	Channels           []int  `json:"channels"`
	IsLegacyPropagation bool   `json:"is_legacy_propagation"`
	HasCAT             bool   `json:"has_cat"`
}

// Table returns the table name for the CrawlerPeer struct.
func (CrawlerPeer) Table() string {
	return CrawlerPeersTable
}

const (
	// Channel constants for detection
	consensusDataChannel  = byte(0x21) // Old/Legacy Propagation
	catMempoolDataChannel = byte(0x31) // CAT Mempool Data
	catMempoolWantChannel = byte(0x32) // CAT Mempool Wants
	propDataChannel       = byte(0x50) // New Propagation Data
	propWantChannel       = byte(0x51) // New Propagation Wants
)

// IsLegacyPropagation checks if the peer only supports legacy propagation
// (has consensus data channel 0x21 but not the new prop channels 0x50/0x51)
func IsLegacyPropagation(channels []int) bool {
	hasNewProp := false
	for _, ch := range channels {
		if byte(ch) == propDataChannel || byte(ch) == propWantChannel {
			hasNewProp = true
			break
		}
	}
	return !hasNewProp
}

// HasCAT checks if the peer supports CAT (Concurrent Asynchronous Transactions)
// by checking for CAT mempool channels 0x31/0x32
func HasCAT(channels []int) bool {
	for _, ch := range channels {
		if byte(ch) == catMempoolDataChannel || byte(ch) == catMempoolWantChannel {
			return true
		}
	}
	return false
}

// WriteCrawlerPeer writes a tracing point for a crawler peer using the predetermined
// schema for crawler tracing.
func WriteCrawlerPeer(
	client trace.Tracer,
	peerID string,
	ip string,
	port int,
	moniker string,
	version string,
	chainID string,
	channels []int,
	isLegacyPropagation bool,
	hasCAT bool,
) {
	client.Write(CrawlerPeer{
		PeerID:             peerID,
		IP:                 ip,
		Port:               port,
		Moniker:            moniker,
		Version:            version,
		ChainID:            chainID,
		Channels:           channels,
		IsLegacyPropagation: isLegacyPropagation,
		HasCAT:             hasCAT,
	})
}
