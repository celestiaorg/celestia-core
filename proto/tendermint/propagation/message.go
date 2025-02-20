package propagation

import (
	fmt "fmt"

	"github.com/cometbft/cometbft/p2p"
	"github.com/gogo/protobuf/proto"
)

var (
	_ p2p.Wrapper   = &HaveParts{}
	_ p2p.Wrapper   = &WantParts{}
	_ p2p.Wrapper   = &RecoveryPart{}
	_ p2p.Unwrapper = &Message{}
)

// Wrap implements the p2p Wrapper interface and wraps a propagation message.
func (m *HaveParts) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_HaveParts{HaveParts: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a propagation message.
func (m *WantParts) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_WantParts{WantParts: m}
	return mm
}

// Wrap implements the p2p Wrapper interface and wraps a propagation message.
func (m *RecoveryPart) Wrap() proto.Message {
	mm := &Message{}
	mm.Sum = &Message_RecoveryPart{RecoveryPart: m}
	return mm
}

// Unwrap implements the p2p Wrapper interface and unwraps a wrapped mempool
// message.
func (m *Message) Unwrap() (proto.Message, error) {
	switch msg := m.Sum.(type) {
	case *Message_HaveParts:
		return m.GetHaveParts(), nil

	case *Message_WantParts:
		return m.GetWantParts(), nil

	case *Message_RecoveryPart:
		return m.GetRecoveryPart(), nil

	default:
		return nil, fmt.Errorf("unknown message: %T", msg)
	}
}
