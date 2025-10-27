package blocksync

import (
	"sync"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	"github.com/cosmos/gogoproto/proto"
)

// MessagePool pools BlockResponse messages to reduce allocations during unmarshaling.
// BlockResponse is by far the largest message type and contains the block data
// with transactions, evidence, and commits that cause the most memory allocation.
// When proto.Unmarshal unmarshals into a pre-allocated message, it reuses existing
// slice capacity instead of allocating new slices, significantly reducing memory pressure.
type MessagePool struct {
	blockResponsePool sync.Pool
}

// NewMessagePool creates a new message pool for blocksync BlockResponse messages.
func NewMessagePool() *MessagePool {
	return &MessagePool{
		blockResponsePool: sync.Pool{
			New: func() interface{} {
				return &bcproto.BlockResponse{}
			},
		},
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

// GetMessageByChannel returns a bcproto.Message wrapper with a pre-allocated
// BlockResponse from the pool for the blocksync channel.
// The wrapper's Sum field is pre-populated with a pooled BlockResponse.
// When protobuf unmarshals:
//   - If it's a BlockResponse: reuses the pooled message's slice capacity
//   - If it's another type (BlockRequest, etc.): protobuf replaces Sum with correct type
func (mp *MessagePool) GetMessageByChannel(channelID byte) proto.Message {
	// Only pool messages on the BlocksyncChannel (0x40)
	if channelID != BlocksyncChannel {
		return nil
	}

	// Return a Message wrapper with pre-allocated BlockResponse from pool
	return &bcproto.Message{
		Sum: &bcproto.Message_BlockResponse{
			BlockResponse: mp.GetBlockResponse(),
		},
	}
}

// PutMessageByChannel returns a BlockResponse message to the pool.
// Only BlockResponse messages are pooled; other message types are ignored.
func (mp *MessagePool) PutMessageByChannel(channelID byte, msg proto.Message) {
	if msg == nil {
		return
	}

	// Only pool BlockResponse messages
	if blockResp, ok := msg.(*bcproto.BlockResponse); ok {
		mp.PutBlockResponse(blockResp)
	}
	// Other message types are not pooled and will be garbage collected normally
}
