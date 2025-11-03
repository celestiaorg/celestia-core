package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

// BlocksyncTables returns the list of tables that are used for blocksync
// tracing.
func BlocksyncTables() []string {
	return []string{
		BlocksyncBlockReceivedTable,
		BlocksyncBlockRequestTable,
		BlocksyncBlockSavedTable,
		BlocksyncMessagePoolTable,
		BlocksyncBlockRequestLatencyTable,
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

// Schema constants for blocksync block request table.
const (
	// BlocksyncBlockRequestTable stores traces of block requests sent to peers.
	BlocksyncBlockRequestTable = "blocksync_block_request"
)

// BlocksyncBlockRequest describes schema for the "blocksync_block_request" table.
// This event is traced when we request a block from a peer.
type BlocksyncBlockRequest struct {
	Height  int64  `json:"height"`
	PeerID  string `json:"peer_id"`
	CurRate int64  `json:"cur_rate"`
}

// Table returns the table name for the BlocksyncBlockRequest struct.
func (BlocksyncBlockRequest) Table() string {
	return BlocksyncBlockRequestTable
}

// WriteBlocksyncBlockRequest writes a tracing point for a block request sent to a peer.
func WriteBlocksyncBlockRequest(client trace.Tracer, height int64, peerID string, curRate int64) {
	if !client.IsCollecting(BlocksyncBlockRequestTable) {
		return
	}
	client.Write(BlocksyncBlockRequest{
		Height:  height,
		PeerID:  peerID,
		CurRate: curRate,
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
	NextAvailable      bool  `json:"next_available"`
	ProcessingTimeMs   int64 `json:"processing_time_ms"`    // Time from PeekTwoBlocks to database save in milliseconds
	ValidationTimeMs   int64 `json:"validation_time_ms"`    // Time spent in validation
	DatabaseSaveTimeMs int64 `json:"database_save_time_ms"` // Time spent saving to database
}

// Table returns the table name for the BlocksyncBlockSaved struct.
func (BlocksyncBlockSaved) Table() string {
	return BlocksyncBlockSavedTable
}

// WriteBlocksyncBlockSaved writes a tracing point for a successfully validated and saved block.
func WriteBlocksyncBlockSaved(client trace.Tracer, height int64, blockSize int, nextAvailable bool, processingTimeMs, validationTimeMs, databaseSaveTimeMs int64) {
	if !client.IsCollecting(BlocksyncBlockSavedTable) {
		return
	}
	client.Write(BlocksyncBlockSaved{
		Height:             height,
		BlockSize:          blockSize,
		NextAvailable:      nextAvailable,
		ProcessingTimeMs:   processingTimeMs,
		ValidationTimeMs:   validationTimeMs,
		DatabaseSaveTimeMs: databaseSaveTimeMs,
	})
}

// Schema constants for blocksync message pool tracing.
const (
	// BlocksyncMessagePoolTable stores traces of message pool operations (get and put).
	BlocksyncMessagePoolTable = "blocksync_message_pool"
)

// BlocksyncMessagePoolOperation is an enum that represents the different types of message pool operations.
type BlocksyncMessagePoolOperation string

const (
	// BlocksyncMessagePoolOpGet is the action for getting a message from the pool.
	BlocksyncMessagePoolOpGet BlocksyncMessagePoolOperation = "get"
	// BlocksyncMessagePoolOpPut is the action for returning a message to the pool.
	BlocksyncMessagePoolOpPut BlocksyncMessagePoolOperation = "put"
)

// BlocksyncMessagePool describes schema for the "blocksync_message_pool" table.
// This event is traced when a message is retrieved from or returned to the pool.
type BlocksyncMessagePool struct {
	Action      string `json:"action"` // "get" or "put"
	ChannelID   byte   `json:"channel_id"`
	MessageType string `json:"message_type"` // For put: type of message being returned. For get: empty string
}

// Table returns the table name for the BlocksyncMessagePool struct.
func (BlocksyncMessagePool) Table() string {
	return BlocksyncMessagePoolTable
}

// WriteBlocksyncMessagePoolGet writes a tracing point for getting a message from the pool.
func WriteBlocksyncMessagePoolGet(client trace.Tracer, channelID byte) {
	if client == nil || !client.IsCollecting(BlocksyncMessagePoolTable) {
		return
	}
	client.Write(BlocksyncMessagePool{
		Action:      string(BlocksyncMessagePoolOpGet),
		ChannelID:   channelID,
		MessageType: "",
	})
}

// WriteBlocksyncMessagePoolPut writes a tracing point for returning a message to the pool.
func WriteBlocksyncMessagePoolPut(client trace.Tracer, channelID byte, messageType string) {
	if client == nil || !client.IsCollecting(BlocksyncMessagePoolTable) {
		return
	}
	client.Write(BlocksyncMessagePool{
		Action:      string(BlocksyncMessagePoolOpPut),
		ChannelID:   channelID,
		MessageType: messageType,
	})
}

// Schema constants for blocksync block request latency table.
const (
	// BlocksyncBlockRequestLatencyTable stores traces of latency from request to receive.
	BlocksyncBlockRequestLatencyTable = "blocksync_block_request_latency"
)

// BlocksyncBlockRequestLatency describes schema for the "blocksync_block_request_latency" table.
// This event is traced when a block is received to measure the latency from when
// the request was sent to when the response was received.
type BlocksyncBlockRequestLatency struct {
	Height    int64  `json:"height"`
	PeerID    string `json:"peer_id"`
	LatencyMs int64  `json:"latency_ms"` // Time from request sent to block received in milliseconds
}

// Table returns the table name for the BlocksyncBlockRequestLatency struct.
func (BlocksyncBlockRequestLatency) Table() string {
	return BlocksyncBlockRequestLatencyTable
}

// WriteBlocksyncBlockRequestLatency writes a tracing point for block request latency.
func WriteBlocksyncBlockRequestLatency(client trace.Tracer, height int64, peerID string, latencyMs int64) {
	if client == nil || !client.IsCollecting(BlocksyncBlockRequestLatencyTable) {
		return
	}
	client.Write(BlocksyncBlockRequestLatency{
		Height:    height,
		PeerID:    peerID,
		LatencyMs: latencyMs,
	})
}
