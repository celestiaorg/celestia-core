package headersync

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/p2p"
)

var _ p2p.Wrapper = &StatusResponse{}
var _ p2p.Wrapper = &GetHeaders{}
var _ p2p.Wrapper = &HeadersResponse{}

func (m *StatusResponse) Wrap() proto.Message {
	hm := &Message{}
	hm.Sum = &Message_StatusResponse{StatusResponse: m}
	return hm
}

func (m *GetHeaders) Wrap() proto.Message {
	hm := &Message{}
	hm.Sum = &Message_GetHeaders{GetHeaders: m}
	return hm
}

func (m *HeadersResponse) Wrap() proto.Message {
	hm := &Message{}
	hm.Sum = &Message_HeadersResponse{HeadersResponse: m}
	return hm
}

// Unwrap implements the p2p Wrapper interface and unwraps a wrapped headersync
// message.
func (m *Message) Unwrap() (proto.Message, error) {
	switch msg := m.Sum.(type) {
	case *Message_StatusResponse:
		return m.GetStatusResponse(), nil

	case *Message_GetHeaders:
		return m.GetGetHeaders(), nil

	case *Message_HeadersResponse:
		return m.GetHeadersResponse(), nil

	default:
		return nil, fmt.Errorf("unknown message: %T", msg)
	}
}
