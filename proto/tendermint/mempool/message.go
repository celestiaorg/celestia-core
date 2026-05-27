package mempool

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/p2p"
)

var (
	_ p2p.Wrapper   = &SeenLargeTx{}
	_ p2p.Wrapper   = &HaveTxChunks{}
	_ p2p.Wrapper   = &WantTxChunks{}
	_ p2p.Wrapper   = &TxChunks{}
	_ p2p.Unwrapper = &Message{}
)

// Wrap implements the p2p Wrapper interface for SeenLargeTx (ADR-013).
func (m *SeenLargeTx) Wrap() proto.Message {
	return &Message{Sum: &Message_SeenLargeTx{SeenLargeTx: m}}
}

// Wrap implements the p2p Wrapper interface for HaveTxChunks (ADR-013).
func (m *HaveTxChunks) Wrap() proto.Message {
	return &Message{Sum: &Message_HaveTxChunks{HaveTxChunks: m}}
}

// Wrap implements the p2p Wrapper interface for WantTxChunks (ADR-013).
func (m *WantTxChunks) Wrap() proto.Message {
	return &Message{Sum: &Message_WantTxChunks{WantTxChunks: m}}
}

// Wrap implements the p2p Wrapper interface for TxChunks (ADR-013).
func (m *TxChunks) Wrap() proto.Message {
	return &Message{Sum: &Message_TxChunks{TxChunks: m}}
}

// Unwrap implements the p2p Wrapper interface and unwraps a wrapped mempool
// message.
func (m *Message) Unwrap() (proto.Message, error) {
	switch msg := m.Sum.(type) {
	case *Message_SeenLargeTx:
		return m.GetSeenLargeTx(), nil
	case *Message_HaveTxChunks:
		return m.GetHaveTxChunks(), nil
	case *Message_WantTxChunks:
		return m.GetWantTxChunks(), nil
	case *Message_TxChunks:
		return m.GetTxChunks(), nil
	default:
		return nil, fmt.Errorf("unknown message: %T", msg)
	}
}
