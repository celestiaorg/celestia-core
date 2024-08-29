package p2p

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/quic-go/quic-go"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	"io"
	"net"
	"time"

	"github.com/gogo/protobuf/proto"

	"github.com/tendermint/tendermint/libs/cmap"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/service"
	"github.com/tendermint/tendermint/pkg/trace"
)

//go:generate ../scripts/mockery_generate.sh Peer
const metricsTickerDuration = 10 * time.Second

// Peer is an interface representing a peer connected on a reactor.
type Peer interface {
	service.Service
	FlushStop()

	ID() ID               // peer's cryptographic ID
	RemoteIP() net.IP     // remote IP of the connection
	RemoteAddr() net.Addr // remote address of the connection

	IsOutbound() bool   // did we dial the peer
	IsPersistent() bool // do we redial this peer when we disconnect

	CloseConn() error // close original connection

	NodeInfo() NodeInfo // peer's info
	Status() ConnectionStatus
	SocketAddr() *NetAddress // actual address of the socket

	// Deprecated: entities looking to act as peers should implement SendEnvelope instead.
	// Send will be removed in v0.37.
	Send(byte, []byte) bool

	// Deprecated: entities looking to act as peers should implement TrySendEnvelope instead.
	// TrySend will be removed in v0.37.
	TrySend(byte, []byte) bool

	Set(string, interface{})
	Get(string) interface{}

	SetRemovalFailed()
	GetRemovalFailed() bool
}

type EnvelopeSender interface {
	SendEnvelope(Envelope) bool
	TrySendEnvelope(Envelope) bool
}

// EnvelopeSendShim implements a shim to allow the legacy peer type that
// does not implement SendEnvelope to be used in places where envelopes are
// being sent. If the peer implements the *Envelope methods, then they are used,
// otherwise, the message is marshaled and dispatched to the legacy *Send.
//
// Deprecated: Will be removed in v0.37.
func SendEnvelopeShim(p Peer, e Envelope, lg log.Logger) bool {
	if es, ok := p.(EnvelopeSender); ok {
		return es.SendEnvelope(e)
	}
	msg := e.Message
	if w, ok := msg.(Wrapper); ok {
		msg = w.Wrap()
	}
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		lg.Error("marshaling message to send", "error", err)
		return false
	}
	return p.Send(e.ChannelID, msgBytes)
}

// EnvelopeTrySendShim implements a shim to allow the legacy peer type that
// does not implement TrySendEnvelope to be used in places where envelopes are
// being sent. If the peer implements the *Envelope methods, then they are used,
// otherwise, the message is marshaled and dispatched to the legacy *Send.
//
// Deprecated: Will be removed in v0.37.
func TrySendEnvelopeShim(p Peer, e Envelope, lg log.Logger) bool {
	if es, ok := p.(EnvelopeSender); ok {
		return es.TrySendEnvelope(e)
	}
	msg := e.Message
	if w, ok := msg.(Wrapper); ok {
		msg = w.Wrap()
	}
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		lg.Error("marshaling message to send", "error", err)
		return false
	}
	return p.TrySend(e.ChannelID, msgBytes)
}

//----------------------------------------------------------

// peerConn contains the raw connection and its config.
type peerConn struct {
	outbound   bool
	persistent bool
	conn       quic.Connection // source connection

	socketAddr *NetAddress
	created    time.Time // time of creation
	// cached RemoteIP()
	ip net.IP
}

func newPeerConn(
	outbound, persistent bool,
	conn quic.Connection,
	socketAddr *NetAddress,
) peerConn {

	return peerConn{
		outbound:   outbound,
		persistent: persistent,
		conn:       conn,
		socketAddr: socketAddr,
		created:    time.Now(),
	}
}

// ID only exists for SecretConnection.
// TODO(rach-id): fix ID here
func (pc peerConn) ID() ID {
	return ID(pc.conn.RemoteAddr().String())
}

// Return the IP from the connection RemoteAddr
func (pc peerConn) RemoteIP() net.IP {
	if pc.ip != nil {
		return pc.ip
	}

	host, _, err := net.SplitHostPort(pc.conn.RemoteAddr().String())
	if err != nil {
		panic(err)
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		panic(err)
	}

	pc.ip = ips[0]

	return pc.ip
}

// peer implements Peer.
//
// Before using a peer, you will need to perform a handshake on connection.
type peer struct {
	service.BaseService

	// raw peerConn and the multiplex connection
	peerConn

	onReceive func(chID byte, msgBytes []byte)

	// peer's node info and the channel it knows about
	// channels = nodeInfo.Channels
	// cached to avoid copying nodeInfo in hasChannel
	nodeInfo NodeInfo
	channels []byte

	streams map[byte]quic.Stream

	// User data
	Data *cmap.CMap

	metrics       *Metrics
	traceClient   trace.Tracer
	metricsTicker *time.Ticker
	mlc           *metricsLabelCache

	// When removal of a peer fails, we set this flag
	removalAttemptFailed bool
}

type PeerOption func(*peer)

func WithPeerTracer(t trace.Tracer) PeerOption {
	return func(p *peer) {
		p.traceClient = t
	}
}

func newPeer(
	pc peerConn,
	nodeInfo NodeInfo,
	reactorsByCh map[byte]Reactor,
	msgTypeByChID map[byte]proto.Message,
	mlc *metricsLabelCache,
	onPeerError func(Peer, interface{}),
	options ...PeerOption,
) *peer {
	p := &peer{
		peerConn:      pc,
		nodeInfo:      nodeInfo,
		channels:      nodeInfo.(DefaultNodeInfo).Channels,
		Data:          cmap.NewCMap(),
		metricsTicker: time.NewTicker(metricsTickerDuration),
		metrics:       NopMetrics(),
		mlc:           mlc,
		traceClient:   trace.NoOpTracer(),
		streams:       make(map[byte]quic.Stream),
	}

	p.onReceive = func(chID byte, msgBytes []byte) {
		reactor := reactorsByCh[chID]
		if reactor == nil {
			// Note that its ok to panic here as it's caught in the conn._recover,
			// which does onPeerError.
			panic(fmt.Sprintf("Unknown channel %X", chID))
		}
		mt := msgTypeByChID[chID]
		msg := proto.Clone(mt)
		err := proto.Unmarshal(msgBytes, msg)
		if err != nil {
			p.Logger.Error("before panic", "msg", msg, "channel", chID, "type", mt, "bytes", hex.EncodeToString(msgBytes))
			return
		}

		if w, ok := msg.(Unwrapper); ok {
			msg, err = w.Unwrap()
			if err != nil {
				panic(fmt.Errorf("unwrapping message: %s", err))
			}
		}

		labels := []string{
			"peer_id", string(p.ID()),
			"chID", fmt.Sprintf("%#x", chID),
		}

		p.metrics.PeerReceiveBytesTotal.With(labels...).Add(float64(len(msgBytes)))
		p.metrics.MessageReceiveBytesTotal.With(append(labels, "message_type", p.mlc.ValueToMetricLabel(msg))...).Add(float64(len(msgBytes)))
		schema.WriteReceivedBytes(p.traceClient, string(p.ID()), chID, len(msgBytes))
		if nr, ok := reactor.(EnvelopeReceiver); ok {
			nr.ReceiveEnvelope(Envelope{
				ChannelID: chID,
				Src:       p,
				Message:   msg,
			})
		} else {
			reactor.Receive(chID, p, msgBytes)
		}
	}

	go func() {
		err := p.StartReceiving()
		if err != nil {
			p.Logger.Error("error when receiving data from peer", "err", err.Error())
			onPeerError(p, err)
		}
	}()

	p.BaseService = *service.NewBaseService(nil, "Peer", p)
	for _, option := range options {
		option(p)
	}

	return p
}

// String representation.
func (p *peer) String() string {
	if p.outbound {
		return fmt.Sprintf("Peer{%v %v out}", p.conn.RemoteAddr().String(), p.ID())
	}

	return fmt.Sprintf("Peer{%v %v in}", p.conn.RemoteAddr().String(), p.ID())
}

//---------------------------------------------------
// Implements service.Service

// SetLogger implements BaseService.
func (p *peer) SetLogger(l log.Logger) {
	p.Logger = l
}

// OnStart implements BaseService.
func (p *peer) OnStart() error {
	if err := p.BaseService.OnStart(); err != nil {
		return err
	}

	go p.metricsReporter()
	return nil
}

// FlushStop mimics OnStop but additionally ensures that all successful
// SendEnvelope() calls will get flushed before closing the connection.
// NOTE: it is not safe to call this method more than once.
func (p *peer) FlushStop() {
	p.metricsTicker.Stop()
	p.BaseService.OnStop()
	for _, stream := range p.streams {
		// TODO(rach-id): set valid error codes
		stream.CancelRead(quic.StreamErrorCode(0))  // stop everything and close the conn
		stream.CancelWrite(quic.StreamErrorCode(0)) // stop everything and close the conn
	}
}

// OnStop implements BaseService.
func (p *peer) OnStop() {
	p.metricsTicker.Stop()
	p.BaseService.OnStop()
	// TODO(rach-id): set valid error code
	if err := p.conn.CloseWithError(quic.ApplicationErrorCode(0), "stopping peer connection"); err != nil { // stop everything and close the conn
		p.Logger.Debug("Error while stopping peer", "err", err)
	}
}

//---------------------------------------------------
// Implements Peer

// ID returns the peer's ID - the hex encoded hash of its pubkey.
func (p *peer) ID() ID {
	return p.nodeInfo.ID()
}

// IsOutbound returns true if the connection is outbound, false otherwise.
func (p *peer) IsOutbound() bool {
	return p.peerConn.outbound
}

// IsPersistent returns true if the peer is persitent, false otherwise.
func (p *peer) IsPersistent() bool {
	return p.peerConn.persistent
}

// NodeInfo returns a copy of the peer's NodeInfo.
func (p *peer) NodeInfo() NodeInfo {
	return p.nodeInfo
}

// SocketAddr returns the address of the socket.
// For outbound peers, it's the address dialed (after DNS resolution).
// For inbound peers, it's the address returned by the underlying connection
// (not what's reported in the peer's NodeInfo).
func (p *peer) SocketAddr() *NetAddress {
	return p.socketAddr
}

// Status returns the peer's ConnectionStatus.
func (p *peer) Status() ConnectionStatus {
	return ConnectionStatus{
		Duration: time.Since(p.created),
		// TODO(rach-id): register ecdsa.PublicKey protobuf definition
		ConnectionState: p.conn.ConnectionState(),
	}
}

// SendEnvelope sends the message in the envelope on the channel specified by the
// envelope. Returns false if the connection times out trying to place the message
// onto its internal queue.
// Using SendEnvelope allows for tracking the message bytes sent and received by message type
// as a metric which Send cannot support.
func (p *peer) SendEnvelope(e Envelope) bool {
	if !p.IsRunning() {
		return false
	} else if !p.hasChannel(e.ChannelID) {
		return false
	}
	msg := e.Message
	metricLabelValue := p.mlc.ValueToMetricLabel(msg)
	if w, ok := msg.(Wrapper); ok {
		msg = w.Wrap()
	}
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		p.Logger.Error("marshaling message to send", "error", err)
		return false
	}
	res := p.Send(e.ChannelID, msgBytes)
	if res {
		labels := []string{
			"message_type", metricLabelValue,
			"chID", fmt.Sprintf("%#x", e.ChannelID),
			"peer_id", string(p.ID()),
		}
		p.metrics.MessageSendBytesTotal.With(labels...).Add(float64(len(msgBytes)))
	}
	return res
}

// Send msg bytes to the channel identified by chID byte. Returns false if the
// send queue is full after timeout, specified by MConnection.
// SendEnvelope replaces Send which will be deprecated in a future release.
func (p *peer) Send(chID byte, msgBytes []byte) bool {
	if !p.IsRunning() {
		return false
	} else if !p.hasChannel(chID) {
		return false
	}
	stream, has := p.streams[chID]
	if !has {
		newStream, err := p.conn.OpenStreamSync(context.Background())
		if err != nil {
			p.Logger.Error("error opening quic stream", "err", err.Error())
			return false
		}
		p.streams[chID] = newStream
		stream = newStream
		err = binary.Write(stream, binary.BigEndian, chID)
		if err != nil {
			p.Logger.Error("error sending channel ID", "err", err.Error())
			return false
		}
	}

	if err := binary.Write(stream, binary.BigEndian, uint32(len(msgBytes))); err != nil {
		p.Logger.Error("Send len failed", "err", err, "stream_id", stream.StreamID(), "msgBytes", log.NewLazySprintf("%X", msgBytes))
		return false
	}
	err := binary.Write(stream, binary.BigEndian, msgBytes)
	if err != nil {
		p.Logger.Info("Send failed", "channel", "stream_id", stream.StreamID(), "msgBytes", log.NewLazySprintf("%X", msgBytes))
		return false
	}
	labels := []string{
		"peer_id", string(p.ID()),
		"chID", fmt.Sprintf("%#x", chID),
	}
	p.metrics.PeerSendBytesTotal.With(labels...).Add(float64(len(msgBytes)))

	return true
}

// TrySend msg bytes to the channel identified by chID byte. Immediately returns
// false if the send queue is full.
// TrySendEnvelope replaces TrySend which will be deprecated in a future release.
func (p *peer) TrySend(chID byte, msgBytes []byte) bool {
	if !p.IsRunning() {
		return false
	} else if !p.hasChannel(chID) {
		return false
	}
	res := p.Send(chID, msgBytes)
	if res {
		labels := []string{
			"peer_id", string(p.ID()),
			"chID", fmt.Sprintf("%#x", chID),
		}
		p.metrics.PeerSendBytesTotal.With(labels...).Add(float64(len(msgBytes)))
	}
	return res
}

// Get the data for a given key.
func (p *peer) Get(key string) interface{} {
	return p.Data.Get(key)
}

// Set sets the data for the given key.
func (p *peer) Set(key string, data interface{}) {
	p.Data.Set(key, data)
}

// hasChannel returns true if the peer reported
// knowing about the given chID.
func (p *peer) hasChannel(chID byte) bool {
	for _, ch := range p.channels {
		if ch == chID {
			return true
		}
	}
	// NOTE: probably will want to remove this
	// but could be helpful while the feature is new
	p.Logger.Debug(
		"Unknown channel for peer",
		"channel",
		chID,
		"channels",
		p.channels,
	)
	return false
}

// CloseConn closes original connection. Used for cleaning up in cases where the peer had not been started at all.
func (p *peer) CloseConn() error {
	// TODO(rach-id): valid error code
	return p.conn.CloseWithError(quic.ApplicationErrorCode(0), "closed peer connection")
}

func (p *peer) SetRemovalFailed() {
	p.removalAttemptFailed = true
}

func (p *peer) GetRemovalFailed() bool {
	return p.removalAttemptFailed
}

//---------------------------------------------------
// methods only used for testing
// TODO: can we remove these?

// CloseConn closes the underlying connection
func (pc *peerConn) CloseConn() {
	// TODO(rach-id): valid error code
	pc.conn.CloseWithError(quic.ApplicationErrorCode(0), "closed peer connection")
}

// RemoteAddr returns peer's remote network address.
func (p *peer) RemoteAddr() net.Addr {
	return p.conn.RemoteAddr()
}

// CanSend returns true if the send queue is not full, false otherwise.
func (p *peer) CanSend(chID byte) bool {
	if !p.IsRunning() {
		return false
	}
	return true
}

//---------------------------------------------------

func PeerMetrics(metrics *Metrics) PeerOption {
	return func(p *peer) {
		p.metrics = metrics
	}
}

func (p *peer) metricsReporter() {
}

func (p *peer) StartReceiving() error {
	for {
		stream, err := p.conn.AcceptStream(context.Background())
		if err != nil {
			p.Logger.Debug("failed to accept stream", "err", err.Error())
			return err
		}
		var chID byte
		err = binary.Read(stream, binary.BigEndian, &chID)
		if err != nil {
			p.Logger.Debug("failed to read channel ID", "err", err.Error())
			return err
		}
		// start accepting data
		go func() {
			for {
				var dataLen uint32
				err = binary.Read(stream, binary.BigEndian, &dataLen)
				if err != nil {
					p.Logger.Debug("failed to read size from stream", "err", err.Error())
					return
				}
				data := make([]byte, dataLen)
				_, err = io.ReadFull(stream, data)
				if err != nil {
					p.Logger.Debug("failed to read data from stream", "err", err.Error())
					return
				}
				p.onReceive(chID, data)
			}
		}()
	}
}
