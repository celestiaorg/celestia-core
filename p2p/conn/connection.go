package conn

import (
	"fmt"
	"time"

	"github.com/gogo/protobuf/proto"

	tmp2p "github.com/tendermint/tendermint/proto/tendermint/p2p"
)

const (
	defaultMaxPacketMsgPayloadSize = 1024

	numBatchPacketMsgs = 10
	minReadBufferSize  = 1024
	minWriteBufferSize = 65536
	updateStats        = 2 * time.Second

	// some of these defaults are written in the user config
	// flushThrottle, sendRate, recvRate
	// TODO: remove values present in config
	defaultFlushThrottle = 100 * time.Millisecond

	defaultSendQueueCapacity   = 1
	defaultRecvBufferCapacity  = 4096
	defaultRecvMessageCapacity = 22020096      // 21MB
	defaultSendRate            = int64(512000) // 500KB/s
	defaultRecvRate            = int64(512000) // 500KB/s
	defaultSendTimeout         = 10 * time.Second
	defaultPingInterval        = 60 * time.Second
	defaultPongTimeout         = 45 * time.Second
)

type receiveCbFunc func(chID byte, msgBytes []byte)
type errorCbFunc func(interface{})

//-----------------------------------------------------------------------------

type ChannelDescriptor struct {
	ID                  byte
	Priority            int
	SendQueueCapacity   int
	RecvBufferCapacity  int
	RecvMessageCapacity int
	MessageType         proto.Message
}

func (chDesc ChannelDescriptor) FillDefaults() (filled ChannelDescriptor) {
	if chDesc.SendQueueCapacity == 0 {
		chDesc.SendQueueCapacity = defaultSendQueueCapacity
	}
	if chDesc.RecvBufferCapacity == 0 {
		chDesc.RecvBufferCapacity = defaultRecvBufferCapacity
	}
	if chDesc.RecvMessageCapacity == 0 {
		chDesc.RecvMessageCapacity = defaultRecvMessageCapacity
	}
	filled = chDesc
	return
}

//----------------------------------------
// Packet

// mustWrapPacket takes a packet kind (oneof) and wraps it in a tmp2p.Packet message.
func mustWrapPacket(pb proto.Message) *tmp2p.Packet {
	var msg tmp2p.Packet

	switch pb := pb.(type) {
	case *tmp2p.Packet: // already a packet
		msg = *pb
	case *tmp2p.PacketPing:
		msg = tmp2p.Packet{
			Sum: &tmp2p.Packet_PacketPing{
				PacketPing: pb,
			},
		}
	case *tmp2p.PacketPong:
		msg = tmp2p.Packet{
			Sum: &tmp2p.Packet_PacketPong{
				PacketPong: pb,
			},
		}
	case *tmp2p.PacketMsg:
		msg = tmp2p.Packet{
			Sum: &tmp2p.Packet_PacketMsg{
				PacketMsg: pb,
			},
		}
	default:
		panic(fmt.Errorf("unknown packet type %T", pb))
	}

	return &msg
}
