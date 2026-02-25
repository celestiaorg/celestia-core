package mempool

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/p2p"
)

var _ p2p.Wrapper = &Txs{}
var _ p2p.Unwrapper = &Message{}

// Wrap implements the p2p Wrapper interface and wraps a mempool message.
func (m *Txs) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_Txs{Txs: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a mempool seen tx message.
func (m *SeenTx) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_SeenTx{SeenTx: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a mempool want tx message.
func (m *WantTx) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_WantTx{WantTx: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a mempool push tx message.
func (m *PushTx) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_PushTx{PushTx: m}
	return mm
}

// Unwrap implements the p2p Wrapper interface and unwraps a wrapped mempool
// message.
func (m *Message) Unwrap() (proto.Message, error) {
	switch msg := m.Sum.(type) {
	case *Message_Txs:
		return m.GetTxs(), nil
	case *Message_SeenTx:
		return m.GetSeenTx(), nil
	case *Message_WantTx:
		return m.GetWantTx(), nil
	case *Message_PushTx:
		return m.GetPushTx(), nil
	default:
		return nil, fmt.Errorf("unknown message: %T", msg)
	}
}
