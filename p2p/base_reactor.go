package p2p

import (
	"context"
	"fmt"
	"github.com/cometbft/cometbft/libs/trace"
	"reflect"

	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p/conn"
	"github.com/cosmos/gogoproto/proto"
)

// ProcessorFunc is the message processor function type.
type ProcessorFunc func(context.Context, <-chan UnprocessedEnvelope) error

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
	InitPeer(peer Peer) (Peer, error)

	// AddPeer is called by the switch after the peer is added and successfully
	// started. Use it to start goroutines communicating with the peer.
	AddPeer(peer Peer)

	// RemovePeer is called by the switch when the peer is stopped (due to error
	// or other reason).
	RemovePeer(peer Peer, reason interface{})

	// Receive is called by the switch when an envelope is received from any connected
	// peer on any of the channels registered by the reactor
	Receive(Envelope)

	// QueueUnprocessedEnvelope is called by the switch when an unprocessed
	// envelope is received. Unprocessed envelopes are immediately buffered in a
	// queue to avoid blocking. Incoming messages are then passed to a
	// processing function. The default processing function unmarshals the
	// messages in the order the sender sent them and then calls Receive on the
	// reactor. The queue size and the processing function can be changed via
	// passing options to the base reactor.
	QueueUnprocessedEnvelope(e UnprocessedEnvelope)
}

//--------------------------------------

type BaseReactor struct {
	service.BaseService // Provides Start, Stop, .Quit
	Switch              *Switch

	incoming     chan UnprocessedEnvelope
	queueingFunc func(UnprocessedEnvelope)

	ctx    context.Context
	cancel context.CancelFunc
	// processor is called with the incoming channel and is responsible for
	// unmarshalling the messages and calling Receive on the reactor.
	processor ProcessorFunc
	name      string
	tracer    trace.Tracer
}

type ReactorOptions func(*BaseReactor)

func NewBaseReactor(name string, impl Reactor, opts ...ReactorOptions) *BaseReactor {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	base := &BaseReactor{
		ctx:         ctx,
		cancel:      cancel,
		BaseService: *service.NewBaseService(nil, name, impl),
		Switch:      nil,
		incoming:    make(chan UnprocessedEnvelope, 100),
		processor:   DefaultProcessor(impl),
		name:        name,
		tracer:      trace.NoOpTracer(),
	}
	base.queueingFunc = base.QueueUnprocessedEnvelope
	for _, opt := range opts {
		opt(base)
	}

	go func() {
		err := base.processor(ctx, base.incoming)
		if err != nil {
			fmt.Println("processor exited with error ", err)
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
func WithProcessor(processor ProcessorFunc) ReactorOptions {
	return func(br *BaseReactor) {
		br.processor = processor
	}
}

// WithQueueingFunc sets the queuing function to use when receiving a message.
func WithQueueingFunc(queuingFunc func(UnprocessedEnvelope)) ReactorOptions {
	return func(br *BaseReactor) {
		br.queueingFunc = queuingFunc
	}
}

func WithTraceClient(tracer trace.Tracer) ReactorOptions {
	return func(br *BaseReactor) {
		br.tracer = tracer
	}
}

// WithIncomingQueueSize sets the size of the incoming message queue for a
// reactor.
func WithIncomingQueueSize(size int) ReactorOptions {
	return func(br *BaseReactor) {
		br.incoming = make(chan UnprocessedEnvelope, size)
	}
}

// QueueUnprocessedEnvelope is called by the switch when an unprocessed
// envelope is received. Unprocessed envelopes are immediately buffered in a
// queue to avoid blocking. The size of the queue can be changed by passing
// options to the base reactor.
func (br *BaseReactor) QueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	schema.WriteQueueSize(br.tracer, br.name, len(br.incoming))
	select {
	// if the context is done, do nothing.
	case <-br.ctx.Done():
	// if not, add the item to the channel.
	case br.incoming <- e:
	}
}

// TryQueueUnprocessedEnvelope an alternative to QueueUnprocessedEnvelope that attempts to queue an unprocessed envelope.
// If the queue is full, it drops the envelope.
func (br *BaseReactor) TryQueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	select {
	case <-br.ctx.Done():
	case br.incoming <- e:
	default:
	}
}

func (br *BaseReactor) OnStop() {
	br.cancel()
}

// DefaultProcessor unmarshalls the message and calls Receive on the reactor.
// This preserves the sender's original order for all messages.
func DefaultProcessor(impl Reactor) func(context.Context, <-chan UnprocessedEnvelope) error {
	implChannels := impl.GetChannels()

	chIDs := make(map[byte]proto.Message, len(implChannels))
	for _, chDesc := range implChannels {
		chIDs[chDesc.ID] = chDesc.MessageType
	}
	return func(ctx context.Context, incoming <-chan UnprocessedEnvelope) error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case ue, ok := <-incoming:
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
					fmt.Println(fmt.Errorf("unmarshaling message: %v into type: %s resulted in error %w", msg, reflect.TypeOf(mt), err))
					continue
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
				impl.Receive(Envelope{
					ChannelID: ue.ChannelID,
					Src:       ue.Src,
					Message:   msg,
				})
			}
		}
	}
}

func (br *BaseReactor) SetSwitch(sw *Switch) {
	br.Switch = sw
}
func (*BaseReactor) GetChannels() []*conn.ChannelDescriptor { return nil }
func (*BaseReactor) AddPeer(Peer)                           {}
func (*BaseReactor) RemovePeer(Peer, interface{})           {}
func (*BaseReactor) Receive(Envelope)                       {}
func (*BaseReactor) InitPeer(peer Peer) (Peer, error)       { return peer, nil }
