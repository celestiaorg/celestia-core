package mempool

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/p2p"
)

var (
	_ p2p.Wrapper   = &Txs{}
	_ p2p.Wrapper   = &SeenLargeTx{}
	_ p2p.Wrapper   = &HaveTxChunks{}
	_ p2p.Wrapper   = &WantTxChunks{}
	_ p2p.Wrapper   = &TxChunk{}
	_ p2p.Unwrapper = &Message{}
)

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

// Wrap implements the p2p Wrapper interface for SeenLargeTx (ADR-012).
func (m *SeenLargeTx) Wrap() proto.Message {
	return &Message{Sum: &Message_SeenLargeTx{SeenLargeTx: m}}
}

// Wrap implements the p2p Wrapper interface for HaveTxChunks (ADR-012).
func (m *HaveTxChunks) Wrap() proto.Message {
	return &Message{Sum: &Message_HaveTxChunks{HaveTxChunks: m}}
}

// Wrap implements the p2p Wrapper interface for WantTxChunks (ADR-012).
func (m *WantTxChunks) Wrap() proto.Message {
	return &Message{Sum: &Message_WantTxChunks{WantTxChunks: m}}
}

// Wrap implements the p2p Wrapper interface for TxChunk (ADR-012).
func (m *TxChunk) Wrap() proto.Message {
	return &Message{Sum: &Message_TxChunk{TxChunk: m}}
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
	case *Message_SeenLargeTx:
		return m.GetSeenLargeTx(), nil
	case *Message_HaveTxChunks:
		return m.GetHaveTxChunks(), nil
	case *Message_WantTxChunks:
		return m.GetWantTxChunks(), nil
	case *Message_TxChunk:
		return m.GetTxChunk(), nil
	default:
		return nil, fmt.Errorf("unknown message: %T", msg)
	}
}
