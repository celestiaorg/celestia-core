package p2p

import (
	"fmt"
	"net"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/libs/cmap"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"

	cmtconn "github.com/cometbft/cometbft/p2p/conn"
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
	Status() cmtconn.ConnectionStatus
	SocketAddr() *NetAddress // actual address of the socket

	Send(Envelope) bool
	TrySend(Envelope) bool

	Set(string, interface{})
	Get(string) interface{}

	SetRemovalFailed()
	GetRemovalFailed() bool

	HasIPChanged() bool // has the peer's IP changed
}

type IntrospectivePeer interface {
	Peer
	Metrics() *Metrics
	ValueToMetricLabel(i any) string
	TraceClient() trace.Tracer
}

//----------------------------------------------------------

// peerConn contains the raw connection and its config.
type peerConn struct {
	outbound   bool
	persistent bool
	conn       net.Conn // source connection

	socketAddr *NetAddress
}

func newPeerConn(
	outbound, persistent bool,
	conn net.Conn,
	socketAddr *NetAddress,
) peerConn {

	return peerConn{
		outbound:   outbound,
		persistent: persistent,
		conn:       conn,
		socketAddr: socketAddr,
	}
}

// ID only exists for SecretConnection.
// NOTE: Will panic if conn is not *SecretConnection.
func (pc peerConn) ID() ID {
	return PubKeyToID(pc.conn.(*cmtconn.SecretConnection).RemotePubKey())
}

// Return the IP from the connection RemoteAddr
func (pc peerConn) RemoteIP() net.IP {
	host, _, err := net.SplitHostPort(pc.conn.RemoteAddr().String())
	if err != nil {
		panic(err)
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		panic(err)
	}

	return ips[0]
}

// peer implements Peer.
//
// Before using a peer, you will need to perform a handshake on connection.
type peer struct {
	service.BaseService

	// raw peerConn and the multiplex connection
	peerConn
	mconn *cmtconn.MConnection

	// peer's node info and the channel it knows about
	// channels = nodeInfo.Channels
	// cached to avoid copying nodeInfo in hasChannel
	nodeInfo NodeInfo
	channels []byte

	// User data
	Data *cmap.CMap

	metrics     *Metrics
	mlc         *metricsLabelCache
	traceClient trace.Tracer

	// Atomic fields for thread-safe concurrent access
	removalAttemptFailed atomic.Bool
	cachedIP             atomic.Pointer[net.IP]

	// Lifecycle coordination: stopped protects metricsTicker from concurrent access
	// Once stopped transitions from false->true, no further lifecycle operations occur
	stopped       atomic.Bool
	metricsTicker *time.Ticker
}

func (p *peer) TraceClient() trace.Tracer {
	return p.traceClient
}

type PeerOption func(*peer)

func WithPeerTracer(t trace.Tracer) PeerOption {
	return func(p *peer) {
		p.traceClient = t
	}
}

func newPeer(
	pc peerConn,
	mConfig cmtconn.MConnConfig,
	nodeInfo NodeInfo,
	reactorsByCh map[byte]Reactor,
	_ map[byte]proto.Message,
	chDescs []*cmtconn.ChannelDescriptor,
	onPeerError func(Peer, interface{}, string),
	mlc *metricsLabelCache,
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
	}

	p.mconn = createMConnection(
		pc.conn,
		p,
		reactorsByCh,
		chDescs,
		onPeerError,
		mConfig,
	)
	p.BaseService = *service.NewBaseService(nil, "Peer", p)
	for _, option := range options {
		option(p)
	}

	return p
}

// String representation.
func (p *peer) String() string {
	if p.outbound {
		return fmt.Sprintf("Peer{%v %v out}", p.mconn, p.ID())
	}

	return fmt.Sprintf("Peer{%v %v in}", p.mconn, p.ID())
}

//---------------------------------------------------
// Implements service.Service

func (p *peer) Metrics() *Metrics {
	return p.metrics
}

func (p *peer) ValueToMetricLabel(i any) string {
	return p.mlc.ValueToMetricLabel(i)
}

// SetLogger implements BaseService.
func (p *peer) SetLogger(l log.Logger) {
	p.Logger = l
	p.mconn.SetLogger(l)
}

// OnStart implements BaseService.
func (p *peer) OnStart() error {
	if err := p.BaseService.OnStart(); err != nil {
		return err
	}

	if err := p.mconn.Start(); err != nil {
		return err
	}

	go p.metricsReporter()
	return nil
}

// FlushStop mimics OnStop but additionally ensures that all successful
// .Send() calls will get flushed before closing the connection.
// Thread-safe and idempotent - can be called multiple times safely.
func (p *peer) FlushStop() {
	if !p.stopped.CompareAndSwap(false, true) {
		return // Already stopped
	}

	p.metricsTicker.Stop()
	p.BaseService.OnStop()
	p.mconn.FlushStop() // stop everything and close the conn
}

func (p *peer) Metrics() *Metrics {
	return p.metrics
}

func (p *peer) ValueToMetricLabel(i any) string {
	return p.mlc.ValueToMetricLabel(i)
}

func (p *peer) TraceClient() trace.Tracer {
	return p.traceClient
}

// OnStop implements BaseService.
// Thread-safe and idempotent - can be called multiple times safely.
func (p *peer) OnStop() {
	if !p.stopped.CompareAndSwap(false, true) {
		return // Already stopped
	}

	p.metricsTicker.Stop()
	p.BaseService.OnStop()
	if err := p.mconn.Stop(); err != nil { // stop everything and close the conn
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
	return p.peerConn.outbound //nolint:staticcheck
}

// IsPersistent returns true if the peer is persitent, false otherwise.
func (p *peer) IsPersistent() bool {
	return p.peerConn.persistent //nolint:staticcheck
}

// NodeInfo returns a copy of the peer's NodeInfo.
func (p *peer) NodeInfo() NodeInfo {
	return p.nodeInfo
}

// RemoteIP returns the IP from the connection RemoteAddr with atomic caching
func (p *peer) RemoteIP() net.IP {
	// Fast path: return cached IP if available
	if cached := p.cachedIP.Load(); cached != nil {
		return *cached
	}

	// Slow path: perform DNS lookup and cache result
	result := p.peerConn.RemoteIP()
	p.cachedIP.Store(&result)
	return result
}

// HasIPChanged returns true if the peer's IP has changed.
// This method clears the cached IP and compares with a fresh lookup.
func (p *peer) HasIPChanged() bool {
	// Get the currently cached IP
	oldIP := p.cachedIP.Load()
	if oldIP == nil {
		return false // No cached IP, so no change detected
	}

	// Clear the cached IP to force a fresh lookup
	p.cachedIP.Store(nil)

	// Get the current IP (will perform fresh DNS lookup)
	newIP := p.RemoteIP()

	// Compare the IPs
	return !(*oldIP).Equal(newIP)
}

// SocketAddr returns the address of the socket.
// For outbound peers, it's the address dialed (after DNS resolution).
// For inbound peers, it's the address returned by the underlying connection
// (not what's reported in the peer's NodeInfo).
func (p *peer) SocketAddr() *NetAddress {
	return p.peerConn.socketAddr //nolint:staticcheck
}

// Status returns the peer's ConnectionStatus.
func (p *peer) Status() cmtconn.ConnectionStatus {
	return p.mconn.Status()
}

// Send msg bytes to the channel identified by chID byte. Returns false if the
// send queue is full after timeout, specified by MConnection.
func (p *peer) Send(e Envelope) bool {
	return p.send(e.ChannelID, e.Message, p.mconn.Send)
}

// TrySend msg bytes to the channel identified by chID byte. Immediately returns
// false if the send queue is full.
func (p *peer) TrySend(e Envelope) bool {
	return p.send(e.ChannelID, e.Message, p.mconn.TrySend)
}

func (p *peer) send(chID byte, msg proto.Message, sendFunc func(byte, []byte) bool) bool {
	if !p.IsRunning() {
		return false
	} else if !p.hasChannel(chID) {
		return false
	}
	metricLabelValue := p.mlc.ValueToMetricLabel(msg)
	if w, ok := msg.(Wrapper); ok {
		msg = w.Wrap()
	}
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		p.Logger.Error("marshaling message to send", "error", err)
		return false
	}
	res := sendFunc(chID, msgBytes)
	if res {
		labels := []string{
			"peer_id", string(p.ID()),
			"chID", fmt.Sprintf("%#x", chID),
		}
		p.metrics.PeerSendBytesTotal.With(labels...).Add(float64(len(msgBytes)))
		labels = append(labels, "message_type", metricLabelValue)
		p.metrics.MessageSendBytesTotal.With(labels...).Add(float64(len(msgBytes)))
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
	return p.peerConn.conn.Close() //nolint:staticcheck
}

func (p *peer) SetRemovalFailed() {
	p.removalAttemptFailed.Store(true)
}

func (p *peer) GetRemovalFailed() bool {
	return p.removalAttemptFailed.Load()
}

//---------------------------------------------------
// methods only used for testing
// TODO: can we remove these?

// CloseConn closes the underlying connection
func (pc *peerConn) CloseConn() {
	pc.conn.Close()
}

// RemoteAddr returns peer's remote network address.
func (p *peer) RemoteAddr() net.Addr {
	return p.peerConn.conn.RemoteAddr() //nolint:staticcheck
}

// CanSend returns true if the send queue is not full, false otherwise.
func (p *peer) CanSend(chID byte) bool {
	if !p.IsRunning() {
		return false
	}
	return p.mconn.CanSend(chID)
}

//---------------------------------------------------

func PeerMetrics(metrics *Metrics) PeerOption {
	return func(p *peer) {
		p.metrics = metrics
	}
}

func (p *peer) metricsReporter() {
	for {
		select {
		case <-p.metricsTicker.C:
			queues := make(map[byte]int, len(p.mconn.Status().Channels))
			status := p.mconn.Status()
			var sendQueueSize float64
			for _, chStatus := range status.Channels {
				sendQueueSize += float64(chStatus.SendQueueSize)
				queues[chStatus.ID] = chStatus.SendQueueSize
			}

			p.metrics.PeerPendingSendBytes.With("peer_id", string(p.ID())).Set(sendQueueSize)
			schema.WritePendingBytes(p.traceClient, string(p.ID()), queues)
		case <-p.Quit():
			return
		}
	}
}

//------------------------------------------------------------------
// helper funcs

func createMConnection(
	conn net.Conn,
	p *peer,
	reactorsByCh map[byte]Reactor,
	chDescs []*cmtconn.ChannelDescriptor,
	onPeerError func(Peer, interface{}, string),
	config cmtconn.MConnConfig,
) *cmtconn.MConnection {

	onReceive := func(chID byte, msgBytes []byte) {
		reactor := reactorsByCh[chID]
		if reactor == nil {
			// Note that its ok to panic here as it's caught in the conn._recover,
			// which does onPeerError.
			panic(fmt.Sprintf("Unknown channel %X", chID))
		}

		reactor.QueueUnprocessedEnvelope(UnprocessedEnvelope{
			ChannelID: chID,
			Src:       p,
			Message:   msgBytes,
		})
	}

	onError := func(r interface{}) {
		onPeerError(p, r, "p2p")
	}

	return cmtconn.NewMConnectionWithConfig(
		conn,
		chDescs,
		onReceive,
		onError,
		config,
	)
}
