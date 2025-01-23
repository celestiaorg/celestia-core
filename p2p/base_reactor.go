package p2p

import (
	"fmt"
	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/libs/service"
	"github.com/tendermint/tendermint/p2p/conn"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	"reflect"
)

// Reactor is responsible for handling incoming messages on one or more
// Channel. Switch calls GetChannels when reactor is added to it. When a new
// peer joins our node, InitPeer and AddPeer are called. RemovePeer is called
// when the peer is stopped. Receive is called when a message is received on a
// channel associated with this reactor.
//
// Peer#Send or Peer#TrySend should be used to send the message to a peer.
type Reactor interface {
	service.Service // Start, Stop

	// SetSwitch allows setting a switch.
	SetSwitch(*Switch)

	// GetChannels returns the list of MConnection.ChannelDescriptor. Make sure
	// that each ID is unique across all the reactors added to the switch.
	GetChannels() []*conn.ChannelDescriptor

	// InitPeer is called by the switch before the peer is started. Use it to
	// initialize data for the peer (e.g. peer state).
	//
	// NOTE: The switch won't call AddPeer nor RemovePeer if it fails to start
	// the peer. Do not store any data associated with the peer in the reactor
	// itself unless you don't want to have a state, which is never cleaned up.
	InitPeer(peer Peer) Peer

	// AddPeer is called by the switch after the peer is added and successfully
	// started. Use it to start goroutines communicating with the peer.
	AddPeer(peer Peer)

	// RemovePeer is called by the switch when the peer is stopped (due to error
	// or other reason).
	RemovePeer(peer Peer, reason interface{})

	// Receive is called by the switch when msgBytes is received from the peer.
	//
	// NOTE reactor can not keep msgBytes around after Receive completes without
	// copying.
	//
	// CONTRACT: msgBytes are not nil.
	//
	// Only one of Receive or ReceiveEnvelope are called per message. If ReceiveEnvelope
	// is implemented, it will be used, otherwise the switch will fallback to
	// using Receive.
	// Deprecated: Reactors looking to receive data from a peer should implement ReceiveEnvelope.
	// Receive will be deprecated in favor of ReceiveEnvelope in v0.37.
	Receive(chID byte, peer Peer, msgBytes []byte)

	// QueueUnprocessedEnvelope is called by the switch when an unprocessed
	// envelope is received. Unprocessed envelopes are immediately buffered in a
	// queue to avoid blocking. Incoming messages are then passed to a
	// processing function. The default processing function unmarshals the
	// messages in the order the sender sent them and then calls Receive on the
	// reactor. The queue size and the processing function can be changed via
	// passing options to the base reactor.
	QueueUnprocessedEnvelope(e UnprocessedEnvelope)
}

type EnvelopeReceiver interface {
	// ReceiveEnvelope is called by the switch when an envelope is received from any connected
	// peer on any of the channels registered by the reactor.
	//
	// Only one of Receive or ReceiveEnvelope are called per message. If ReceiveEnvelope
	// is implemented, it will be used, otherwise the switch will fallback to
	// using Receive. Receive will be replaced by ReceiveEnvelope in a future version
	ReceiveEnvelope(Envelope)
}

//--------------------------------------

type BaseReactor struct {
	service.BaseService // Provides Start, Stop, .Quit
	Switch              *Switch

	incoming chan UnprocessedEnvelope

	// processor is called with the incoming channel and is responsible for
	// unmarshalling the messages and calling Receive on the reactor.
	processor func(incoming <-chan UnprocessedEnvelope) error
}

type ReactorOptions func(*BaseReactor)

func NewBaseReactor(name string, impl Reactor, opts ...ReactorOptions) *BaseReactor {
	base := &BaseReactor{
		BaseService: *service.NewBaseService(nil, name, impl),
		Switch:      nil,
		incoming:    make(chan UnprocessedEnvelope, 100),
		processor:   DefaultProcessor(impl),
	}
	for _, opt := range opts {
		opt(base)
	}

	go func() {
		err := base.processor(base.incoming)
		if err != nil {
			err = base.Stop()
			if err != nil {
				panic(err)
			}
		}
	}()

	return base
}

// WithProcessor sets the processor function for the reactor. The processor
// function is called with the incoming channel and is responsible for
// unmarshalling the messages and calling Receive on the reactor.
func WithProcessor(processor func(<-chan UnprocessedEnvelope) error) ReactorOptions {
	return func(br *BaseReactor) {
		br.processor = processor
	}
}

// WithIncomingQueueSize sets the size of the incoming message queue for a
// reactor.
func WithIncomingQueueSize(size int) ReactorOptions {
	return func(br *BaseReactor) {
		br.incoming = make(chan UnprocessedEnvelope, size)
	}
}

func (br *BaseReactor) SetSwitch(sw *Switch) {
	br.Switch = sw
}

// QueueUnprocessedEnvelope is called by the switch when an unprocessed
// envelope is received. Unprocessed envelopes are immediately buffered in a
// queue to avoid blocking. The size of the queue can be changed by passing
// options to the base reactor.
func (br *BaseReactor) QueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	br.incoming <- e
}

func (br *BaseReactor) OnStop() {
	close(br.incoming)
}

// DefaultProcessor unmarshalls the message and calls Receive on the reactor.
// This preserves the sender's original order for all messages.
func DefaultProcessor(impl Reactor) func(<-chan UnprocessedEnvelope) error {
	implChannels := impl.GetChannels()

	chIDs := make(map[byte]proto.Message, len(implChannels))
	for _, chDesc := range implChannels {
		chIDs[chDesc.ID] = chDesc.MessageType
	}
	return func(incoming <-chan UnprocessedEnvelope) error {
		for {
			ue, ok := <-incoming
			if !ok {
				// this means the channel was closed.
				return nil
			}
			mt := chIDs[ue.ChannelID]

			if mt == nil {
				return fmt.Errorf("no message type registered for channel %d", ue.ChannelID)
			}

			msg := proto.Clone(mt)

			err := proto.Unmarshal(ue.Message, msg)
			if err != nil {
				return fmt.Errorf("unmarshaling message: %v into type: %s", err, reflect.TypeOf(mt))
			}

			if w, ok := msg.(Unwrapper); ok {
				msg, err = w.Unwrap()
				if err != nil {
					return fmt.Errorf("unwrapping message: %v", err)
				}
			}

			labels := []string{
				"peer_id", string(ue.Src.ID()),
				"chID", fmt.Sprintf("%#x", ue.ChannelID),
			}

			ue.Src.Metrics().PeerReceiveBytesTotal.With(labels...).Add(float64(len(ue.Message)))
			ue.Src.Metrics().MessageReceiveBytesTotal.With(append(labels, "message_type", ue.Src.ValueToMetricLabel(msg))...).Add(float64(len(ue.Message)))
			schema.WriteReceivedBytes(ue.Src.TraceClient(), string(ue.Src.ID()), ue.ChannelID, len(ue.Message))

			if nr, ok := impl.(EnvelopeReceiver); ok {
				nr.ReceiveEnvelope(Envelope{
					ChannelID: ue.ChannelID,
					Src:       ue.Src,
					Message:   msg,
				})
			} else {
				impl.Receive(ue.ChannelID, ue.Src, ue.Message)
			}
		}
	}
}

// ParallelProcessor creates a processor that runs multiple goroutines to
// process incoming messages concurrently. It uses the default processor to
// unmarshal the messages and call Receive on the reactor. This breaks the
// guarantee that messages passed to this reactor are processed in the order
// that the sender sent them.
func ParallelProcessor(impl Reactor, threads int) func(<-chan UnprocessedEnvelope) error {
	return func(incoming <-chan UnprocessedEnvelope) error {
		for i := 0; i < threads; i++ {
			go func() {
				err := DefaultProcessor(impl)(incoming)
				if err != nil {
					err = impl.Stop()
					if err != nil {
						panic(err)
					}
				}
			}()
		}
		return nil
	}
}

func (*BaseReactor) GetChannels() []*conn.ChannelDescriptor        { return nil }
func (*BaseReactor) AddPeer(peer Peer)                             {}
func (*BaseReactor) RemovePeer(peer Peer, reason interface{})      {}
func (*BaseReactor) ReceiveEnvelope(e Envelope)                    {}
func (*BaseReactor) Receive(chID byte, peer Peer, msgBytes []byte) {}
func (*BaseReactor) InitPeer(peer Peer) Peer                       { return peer }
