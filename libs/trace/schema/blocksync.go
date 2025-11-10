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
		BlocksyncBlockValidatedTable,
		BlocksyncBatchSavedTable,
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

// Schema constants for blocksync block validated table.
const (
	// BlocksyncBlockValidatedTable stores traces of block validation timing.
	BlocksyncBlockValidatedTable = "blocksync_block_validated"
)

// BlocksyncBlockValidated describes schema for the "blocksync_block_validated" table.
// This event is traced after a block is successfully validated (before saving).
type BlocksyncBlockValidated struct {
	Height             int64 `json:"height"`
	BlockSize          int   `json:"block_size"`
	ValidationDuration int64 `json:"validation_duration"` // Duration in milliseconds
}

// Table returns the table name for the BlocksyncBlockValidated struct.
func (BlocksyncBlockValidated) Table() string {
	return BlocksyncBlockValidatedTable
}

// WriteBlocksyncBlockValidated writes a tracing point for a validated block.
func WriteBlocksyncBlockValidated(client trace.Tracer, height int64, blockSize int, validationDuration int64) {
	if !client.IsCollecting(BlocksyncBlockValidatedTable) {
		return
	}
	client.Write(BlocksyncBlockValidated{
		Height:             height,
		BlockSize:          blockSize,
		ValidationDuration: validationDuration,
	})
}

// Schema constants for blocksync batch saved table.
const (
	// BlocksyncBatchSavedTable stores traces of batch save operations.
	BlocksyncBatchSavedTable = "blocksync_batch_saved"
)

// BlocksyncBatchSaved describes schema for the "blocksync_batch_saved" table.
// This event is traced when a batch of blocks is saved to disk.
type BlocksyncBatchSaved struct {
	StartHeight  int64 `json:"start_height"`  // First block height in batch
	EndHeight    int64 `json:"end_height"`    // Last block height in batch
	NumBlocks    int   `json:"num_blocks"`    // Number of blocks in batch
	TotalSize    int   `json:"total_size"`    // Total size of all blocks in bytes
	SaveDuration int64 `json:"save_duration"` // Duration in milliseconds
}

// Table returns the table name for the BlocksyncBatchSaved struct.
func (BlocksyncBatchSaved) Table() string {
	return BlocksyncBatchSavedTable
}

// WriteBlocksyncBatchSaved writes a tracing point for a batch save operation.
func WriteBlocksyncBatchSaved(client trace.Tracer, startHeight, endHeight int64, numBlocks, totalSize int, saveDuration int64) {
	if !client.IsCollecting(BlocksyncBatchSavedTable) {
		return
	}
	client.Write(BlocksyncBatchSaved{
		StartHeight:  startHeight,
		EndHeight:    endHeight,
		NumBlocks:    numBlocks,
		TotalSize:    totalSize,
		SaveDuration: saveDuration,
	})
}
