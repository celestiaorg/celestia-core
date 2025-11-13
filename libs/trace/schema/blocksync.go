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
		BlocksyncBlockRequestedTable,
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
	Height             int64 `json:"height"`
	BlockSize          int   `json:"block_size"`
	ValidationDuration int64 `json:"validation_duration"` // Duration in milliseconds
	SaveDuration       int64 `json:"save_duration"`       // Duration in milliseconds
	TotalDuration      int64 `json:"total_duration"`      // Duration in milliseconds
}

// Table returns the table name for the BlocksyncBlockSaved struct.
func (BlocksyncBlockSaved) Table() string {
	return BlocksyncBlockSavedTable
}

// WriteBlocksyncBlockSaved writes a tracing point for a successfully validated and saved block.
func WriteBlocksyncBlockSaved(client trace.Tracer, height int64, blockSize int, validationDuration, saveDuration, totalDuration int64) {
	if !client.IsCollecting(BlocksyncBlockSavedTable) {
		return
	}
	client.Write(BlocksyncBlockSaved{
		Height:             height,
		BlockSize:          blockSize,
		ValidationDuration: validationDuration,
		SaveDuration:       saveDuration,
		TotalDuration:      totalDuration,
	})
}

// Schema constants for blocksync block requested table.
const (
	// BlocksyncBlockRequestedTable stores traces of block requests sent to peers.
	BlocksyncBlockRequestedTable = "blocksync_block_requested"
)

// BlocksyncBlockRequested describes schema for the "blocksync_block_requested" table.
// This event is traced when we request a block from a peer.
type BlocksyncBlockRequested struct {
	Height int64  `json:"height"`
	PeerID string `json:"peer_id"`
}

// Table returns the table name for the BlocksyncBlockRequested struct.
func (BlocksyncBlockRequested) Table() string {
	return BlocksyncBlockRequestedTable
}

// WriteBlocksyncBlockRequested writes a tracing point for a block request sent to a peer.
func WriteBlocksyncBlockRequested(client trace.Tracer, height int64, peerID string) {
	if !client.IsCollecting(BlocksyncBlockRequestedTable) {
		return
	}
	client.Write(BlocksyncBlockRequested{
		Height: height,
		PeerID: peerID,
	})
}
