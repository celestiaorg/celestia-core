package p2p

import (
	"context"
	"fmt"

	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p/conn"
	"github.com/cosmos/gogoproto/proto"
)

// ProcessorFunc is the message processor function type.
type ProcessorFunc func(context.Context, <-chan Envelope)

// MessagePoolHooks provides hooks for message pooling to reduce allocations.
// If configured, the base reactor will get messages from the pool before unmarshaling
// and return them to the pool after processing (via defer).
type MessagePoolHooks struct {
	// GetMessage retrieves a pre-allocated message from the pool for the given channel.
	// The data parameter allows peeking at the wire format to determine message type.
	// Should return nil if no pool is configured for this channel or message type.
	GetMessage func(channelID byte, data []byte) proto.Message

	// PutMessage returns a message to the pool after processing.
	// The message will be reset before being returned to the pool.
	PutMessage func(channelID byte, msg proto.Message)
}

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

	incoming     chan Envelope
	queueingFunc func(UnprocessedEnvelope)

	ctx    context.Context
	cancel context.CancelFunc
	// processor is called with the incoming channel and is responsible for
	// calling Receive on the reactor with already-unmarshalled messages.
	processor ProcessorFunc

	// messagePoolHooks provides optional message pooling to reduce allocations
	messagePoolHooks *MessagePoolHooks

	// chIDs maps channel IDs to their message types for unmarshalling
	chIDs map[byte]proto.Message
}

type ReactorOptions func(*BaseReactor)

func NewBaseReactor(name string, impl Reactor, opts ...ReactorOptions) *BaseReactor {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	// Build chIDs map from reactor channels
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
		incoming:    make(chan Envelope, 100),
		processor:   nil, // Will be set after base is created
		chIDs:       chIDs,
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

// WithIncomingQueueSize sets the size of the incoming message queue for a
// reactor.
func WithIncomingQueueSize(size int) ReactorOptions {
	return func(br *BaseReactor) {
		br.incoming = make(chan Envelope, size)
	}
}

// WithMessagePoolHooks configures message pooling hooks for the reactor.
// This enables message reuse during unmarshaling, significantly reducing allocations.
func WithMessagePoolHooks(hooks *MessagePoolHooks) ReactorOptions {
	return func(br *BaseReactor) {
		br.messagePoolHooks = hooks
	}
}

// unmarshalEnvelope synchronously unmarshals an UnprocessedEnvelope into an Envelope.
// This is called in the connection receive goroutine before queueing.
func (br *BaseReactor) unmarshalEnvelope(ue UnprocessedEnvelope) (Envelope, error) {
	// Save message length for metrics before we lose the bytes
	msgLen := len(ue.Message)

	mt := br.chIDs[ue.ChannelID]
	if mt == nil {
		return Envelope{}, fmt.Errorf("no message type registered for channel %d", ue.ChannelID)
	}

	// Get message from pool if hooks are configured
	// Pass raw bytes to allow peeking at wire format for type detection
	var msg proto.Message
	//if br.messagePoolHooks != nil && br.messagePoolHooks.GetMessage != nil {
	//	msg = br.messagePoolHooks.GetMessage(ue.ChannelID, ue.Message)
	//}

	// Fallback to normal allocation if no pool
	//if msg == nil {
	msg = proto.Clone(mt)
	//}

	// Unmarshal into the message (wrapper or regular message)
	err := proto.Unmarshal(ue.Message, msg)
	if err != nil {
		return Envelope{}, err
	}

	// Unwrap to get the actual message for processing
	if w, ok := msg.(Unwrapper); ok {
		msg, err = w.Unwrap()
		if err != nil {
			return Envelope{}, err
		}
	}

	// Update metrics and tracing
	labels := []string{
		"peer_id", string(ue.Src.ID()),
		"chID", fmt.Sprintf("%#x", ue.ChannelID),
	}
	ue.Src.Metrics().PeerReceiveBytesTotal.With(labels...).Add(float64(msgLen))
	ue.Src.Metrics().MessageReceiveBytesTotal.With(append(labels, "message_type", ue.Src.ValueToMetricLabel(msg))...).Add(float64(msgLen))
	schema.WriteReceivedBytes(ue.Src.TraceClient(), string(ue.Src.ID()), ue.ChannelID, msgLen)

	envelope := Envelope{
		ChannelID: ue.ChannelID,
		Src:       ue.Src,
		Message:   msg,
	}

	return envelope, nil
}

// QueueUnprocessedEnvelope is called by the switch when an unprocessed
// envelope is received. It synchronously unmarshals the message bytes
// and queues the resulting proto.Message to avoid blocking the connection
// receive loop with queuing operations.
func (br *BaseReactor) QueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	// Unmarshal synchronously in the connection receive goroutine
	envelope, err := br.unmarshalEnvelope(e)
	if err != nil {
		// Log error and disconnect peer
		if br.Logger != nil {
			br.Logger.Error("failed to unmarshal envelope", "err", err, "channel", e.ChannelID)
		}
		disconnectPeer(br, e.Src, fmt.Sprintf("unmarshal error: %v", err), br.String())
		return
	}

	// Queue the already-unmarshalled envelope
	select {
	// if the context is done, do nothing.
	case <-br.ctx.Done():
	// if not, add the item to the channel.
	case br.incoming <- envelope:
	}
}

// TryQueueUnprocessedEnvelope an alternative to QueueUnprocessedEnvelope that attempts to queue an unprocessed envelope.
// If the queue is full, it drops the envelope.
func (br *BaseReactor) TryQueueUnprocessedEnvelope(e UnprocessedEnvelope) {
	// Unmarshal synchronously in the connection receive goroutine
	envelope, err := br.unmarshalEnvelope(e)
	if err != nil {
		// Log error and disconnect peer
		if br.Logger != nil {
			br.Logger.Error("failed to unmarshal envelope", "err", err, "channel", e.ChannelID)
		}
		disconnectPeer(br, e.Src, fmt.Sprintf("unmarshal error: %v", err), br.String())
		return
	}

	// Try to queue the already-unmarshalled envelope
	select {
	case <-br.ctx.Done():
	case br.incoming <- envelope:
	default:
		// Queue is full, drop the envelope
	}
}

func (br *BaseReactor) OnStop() {
	br.cancel()
}

// ProcessorWithReactor processes already-unmarshalled messages and calls Receive on the reactor.
// Messages are unmarshalled synchronously in QueueUnprocessedEnvelope before being queued.
// This preserves the sender's original order for all messages and supports panic recovery with peer disconnection.
func ProcessorWithReactor(impl Reactor, baseReactor *BaseReactor) func(context.Context, <-chan Envelope) {
	return func(ctx context.Context, incoming <-chan Envelope) {
		for {
			select {
			case <-ctx.Done():
				return
			case envelope, ok := <-incoming:
				if !ok {
					// this means the channel was closed.
					return
				}

				// Process message with panic recovery for individual peer
				process := func(envelope Envelope) error {
					defer baseReactor.ProtectPanic(envelope.Src)

					// If pooled message, return it to pool after processing
					//if baseReactor.messagePoolHooks != nil && baseReactor.messagePoolHooks.PutMessage != nil {
					//	defer baseReactor.messagePoolHooks.PutMessage(envelope.ChannelID, envelope.Message)
					//}

					// Note: We don't have the original byte length here anymore
					// Metrics and tracing are handled in unmarshalEnvelope
					// Just call Receive with the already-unmarshalled message
					impl.Receive(envelope)

					return nil
				}

				err := process(envelope)
				if err != nil {
					disconnectPeer(baseReactor, envelope.Src, fmt.Sprintf("error in reactor processing: %v", err), impl.String())
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
