package p2p

import (
	"context"
	"fmt"
	"github.com/cometbft/cometbft/libs/trace"

	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p/conn"
	"github.com/cosmos/gogoproto/proto"
)

// ProcessorFunc is the message processor function type.
type ProcessorFunc func(context.Context, <-chan UnmarshalResult)

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

type UnmarshalResult struct {
	Src           IntrospectivePeer
	Msg           proto.Message
	BytesReceived int
	ChannelID     byte
	Err           error
}

type BaseReactor struct {
	service.BaseService // Provides Start, Stop, .Quit
	Switch              *Switch

	incoming     chan UnmarshalResult
	queueingFunc func(UnprocessedEnvelope)

	ctx    context.Context
	cancel context.CancelFunc
	chIDs  map[byte]proto.Message
	// processor is called with the incoming channel and is responsible for
	// unmarshalling the messages and calling Receive on the reactor.
	processor   ProcessorFunc
	name        string
	traceClient trace.Tracer
}

type ReactorOptions func(*BaseReactor)

func NewBaseReactor(name string, impl Reactor, opts ...ReactorOptions) *BaseReactor {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	implChannels := impl.GetChannels()

	chIDs := make(map[byte]proto.Message, len(implChannels))
	for _, chDesc := range implChannels {
		chIDs[chDesc.ID] = chDesc.MessageType
	}
	base := &BaseReactor{
		ctx:         ctx,
		cancel:      cancel,
		BaseService: *service.NewBaseService(nil, name, impl),
		Switch:      nil, // set by the switch later
		incoming:    make(chan UnmarshalResult, 100),
		chIDs:       chIDs,
		processor:   nil, // Will be set after base is created
		name:        name,
		traceClient: trace.NoOpTracer(),
	}
	base.queueingFunc = base.QueueUnprocessedEnvelope
	for _, opt := range opts {
		opt(base)
	}

	// Set the processor after base is created, only if it hasn't been set by options
	if base.processor == nil {
		base.processor = ProcessorWithReactor(impl, base)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				base.Logger.Error("processor panicked", "err", r)
				// Try to stop the reactor gracefully only if it's running
				if base.IsRunning() {
					if err := base.Stop(); err != nil {
						base.Logger.Error("failed to stop reactor after panic", "err", err)
					}
				}
			}
		}()

		base.processor(ctx, base.incoming)
	}()

	return base
}

// WithProcessor sets the processor function for the reactor. The processor
// function is called with the incoming channel and is responsible for
// calling Receive on the reactor.
func WithProcessor(processor ProcessorFunc) ReactorOptions {
	return func(br *BaseReactor) {
		br.processor = processor
	}
}

// WithTraceClient sets the tracing client using options
func WithTraceClient(traceClient trace.Tracer) ReactorOptions {
	return func(br *BaseReactor) {
		br.traceClient = traceClient
	}
}

// SetTraceClient sets the tracing client.
func (br *BaseReactor) SetTraceClient(traceClient trace.Tracer) {
	br.traceClient = traceClient
}

// WithQueueingFunc sets the queuing function to use when receiving a message.
func WithQueueingFunc(queuingFunc func(UnprocessedEnvelope)) ReactorOptions {
	return func(br *BaseReactor) {
		br.queueingFunc = queuingFunc
	}
}

// WithIncomingQueueSize sets the size of the incoming message queue for a
// reactor.
func WithIncomingQueueSize(size int) ReactorOptions {
	return func(br *BaseReactor) {
		br.incoming = make(chan UnmarshalResult, size)
	}
}

// QueueUnprocessedEnvelope is called by the switch when an unprocessed
// envelope is received. Unprocessed envelopes are immediately buffered in a
// queue to avoid blocking. The size of the queue can be changed by passing
// options to the base reactor.
func (br *BaseReactor) QueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	if len(br.incoming) == cap(br.incoming) {
		schema.WriteQueueLimit(br.traceClient, e.ChannelID, br.name, true)
	}
	select {
	// if the context is done, do nothing.
	case <-br.ctx.Done():
	// if not, add the item to the channel.
	case br.incoming <- br.unmarshalEnvelope(e):
	}
}

// TryQueueUnprocessedEnvelope an alternative to QueueUnprocessedEnvelope that attempts to queue an unprocessed envelope.
// If the queue is full, it drops the envelope.
func (br *BaseReactor) TryQueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	if len(br.incoming) == cap(br.incoming) {
		schema.WriteQueueLimit(br.traceClient, e.ChannelID, br.name, true)
	}
	select {
	case <-br.ctx.Done():
	case br.incoming <- br.unmarshalEnvelope(e):
	default:
	}
}

func (br *BaseReactor) unmarshalEnvelope(e UnprocessedEnvelope) UnmarshalResult {
	var (
		mt  = br.chIDs[e.ChannelID]
		res = UnmarshalResult{
			Src:           e.Src,
			BytesReceived: len(e.Message),
			ChannelID:     e.ChannelID,
		}
	)
	if mt == nil {
		res.Err = fmt.Errorf("no message type registered for channel %d", e.ChannelID)
		return res
	}

	res.Msg = proto.Clone(mt)
	res.Err = proto.Unmarshal(e.Message, res.Msg)
	return res
}

func (br *BaseReactor) OnStop() {
	br.cancel()
}

// ProcessorWithReactor unmarshalls the message and calls Receive on the reactor.
// This preserves the sender's original order for all messages and supports panic recovery with peer disconnection.
func ProcessorWithReactor(impl Reactor, baseReactor *BaseReactor) ProcessorFunc {
	return func(ctx context.Context, incoming <-chan UnmarshalResult) {
		for {
			select {
			case <-ctx.Done():
				return
			case res, ok := <-incoming:
				if !ok {
					// this means the channel was closed.
					return
				}
				// Process message with panic recovery for individual peer
				process := func(res UnmarshalResult) error {
					defer baseReactor.ProtectPanic(res.Src)
					if res.Err != nil {
						return res.Err
					}
					var (
						err              error
						msg              = res.Msg
						logBytesReceived = float64(res.BytesReceived)
					)
					if w, ok := msg.(Unwrapper); ok {
						msg, err = w.Unwrap()
						if err != nil {
							return err
						}
					}

					labels := []string{
						"peer_id", string(res.Src.ID()),
						"chID", fmt.Sprintf("%#x", res.ChannelID),
					}

					res.Src.Metrics().PeerReceiveBytesTotal.With(labels...).Add(logBytesReceived)
					res.Src.Metrics().MessageReceiveBytesTotal.With(append(labels, "message_type", res.Src.ValueToMetricLabel(msg))...).Add(logBytesReceived)
					schema.WriteReceivedBytes(res.Src.TraceClient(), string(res.Src.ID()), res.ChannelID, res.BytesReceived)
					impl.Receive(Envelope{
						ChannelID: res.ChannelID,
						Src:       res.Src,
						Message:   msg,
					})

					return nil
				}

				err := process(res)
				if err != nil {
					disconnectPeer(baseReactor, res.Src, fmt.Sprintf("error in reactor processing: %v", err), impl.String())
				}
			}
		}
	}
}

// ProtectPanic provides panic recovery for reactor operations involving a specific peer.
// If a panic occurs, it will disconnect the peer with an appropriate error message.
// Usage: defer baseReactor.ProtectPanic(peer)
func (br *BaseReactor) ProtectPanic(peer Peer) {
	if r := recover(); r != nil {
		disconnectPeer(br, peer, fmt.Sprintf("panic in reactor: %v", r), br.String())
	}
}

func disconnectPeer(baseReactor *BaseReactor, peer Peer, reason, reactor string) {
	// the switch is added for all reactors so should be here. the worst case if not is
	// that the peer doesn't get disconnected.
	if baseReactor != nil && baseReactor.Switch != nil {
		baseReactor.Switch.StopPeerForError(peer, reason, reactor)
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
