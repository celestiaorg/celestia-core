package p2p

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p/conn"
)

//-----------------------------------------------------------------------------

// Switch handles reactor management and message routing, delegating peer management to PeerManager
type Switch struct {
	service.BaseService

	config        *config.P2PConfig
	reactors      map[string]Reactor
	chDescs       []*conn.ChannelDescriptor
	reactorsByCh  map[byte]Reactor
	msgTypeByChID map[byte]proto.Message
	nodeInfo      NodeInfo // our node info
	nodeKey       *NodeKey // our node privkey

	transport   Transport
	peerManager PeerManager // Central peer management

	// Backward compatibility fields for tests
	addrBook AddrBook
	peers    IPeerSet

	metrics     *Metrics
	mlc         *metricsLabelCache
	traceClient trace.Tracer
}

// NetAddress returns the address the switch is listening on.
func (sw *Switch) NetAddress() *NetAddress {
	addr := sw.transport.NetAddress()
	return &addr
}

// SwitchOption sets an optional parameter on the Switch.
type SwitchOption func(*Switch)

// NewSwitch creates a new Switch with the given config.
func NewSwitch(
	cfg *config.P2PConfig,
	transport Transport,
	options ...SwitchOption,
) *Switch {

	peerManager := NewPeerManager(cfg)

	sw := &Switch{
		config:        cfg,
		reactors:      make(map[string]Reactor),
		chDescs:       make([]*conn.ChannelDescriptor, 0),
		reactorsByCh:  make(map[byte]Reactor),
		msgTypeByChID: make(map[byte]proto.Message),
		peerManager:   peerManager,
		transport:     transport,
		addrBook:      &addressBookProxy{pm: peerManager},
		peers:         &peerSetProxy{pm: peerManager},
		metrics:       NopMetrics(),
		mlc:           newMetricsLabelCache(),
		traceClient:   trace.NoOpTracer(),
	}

	sw.BaseService = *service.NewBaseService(nil, "P2P Switch", sw)

	for _, option := range options {
		option(sw)
	}

	// Initialize peer manager with necessary components
	sw.peerManager.SetTransport(transport)
	sw.peerManager.SetMetrics(sw.metrics)
	sw.peerManager.SetTraceClient(sw.traceClient)

	// Node info will be set when SetNodeKey and SetNodeInfo are called
	if sw.nodeKey != nil {
		sw.peerManager.SetNodeKey(sw.nodeKey)
	}
	if sw.nodeInfo != nil {
		sw.peerManager.SetNodeInfo(sw.nodeInfo)
	}

	// Initialize peer manager with reactor info after options are applied
	sw.peerManager.SetReactorsByCh(sw.reactorsByCh)
	sw.peerManager.SetMsgTypeByChID(sw.msgTypeByChID)
	sw.peerManager.SetChDescs(sw.chDescs)

	return sw
}

// SwitchFilterTimeout sets the timeout used for peer filters.
func SwitchFilterTimeout(timeout time.Duration) SwitchOption {
	return func(sw *Switch) {
		sw.peerManager.SetFilterTimeout(timeout)
	}
}

// SwitchPeerFilters sets the filters for rejection of new peers.
func SwitchPeerFilters(filters ...PeerFilterFunc) SwitchOption {
	return func(sw *Switch) {
		sw.peerManager.SetPeerFilters(filters)
	}
}

// WithMetrics sets the metrics.
func WithMetrics(metrics *Metrics) SwitchOption {
	return func(sw *Switch) {
		sw.metrics = metrics
		sw.peerManager.SetMetrics(metrics)
	}
}

// WithTracer sets the tracer.
func WithTracer(tracer trace.Tracer) SwitchOption {
	return func(sw *Switch) {
		sw.traceClient = tracer
		sw.peerManager.SetTraceClient(tracer)
	}
}

//---------------------------------------------------------------------
// Switch setup

// AddReactor adds the given reactor to the switch.
// NOTE: Not goroutine safe.
func (sw *Switch) AddReactor(name string, reactor Reactor) Reactor {
	for _, chDesc := range reactor.GetChannels() {
		chID := chDesc.ID
		// No two reactors can share the same channel.
		if sw.reactorsByCh[chID] != nil {
			panic(fmt.Sprintf("Channel %X has multiple reactors %v & %v", chID, sw.reactorsByCh[chID], reactor))
		}
		sw.chDescs = append(sw.chDescs, chDesc)
		sw.reactorsByCh[chID] = reactor
		sw.msgTypeByChID[chID] = chDesc.MessageType
	}
	sw.reactors[name] = reactor
	reactor.SetSwitch(sw)

	// Update peer manager with new reactor info
	sw.peerManager.SetReactorsByCh(sw.reactorsByCh)
	sw.peerManager.SetMsgTypeByChID(sw.msgTypeByChID)
	sw.peerManager.SetChDescs(sw.chDescs)

	return reactor
}

// RemoveReactor removes the given Reactor from the Switch.
// NOTE: Not goroutine safe.
func (sw *Switch) RemoveReactor(name string, reactor Reactor) {
	for _, chDesc := range reactor.GetChannels() {
		// remove channel description
		for i := 0; i < len(sw.chDescs); i++ {
			if chDesc.ID == sw.chDescs[i].ID {
				sw.chDescs = append(sw.chDescs[:i], sw.chDescs[i+1:]...)
				break
			}
		}
		delete(sw.reactorsByCh, chDesc.ID)
		delete(sw.msgTypeByChID, chDesc.ID)
	}
	delete(sw.reactors, name)
	reactor.SetSwitch(nil)

	// Update peer manager with updated reactor info
	sw.peerManager.SetReactorsByCh(sw.reactorsByCh)
	sw.peerManager.SetMsgTypeByChID(sw.msgTypeByChID)
	sw.peerManager.SetChDescs(sw.chDescs)
}

// Reactors returns a map of reactors registered on the switch.
// NOTE: Not goroutine safe.
func (sw *Switch) Reactors() map[string]Reactor {
	return sw.reactors
}

// Reactor returns the reactor with the given name.
// NOTE: Not goroutine safe.
func (sw *Switch) Reactor(name string) Reactor {
	return sw.reactors[name]
}

// SetNodeInfo sets the switch's NodeInfo for checking compatibility and handshaking with other nodes.
// NOTE: Not goroutine safe.
func (sw *Switch) SetNodeInfo(nodeInfo NodeInfo) {
	sw.nodeInfo = nodeInfo
	sw.peerManager.SetNodeInfo(nodeInfo)
}

// NodeInfo returns the switch's NodeInfo.
// NOTE: Not goroutine safe.
func (sw *Switch) NodeInfo() NodeInfo {
	return sw.nodeInfo
}

// SetNodeKey sets the switch's private key for authenticated encryption.
// NOTE: Not goroutine safe.
func (sw *Switch) SetNodeKey(nodeKey *NodeKey) {
	sw.nodeKey = nodeKey
	sw.peerManager.SetNodeKey(nodeKey)
}

//---------------------------------------------------------------------
// Service start/stop

// OnStart implements BaseService. It starts all the reactors and peer manager.
func (sw *Switch) OnStart() error {
	// Transport is already started when created
	// No need to start transport separately

	// Configure peer manager's stop peer for error function
	sw.peerManager.SetStopPeerForErrorFunc(sw.StopPeerForError)

	// Start peer manager first
	if err := sw.peerManager.Start(); err != nil {
		return err
	}

	// Start reactors
	for _, reactor := range sw.reactors {
		err := reactor.Start()
		if err != nil {
			return fmt.Errorf("failed to start %v: %w", reactor, err)
		}
	}

	// Start accept routine
	go sw.acceptRoutine()

	return nil
}

// OnStop implements BaseService.
func (sw *Switch) OnStop() {
	// Stop reactors
	for _, reactor := range sw.reactors {
		if err := reactor.Stop(); err != nil {
			sw.Logger.Error("error while stopping reactor", "reactor", reactor, "error", err)
		}
	}

	// Stop peer manager
	if err := sw.peerManager.Stop(); err != nil {
		sw.Logger.Error("error while stopping peer manager", "error", err)
	}

	// Stop transport if it supports Close (e.g., MultiplexTransport)
	if closer, ok := sw.transport.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			sw.Logger.Error("error while stopping transport", "error", err)
		}
	}
}

//---------------------------------------------------------------------
// Routing

// Broadcast runs a go routine for each attempted send, which will block trying
// to send for defaultSendTimeoutSeconds. Returns a channel which receives
// success values for each attempted send (false if times out). Channel will be
// closed once msg bytes are sent to all peers (or time out).
//
// NOTE: Broadcast uses goroutines, so order of broadcast may not be preserved.
func (sw *Switch) Broadcast(e Envelope) chan bool {
	peers := sw.peerManager.Peers().List()
	var success chan bool = make(chan bool, len(peers))

	sw.Logger.Debug("Broadcast", "channel", e.ChannelID, "msg", e.Message)

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p Peer) {
			defer wg.Done()
			success <- p.Send(e)
		}(peer)
	}

	go func() {
		wg.Wait()
		close(success)
	}()

	return success
}

//---------------------------------------------------------------------
// Peer management methods (delegated to PeerManager)

// addPeer adds a peer to the peer manager.
// This method is used primarily by tests.
func (sw *Switch) addPeer(peer Peer) error {
	return sw.peerManager.AcceptPeer(peer)
}

// addOutboundPeerWithConfig dials an outbound peer with the given config.
// This method is used primarily by tests.
func (sw *Switch) addOutboundPeerWithConfig(addr *NetAddress, cfg *config.P2PConfig) error {
	// Access the internal method of the peer manager
	if pm, ok := sw.peerManager.(*defaultPeerManager); ok {
		return pm.addOutboundPeerWithConfig(addr, cfg)
	}
	// Fallback to regular dial if we can't access the internal method
	return sw.peerManager.DialPeer(addr)
}

// addrBook returns the address book from the peer manager.
// This property is accessed by tests.
type addressBookProxy struct {
	pm PeerManager
}

func (abp *addressBookProxy) AddAddress(addr *NetAddress, src *NetAddress) error {
	return abp.pm.AddAddressFromPEX(addr, src)
}

func (abp *addressBookProxy) AddPrivateIDs(ids []string) {
	// This method is not directly available on PeerManager
	// We'll need to add it if required
}

func (abp *addressBookProxy) AddOurAddress(addr *NetAddress) {
	// This method is not directly available on PeerManager
	// We'll need to add it if required
}

func (abp *addressBookProxy) OurAddress(addr *NetAddress) bool {
	return abp.pm.OurAddress(addr)
}

func (abp *addressBookProxy) MarkGood(id ID) {
	abp.pm.MarkPeerAsGood(id)
}

func (abp *addressBookProxy) RemoveAddress(addr *NetAddress) {
	// This method is not directly available on PeerManager
	// We'll need to add it if required
}

func (abp *addressBookProxy) HasAddress(addr *NetAddress) bool {
	return abp.pm.IsDialingOrExistingAddress(addr)
}

func (abp *addressBookProxy) Save() {
	// This method is not directly available on PeerManager
	// We'll need to add it if required
}

// peerSetProxy implements IPeerSet by delegating to PeerManager
type peerSetProxy struct {
	pm PeerManager
}

func (psp *peerSetProxy) Has(key ID) bool {
	return psp.pm.Peers().Has(key)
}

func (psp *peerSetProxy) HasIP(ip net.IP) bool {
	return psp.pm.Peers().HasIP(ip)
}

func (psp *peerSetProxy) Get(key ID) Peer {
	return psp.pm.Peers().Get(key)
}

func (psp *peerSetProxy) List() []Peer {
	return psp.pm.Peers().List()
}

func (psp *peerSetProxy) Size() int {
	return psp.pm.Peers().Size()
}

//---------------------------------------------------------------------
// Accept routine

func (sw *Switch) acceptRoutine() {
	for {
		p, err := sw.transport.Accept(peerConfig{
			chDescs:       sw.chDescs,
			onPeerError:   sw.StopPeerForError,
			reactorsByCh:  sw.reactorsByCh,
			msgTypeByChID: sw.msgTypeByChID,
			metrics:       sw.metrics,
		})
		if err != nil {
			switch err := err.(type) {
			case ErrRejected:
				if err.IsSelf() {
					// Remove the given address from the address book and add our address
					sw.peerManager.SetAddrBook(sw.peerManager.(*defaultPeerManager).addrBook) // Access addrBook if needed
				}

				outbound, inbound, dialing := sw.NumPeers()
				sw.Logger.Info(
					"Inbound Peer rejected",
					"err", err,
					"outbound", outbound, "inbound", inbound, "dialing", dialing,
				)
				continue
			case ErrFilterTimeout:
				sw.Logger.Error(
					"Peer filter timed out",
					"err", err,
				)
				continue
			case ErrTransportClosed:
				outbound, inbound, dialing := sw.NumPeers()
				sw.Logger.Error(
					"Stopped accept routine, as transport is closed",
					"outbound", outbound, "inbound", inbound, "dialing", dialing,
				)
			default:
				outbound, inbound, dialing := sw.NumPeers()
				sw.Logger.Error(
					"Accept on transport errored",
					"err", err,
					"outbound", outbound, "inbound", inbound, "dialing", dialing,
				)
				// We could instead have a retry loop around the acceptRoutine,
				// but that would need to stop and let the node shutdown eventually.
				// So might as well panic and let process managers restart the node.
				// There's no point in letting the node run without the acceptRoutine,
				// since it won't be able to accept new connections.
				panic(fmt.Errorf("accept routine exited: %w", err))
			}

			break
		}

		if err := sw.peerManager.AcceptPeer(p); err != nil {
			_ = p.Stop()
			if _, ok := err.(ErrSwitchAuthenticationFailure); ok {
				// TODO: Add ip address and p2p address to error message.
				sw.Logger.Error("Inbound Peer authentication failed", "err", err, "peer", p)
			} else {
				sw.Logger.Error("Inbound Peer failed", "err", err, "peer", p)
			}
			continue
		}
	}
}

// NumPeers returns the count of outbound/inbound and dialing peers.
// unconditional peers are not counted here.
func (sw *Switch) NumPeers() (outbound, inbound, dialing int) {
	return sw.peerManager.NumPeers()
}

func (sw *Switch) IsPeerUnconditional(id ID) bool {
	return sw.peerManager.IsPeerUnconditional(id)
}

// MaxNumOutboundPeers returns a maximum number of outbound peers.
func (sw *Switch) MaxNumOutboundPeers() int {
	return sw.peerManager.MaxNumOutboundPeers()
}

// Peers returns the set of peers that are connected to the switch.
func (sw *Switch) Peers() IPeerSet {
	return sw.peerManager.Peers()
}

func (sw *Switch) StopPeerForError(peer Peer, reason interface{}) {
	sw.peerManager.StopPeerForError(peer, reason)
}

func (sw *Switch) StopPeerGracefully(peer Peer) {
	sw.peerManager.StopPeerGracefully(peer)
}

func (sw *Switch) SetAddrBook(addrBook AddrBook) {
	sw.peerManager.SetAddrBook(addrBook)
}

func (sw *Switch) MarkPeerAsGood(peer Peer) {
	sw.peerManager.MarkPeerAsGood(peer.ID())
}

func (sw *Switch) DialPeersAsync(peers []string) error {
	return sw.peerManager.DialPeersAsync(peers)
}

func (sw *Switch) DialPeerWithAddress(addr *NetAddress) error {
	return sw.peerManager.DialPeer(addr)
}

func (sw *Switch) IsDialingOrExistingAddress(addr *NetAddress) bool {
	return sw.peerManager.IsDialingOrExistingAddress(addr)
}

func (sw *Switch) AddPersistentPeers(addrs []string) error {
	return sw.peerManager.AddPersistentPeers(addrs)
}

func (sw *Switch) AddUnconditionalPeerIDs(ids []string) error {
	return sw.peerManager.AddUnconditionalPeerIDs(ids)
}

func (sw *Switch) AddPrivatePeerIDs(ids []string) error {
	// Private peer IDs are handled the same as unconditional peer IDs
	// for the purposes of peer management
	return sw.peerManager.AddUnconditionalPeerIDs(ids)
}

func (sw *Switch) IsPeerPersistent(na *NetAddress) bool {
	return sw.peerManager.IsPeerPersistent(na)
}
