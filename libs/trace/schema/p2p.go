package schema

import "github.com/cometbft/cometbft/libs/trace"

// P2PTables returns the list of tables that are used for p2p tracing.
func P2PTables() []string {
	return []string{
		PeersTable,
		PendingBytesTable,
		ReceivedBytesTable,
		P2PBufferPoolTable,
	}
}

const (
	// PeerUpdateTable is the name of the table that stores the p2p peer
	// updates.
	PeersTable = "peers"
)

// P2PPeerUpdate is an enum that represents the different types of p2p
// trace data.
type P2PPeerUpdate string

const (
	// PeerJoin is the action for when a peer is connected.
	PeerJoin P2PPeerUpdate = "connect"
	// PeerDisconnect is the action for when a peer is disconnected.
	PeerDisconnect P2PPeerUpdate = "disconnect"
)

// PeerUpdate describes schema for the "peer_update" table.
type PeerUpdate struct {
	PeerID string `json:"peer_id"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// Table returns the table name for the PeerUpdate struct.
func (PeerUpdate) Table() string {
	return PeersTable
}

// WritePeerUpdate writes a tracing point for a peer update using the predetermined
// schema for p2p tracing.
func WritePeerUpdate(client trace.Tracer, peerID string, action P2PPeerUpdate, reason string) {
	client.Write(PeerUpdate{PeerID: peerID, Action: string(action), Reason: reason})
}

const (
	PendingBytesTable = "pending_bytes"
)

type PendingBytes struct {
	PeerID string       `json:"peer_id"`
	Bytes  map[byte]int `json:"bytes"`
}

func (PendingBytes) Table() string {
	return PendingBytesTable
}

func WritePendingBytes(client trace.Tracer, peerID string, bytes map[byte]int) {
	client.Write(PendingBytes{PeerID: peerID, Bytes: bytes})
}

const (
	ReceivedBytesTable = "received_bytes"
)

type ReceivedBytes struct {
	PeerID  string `json:"peer_id"`
	Channel byte   `json:"channel"`
	Bytes   int    `json:"bytes"`
}

func (ReceivedBytes) Table() string {
	return ReceivedBytesTable
}

func WriteReceivedBytes(client trace.Tracer, peerID string, channel byte, bytes int) {
	client.Write(ReceivedBytes{PeerID: peerID, Channel: channel, Bytes: bytes})
}

// Schema constants for p2p buffer pool tracing.
const (
	// P2PBufferPoolTable stores traces of buffer pool operations (get and put).
	P2PBufferPoolTable = "p2p_buffer_pool"
)

// P2PBufferPoolOperation is an enum that represents the different types of buffer pool operations.
type P2PBufferPoolOperation string

const (
	// P2PBufferPoolGet is the action for getting a buffer from the pool.
	P2PBufferPoolGet P2PBufferPoolOperation = "get"
	// P2PBufferPoolPut is the action for returning a buffer to the pool.
	P2PBufferPoolPut P2PBufferPoolOperation = "put"
	// P2PBufferPoolCreate is the action for creating a buffer pool for a peer connection.
	P2PBufferPoolCreate P2PBufferPoolOperation = "create"
	// P2PBufferPoolNil is traced when buffer pool is unexpectedly nil during receive.
	P2PBufferPoolNil P2PBufferPoolOperation = "nil"
)

// P2PBufferPool describes schema for the "p2p_buffer_pool" table.
// This event is traced when a buffer is retrieved from or returned to the connection pool.
type P2PBufferPool struct {
	Action    string `json:"action"`     // "get", "put", "create", or "nil"
	MinCap    int    `json:"min_cap"`    // For get: requested minimum capacity. For others: 0
	BufferCap int    `json:"buffer_cap"` // For get: actual capacity returned. For put: capacity being returned. For others: 0
	Discarded bool   `json:"discarded"`  // For put: whether buffer was discarded. For others: false
	ChannelID int    `json:"channel_id"` // Channel ID (0 for create/nil if not channel-specific)
	PeerID    string `json:"peer_id,omitempty"` // Peer ID (for create/nil operations)
}

// Table returns the table name for the P2PBufferPool struct.
func (P2PBufferPool) Table() string {
	return P2PBufferPoolTable
}

// WriteP2PBufferPoolGet writes a tracing point for getting a buffer from the pool.
func WriteP2PBufferPoolGet(client trace.Tracer, minCap, actualCap, channelID int) {
	if client == nil || !client.IsCollecting(P2PBufferPoolTable) {
		return
	}
	client.Write(P2PBufferPool{
		Action:    string(P2PBufferPoolGet),
		MinCap:    minCap,
		BufferCap: actualCap,
		Discarded: false,
		ChannelID: channelID,
	})
}

// WriteP2PBufferPoolPut writes a tracing point for returning a buffer to the pool.
func WriteP2PBufferPoolPut(client trace.Tracer, bufferCap, channelID int, discarded bool) {
	if client == nil || !client.IsCollecting(P2PBufferPoolTable) {
		return
	}
	client.Write(P2PBufferPool{
		Action:    string(P2PBufferPoolPut),
		MinCap:    0,
		BufferCap: bufferCap,
		Discarded: discarded,
		ChannelID: channelID,
	})
}

// WriteP2PBufferPoolCreate writes a tracing point for creating a buffer pool.
func WriteP2PBufferPoolCreate(client trace.Tracer, peerID string) {
	if client == nil || !client.IsCollecting(P2PBufferPoolTable) {
		return
	}
	client.Write(P2PBufferPool{
		Action:    string(P2PBufferPoolCreate),
		MinCap:    0,
		BufferCap: 0,
		Discarded: false,
		ChannelID: 0,
		PeerID:    peerID,
	})
}

// WriteP2PBufferPoolNil writes a tracing point when buffer pool is unexpectedly nil.
func WriteP2PBufferPoolNil(client trace.Tracer, channelID int, msgSize int) {
	if client == nil || !client.IsCollecting(P2PBufferPoolTable) {
		return
	}
	client.Write(P2PBufferPool{
		Action:    string(P2PBufferPoolNil),
		MinCap:    msgSize, // Store message size in MinCap field
		BufferCap: 0,
		Discarded: false,
		ChannelID: channelID,
		PeerID:    "",
	})
}
