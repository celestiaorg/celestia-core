package mempool

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/p2p"
)

var _ p2p.Wrapper = &Txs{}
var _ p2p.Wrapper = &TxManifest{}
var _ p2p.Wrapper = &WantChunk{}
var _ p2p.Wrapper = &TxChunk{}
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

// Wrap implements the p2p Wrapper interface and wraps a mempool tx manifest message.
func (m *TxManifest) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_TxManifest{TxManifest: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a mempool want chunk message.
func (m *WantChunk) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_WantChunk{WantChunk: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a mempool tx chunk message.
func (m *TxChunk) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_TxChunk{TxChunk: m}
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
	case *Message_TxManifest:
		return m.GetTxManifest(), nil
	case *Message_WantChunk:
		return m.GetWantChunk(), nil
	case *Message_TxChunk:
		return m.GetTxChunk(), nil
	default:
		return nil, fmt.Errorf("unknown message: %T", msg)
	}
}
