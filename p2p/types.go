package p2p

import (
	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/p2p/conn"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

type ChannelDescriptor = conn.ChannelDescriptor
type ConnectionStatus = conn.ConnectionStatus

type UnprocessedEnvelope struct {
	Src       IntrospectivePeer
	Message   []byte
	ChannelID byte

	// ReturnBuffer is called after Message is processed to return
	// the buffer to the connection's buffer pool (if configured).
	// This callback MUST be called exactly once via defer to prevent leaks.
	// Can be nil if no buffer pool is configured.
	ReturnBuffer func()
}

// Envelope contains a message with sender routing info.
type Envelope struct {
	Src       Peer          // sender (empty if outbound)
	Message   proto.Message // message payload
	ChannelID byte
}

// Unwrapper is a Protobuf message that can contain a variety of inner messages
// (e.g. via oneof fields). If a Channel's message type implements Unwrapper, the
// p2p layer will automatically unwrap inbound messages so that reactors do not have to do this themselves.
type Unwrapper interface {
	proto.Message

	// Unwrap will unwrap the inner message contained in this message.
	Unwrap() (proto.Message, error)
}

// Wrapper is a companion type to Unwrapper. It is a Protobuf message that can contain a variety of inner messages. The p2p layer will automatically wrap outbound messages so that the reactors do not have to do it themselves.
type Wrapper interface {
	proto.Message

	// Wrap will take the underlying message and wrap it in its wrapper type.
	Wrap() proto.Message
}

var (
	_ Wrapper = &tmp2p.PexRequest{}
	_ Wrapper = &tmp2p.PexAddrs{}
)
