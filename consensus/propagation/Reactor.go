package propagation

import (
	"fmt"
	"reflect"

	"github.com/cometbft/cometbft/consensus"
	types2 "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/proto/tendermint/propagation"
)

const (
	// TODO: set a valid max msg size
	maxMsgSize = 1048576
)

type Reactor struct {
	p2p.BaseReactor // BaseService + p2p.Switch

	// TODO remove nolint
	//nolint:unused
	conS *consensus.State
}

func NewReactor(consensusState *consensus.State, options ...ReactorOption) *Reactor {
	reactor := &Reactor{}
	reactor.BaseReactor = *p2p.NewBaseReactor("BlockProp", reactor)

	for _, option := range options {
		option(reactor)
	}
	return reactor
}

type ReactorOption func(*Reactor)

func (blockProp *Reactor) OnStart() error {
	// TODO: implement
	return nil
}

func (blockProp *Reactor) OnStop() {
	// TODO: implement
}

func GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			// TODO: set better values
			ID:                  consensus.PropagationChannel,
			Priority:            6,
			SendQueueCapacity:   100,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propagation.Message{},
		},
	}
}

func (blockProp *Reactor) AddPeer(peer p2p.Peer) {
	// TODO: implement
}

func (blockProp *Reactor) ReceiveEnvelope(e p2p.Envelope) {
	if !blockProp.IsRunning() {
		blockProp.Logger.Debug("Receive", "src", e.Src, "chId", e.ChannelID)
		return
	}

	m := e.Message
	if wm, ok := m.(p2p.Wrapper); ok {
		m = wm.Wrap()
	}
	msg, err := types2.MsgFromProto(m.(*propagation.Message))
	if err != nil {
		blockProp.Logger.Error("Error decoding message", "src", e.Src, "chId", e.ChannelID, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err)
		return
	}

	if err = msg.ValidateBasic(); err != nil {
		blockProp.Logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err)
		return
	}
	switch e.ChannelID {
	case consensus.PropagationChannel:
		switch msg := msg.(type) {
		case *types2.TxMetaData:
			// TODO: implement
		case *types2.CompactBlock:
			// TODO: implement
		case *types2.PartMetaData:
			// TODO: implement
		case *types2.HaveParts:
			// TODO: implement
		case *types2.WantParts:
			// TODO: implement
		case *types2.RecoveryPart:
			// TODO: implement
		default:
			blockProp.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
		}
	default:
		blockProp.Logger.Error(fmt.Sprintf("Unknown chId %X", e.ChannelID))
	}
}

func (blockProp *Reactor) Receive(e p2p.Envelope) {
	blockProp.ReceiveEnvelope(e)
}
