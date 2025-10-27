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

// GetMessageByChannel returns a pooled BlockResponse message for the blocksync channel.
// Returns nil for other channels (they will be allocated normally via proto.Clone).
// We only pool BlockResponse because it's by far the largest and most frequent message,
// containing block data with transactions, evidence, and commits.
func (mp *MessagePool) GetMessageByChannel(channelID byte) proto.Message {
	// Only pool messages on the BlocksyncChannel (0x40)
	if channelID != BlocksyncChannel {
		return nil
	}

	// Return BlockResponse for pooling
	// If unmarshal fails (wrong type), base reactor will fall back to proto.Clone
	return mp.GetBlockResponse()
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
