package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

// BlocksyncTables returns the list of tables that are used for blocksync
// tracing.
func BlocksyncTables() []string {
	return []string{
		BlocksyncBlockReceivedTable,
		BlocksyncBlockSavedTable,
	}
}

// Schema constants for blocksync tracing tables.
const (
	// BlocksyncBlockReceivedTable stores traces of blocks received from peers.
	BlocksyncBlockReceivedTable = "blocksync_block_received"
)

// BlocksyncBlockReceived describes schema for the "blocksync_block_received" table.
// This event is traced when we receive a block from a peer.
type BlocksyncBlockReceived struct {
	Height    int64  `json:"height"`
	PeerID    string `json:"peer_id"`
	BlockSize int    `json:"block_size"`
}

// Table returns the table name for the BlocksyncBlockReceived struct.
func (BlocksyncBlockReceived) Table() string {
	return BlocksyncBlockReceivedTable
}

// WriteBlocksyncBlockReceived writes a tracing point for a block received from a peer.
func WriteBlocksyncBlockReceived(client trace.Tracer, height int64, peerID string, blockSize int) {
	if !client.IsCollecting(BlocksyncBlockReceivedTable) {
		return
	}
	client.Write(BlocksyncBlockReceived{
		Height:    height,
		PeerID:    peerID,
		BlockSize: blockSize,
	})
}

// Schema constants for blocksync block saved table.
const (
	// BlocksyncBlockSavedTable stores traces of blocks successfully validated and saved to store.
	BlocksyncBlockSavedTable = "blocksync_block_saved"
)

// BlocksyncBlockSaved describes schema for the "blocksync_block_saved" table.
// This event is traced after a block is successfully validated and saved to the block store.
type BlocksyncBlockSaved struct {
	Height    int64 `json:"height"`
	BlockSize int   `json:"block_size"`
}

// Table returns the table name for the BlocksyncBlockSaved struct.
func (BlocksyncBlockSaved) Table() string {
	return BlocksyncBlockSavedTable
}

// WriteBlocksyncBlockSaved writes a tracing point for a successfully validated and saved block.
func WriteBlocksyncBlockSaved(client trace.Tracer, height int64, blockSize int) {
	if !client.IsCollecting(BlocksyncBlockSavedTable) {
		return
	}
	client.Write(BlocksyncBlockSaved{
		Height:    height,
		BlockSize: blockSize,
	})
}
