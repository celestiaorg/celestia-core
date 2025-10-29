package blocksync

import (
	"sync"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// Protobuf field numbers for the Message oneof wrapper
	FieldBlockRequest    = 1
	FieldNoBlockResponse = 2
	FieldBlockResponse   = 3
	FieldStatusRequest   = 4
	FieldStatusResponse  = 5
)

// MessagePool pools BlockResponse messages to reduce allocations during unmarshaling.
// BlockResponse is by far the largest message type and contains the block data
// with transactions, evidence, and commits that cause the most memory allocation.
// When proto.Unmarshal unmarshals into a pre-allocated message, it reuses existing
// slice capacity instead of allocating new slices, significantly reducing memory pressure.
type MessagePool struct {
	blockResponsePool sync.Pool
	traceClient       trace.Tracer
}

// NewMessagePool creates a new message pool for blocksync BlockResponse messages.
func NewMessagePool(traceClient trace.Tracer) *MessagePool {
	return &MessagePool{
		blockResponsePool: sync.Pool{
			New: func() interface{} {
				return &bcproto.BlockResponse{}
			},
		},
		traceClient: traceClient,
	}
}

// GetBlockResponse gets a BlockResponse message from the pool.
func (mp *MessagePool) GetBlockResponse() *bcproto.BlockResponse {
	return mp.blockResponsePool.Get().(*bcproto.BlockResponse)
}

// PutBlockResponse returns a BlockResponse message to the pool.
func (mp *MessagePool) PutBlockResponse(msg *bcproto.BlockResponse) {
	if msg == nil {
		return
	}
	msg.Reset()
	mp.blockResponsePool.Put(msg)
}

// peekMessageType reads the protobuf field tag from wire format to determine message type
// without unmarshaling the entire message. Returns the field number (1-5) or 0 if unknown.
func (mp *MessagePool) peekMessageType(data []byte) int {
	if len(data) == 0 {
		return 0
	}

	// Read just the field tag from the wire format (typically 1-2 bytes)
	fieldNum, wireType, n := protowire.ConsumeTag(data)
	if n < 0 {
		return 0 // Invalid tag
	}

	// Verify it's a length-delimited field (wireType 2, used by all Message types)
	if wireType != protowire.BytesType {
		return 0 // Unexpected wire type
	}

	return int(fieldNum)
}

// GetMessageByChannel returns a bcproto.Message wrapper for the blocksync channel.
// It peeks at the wire format to determine message type:
//   - If BlockResponse (field 3): returns wrapper with pre-allocated BlockResponse from pool
//   - For other types: returns nil (caller should allocate normally)
//
// This eliminates the memory leak where we pre-allocated BlockResponse for ALL messages
// but only ~15% were actually BlockResponse (the rest leaked when protobuf replaced Sum field).
func (mp *MessagePool) GetMessageByChannel(channelID byte, data []byte) proto.Message {
	// Only pool messages on the BlocksyncChannel (0x40)
	if channelID != BlocksyncChannel {
		return nil
	}

	// Peek at wire format to determine message type
	fieldNum := mp.peekMessageType(data)

	// Only get from pool if it's actually a BlockResponse
	if fieldNum == FieldBlockResponse {
		// Trace: getting message from pool
		schema.WriteBlocksyncMessagePoolGet(mp.traceClient, channelID)

		// Return a Message wrapper with pre-allocated BlockResponse from pool
		return &bcproto.Message{
			Sum: &bcproto.Message_BlockResponse{
				BlockResponse: mp.GetBlockResponse(),
			},
		}
	}

	// For other message types (BlockRequest, StatusRequest, etc.), return nil
	// Caller will allocate normally, avoiding the leak
	return nil
}

// PutMessageByChannel returns a BlockResponse message to the pool.
// It accepts either:
// 1. The Message wrapper (bcproto.Message) - extracts BlockResponse from Sum field
// 2. The unwrapped BlockResponse directly
func (mp *MessagePool) PutMessageByChannel(channelID byte, msg proto.Message) {
	if msg == nil {
		return
	}

	// Check if it's the Message wrapper
	if wrapper, ok := msg.(*bcproto.Message); ok {
		// Extract BlockResponse from the wrapper's Sum field
		if brMsg, ok := wrapper.GetSum().(*bcproto.Message_BlockResponse); ok && brMsg.BlockResponse != nil {
			mp.PutBlockResponse(brMsg.BlockResponse)
			// Trace: returning message to pool
			schema.WriteBlocksyncMessagePoolPut(mp.traceClient, channelID, "BlockResponse")
		}
		// Note: If Sum was replaced by protobuf (BlockRequest, etc.), the original
		// BlockResponse is lost and cannot be recovered
		return
	}

	// Check if it's an unwrapped BlockResponse
	if blockResp, ok := msg.(*bcproto.BlockResponse); ok {
		mp.PutBlockResponse(blockResp)
		// Trace: returning message to pool
		schema.WriteBlocksyncMessagePoolPut(mp.traceClient, channelID, "BlockResponse")
	}
}
