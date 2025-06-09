package p2p

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/cmap"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p/conn"
	"github.com/cosmos/gogoproto/proto"
)

const (
	// wait a random amount of time from this interval
	// before dialing peers or reconnecting to help prevent DoS
	dialRandomizerIntervalMilliseconds = 3000

	// repeatedly try to reconnect for a few minutes
	// ie. 5 * 20 = 100s
	reconnectAttempts = 20
	reconnectInterval = 5 * time.Second

	// then move into exponential backoff mode for ~1day
	// ie. 3**10 = 16hrs
	reconnectBackOffAttempts    = 10
	reconnectBackOffBaseSeconds = 3

	// peer manager filter timeout
	peerManagerFilterTimeout = 5 * time.Second
)

// An AddrBook represents an address book from the pex package, which is used
// to store peer addresses.
type AddrBook interface {
	AddAddress(addr *NetAddress, src *NetAddress) error
	AddPrivateIDs([]string)
	AddOurAddress(*NetAddress)
	OurAddress(*NetAddress) bool
	MarkGood(ID)
	RemoveAddress(*NetAddress)
	HasAddress(*NetAddress) bool
	Save()
}

// PeerFilterFunc to be implemented by filter hooks after a new Peer has been
// fully setup.
type PeerFilterFunc func(IPeerSet, Peer) error

type privateAddr interface {
	PrivateAddr() bool
}

func isPrivateAddr(err error) bool {
	te, ok := err.(privateAddr)
	return ok && te.PrivateAddr()
}

// MConnConfig returns an MConnConfig with fields updated
// from the P2PConfig.
func MConnConfig(cfg *config.P2PConfig) conn.MConnConfig {
	mConfig := conn.DefaultMConnConfig()
	mConfig.FlushThrottle = cfg.FlushThrottleTimeout
	mConfig.SendRate = cfg.SendRate
	mConfig.RecvRate = cfg.RecvRate
	mConfig.MaxPacketMsgPayloadSize = cfg.MaxPacketMsgPayloadSize
	mConfig.TestFuzz = cfg.TestFuzz
	mConfig.TestFuzzConfig = cfg.TestFuzzConfig
	return mConfig
}

// PeerCriteria defines requirements for reactor-specific peer subsets
type PeerCriteria struct {
	// NodeTypes specifies acceptable node types (e.g., validator, full node)
	NodeTypes []string
	// Capabilities specifies required capabilities (e.g., "state_sync", "fast_sync")
	Capabilities []string
	// MaxPeers limits the number of peers for this reactor (0 = no limit)
	MaxPeers int
	// Persistent indicates if connections should be maintained persistently
	Persistent bool
}

// PeerManager interface defines the central hub for peer management
type PeerManager interface {
	service.Service

	// Core peer lifecycle
	AcceptPeer(peer Peer) error
	DialPeer(addr *NetAddress) error
	RemovePeer(peerID ID, reason interface{})
	StopPeerForError(peer Peer, reason interface{})
	StopPeerGracefully(peer Peer)

	// Reactor-specific peer management
	GetPeersForReactor(reactorName string) []Peer
	AssignPeerToReactor(peerID ID, reactorName string) error
	SetReactorPeerCriteria(reactorName string, criteria PeerCriteria)

	// PEX-specific operations
	NeedMorePeers() bool
	GetPeersForAddressExchange() []Peer
	AddAddressFromPEX(addr *NetAddress, source *NetAddress) error

	// Policy and configuration
	MarkPeerAsGood(peerID ID)
	AddPersistentPeers(addrs []string) error
	AddUnconditionalPeerIDs(ids []string) error
	IsPeerPersistent(addr *NetAddress) bool
	IsPeerUnconditional(id ID) bool

	// State queries
	NumPeers() (outbound, inbound, dialing int)
	MaxNumOutboundPeers() int
	GetPeer(peerID ID) Peer
	Peers() IPeerSet
	IsDialingOrExistingAddress(addr *NetAddress) bool

	// Address book integration
	SetAddrBook(addrBook AddrBook)
	OurAddress(addr *NetAddress) bool

	// Async dialing operations
	DialPeersAsync(peers []string) error

	// Internal methods for Switch integration
	SetReactorsByCh(reactorsByCh map[byte]Reactor)
	SetMsgTypeByChID(msgTypeByChID map[byte]proto.Message)
	SetChDescs(chDescs []*conn.ChannelDescriptor)
	SetStopPeerForErrorFunc(fn func(Peer, interface{}))
	SetTransport(transport Transport)
	SetFilterTimeout(timeout time.Duration)
	SetPeerFilters(filters []PeerFilterFunc)
	SetMetrics(metrics *Metrics)
	SetTraceClient(client trace.Tracer)
	SetNodeKey(nodeKey *NodeKey)
	SetNodeInfo(nodeInfo NodeInfo)
}

// defaultPeerManager implements PeerManager interface
type defaultPeerManager struct {
	service.BaseService

	config *config.P2PConfig

	// Peer state
	peers        *PeerSet
	dialing      *cmap.CMap
	reconnecting *cmap.CMap

	// Reactor integration
	reactorsByCh  map[byte]Reactor
	msgTypeByChID map[byte]proto.Message
	chDescs       []*conn.ChannelDescriptor

	// Reactor-specific peer assignments
	reactorPeers    map[string][]ID         // reactorName -> peer IDs
	reactorCriteria map[string]PeerCriteria // reactorName -> criteria

	// Configuration
	addrBook             AddrBook
	persistentPeersAddrs []*NetAddress
	unconditionalPeerIDs map[ID]struct{}
	filterTimeout        time.Duration
	peerFilters          []PeerFilterFunc
	ourAddrs             map[string]*NetAddress // Track our own addresses
	nodeKey              *NodeKey               // Our node's private key
	nodeInfo             NodeInfo               // Our node information

	// Dependencies
	transport            Transport
	stopPeerForErrorFunc func(Peer, interface{})

	// Utilities
	rng         *rand.Rand
	metrics     *Metrics
	mlc         *metricsLabelCache
	traceClient trace.Tracer

	// Synchronization
	mtx sync.RWMutex
}

// NewPeerManager creates a new peer manager
func NewPeerManager(cfg *config.P2PConfig) PeerManager {
	pm := &defaultPeerManager{
		config:               cfg,
		peers:                NewPeerSet(),
		dialing:              cmap.NewCMap(),
		reconnecting:         cmap.NewCMap(),
		reactorsByCh:         make(map[byte]Reactor),
		msgTypeByChID:        make(map[byte]proto.Message),
		chDescs:              make([]*conn.ChannelDescriptor, 0),
		reactorPeers:         make(map[string][]ID),
		reactorCriteria:      make(map[string]PeerCriteria),
		persistentPeersAddrs: make([]*NetAddress, 0),
		unconditionalPeerIDs: make(map[ID]struct{}),
		filterTimeout:        peerManagerFilterTimeout,
		peerFilters:          make([]PeerFilterFunc, 0),
		ourAddrs:             make(map[string]*NetAddress),
		metrics:              NopMetrics(),
		mlc:                  newMetricsLabelCache(),
		traceClient:          trace.NoOpTracer(),
	}

	// Ensure we have a completely undeterministic PRNG.
	pm.rng = rand.NewRand()

	pm.BaseService = *service.NewBaseService(nil, "PeerManager", pm)
	return pm
}

// OnStart implements service.Service
func (pm *defaultPeerManager) OnStart() error {
	// Start accepting connections - this will be handled by the transport/switch
	return nil
}

// OnStop implements service.Service
func (pm *defaultPeerManager) OnStop() {
	// Stop all peers
	for _, p := range pm.peers.List() {
		pm.stopAndRemovePeer(p, nil)
	}
}

// Core peer lifecycle methods

func (pm *defaultPeerManager) AcceptPeer(peer Peer) error {
	// Check if we have too many inbound peers (unless this is an unconditional peer)
	if !peer.IsOutbound() && !pm.IsPeerUnconditional(peer.ID()) {
		_, inbound, _ := pm.NumPeers()
		if inbound >= pm.config.MaxNumInboundPeers {
			pm.Logger.Info(
				"Ignoring inbound connection: already have enough inbound peers",
				"address", peer.SocketAddr(),
				"have", inbound,
				"max", pm.config.MaxNumInboundPeers,
			)
			return ErrRejected{id: peer.ID(), addr: *peer.SocketAddr(), err: fmt.Errorf("too many inbound peers")}
		}
	}

	return pm.addPeer(peer)
}

func (pm *defaultPeerManager) DialPeer(addr *NetAddress) error {
	// Check for self-connection before dialing
	if pm.nodeKey != nil && addr.ID == PubKeyToID(pm.nodeKey.PubKey()) {
		// Track this address as our own
		pm.ourAddrs[addr.String()] = addr
		// Also add to address book if available
		if pm.addrBook != nil {
			pm.addrBook.AddOurAddress(addr)
		}
		return ErrRejected{id: addr.ID, addr: *addr, isSelf: true}
	}

	if pm.IsDialingOrExistingAddress(addr) {
		return ErrCurrentlyDialingOrExistingAddress{addr.String()}
	}

	pm.dialing.Set(string(addr.ID), addr)
	defer pm.dialing.Delete(string(addr.ID))

	return pm.addOutboundPeerWithConfig(addr, pm.config)
}

func (pm *defaultPeerManager) RemovePeer(peerID ID, reason interface{}) {
	peer := pm.peers.Get(peerID)
	if peer != nil {
		pm.stopAndRemovePeer(peer, reason)
	}
}

func (pm *defaultPeerManager) StopPeerForError(peer Peer, reason interface{}) {
	pm.Logger.Error("Stopping peer for error", "peer", peer, "err", reason)
	pm.stopAndRemovePeer(peer, reason)
}

func (pm *defaultPeerManager) StopPeerGracefully(peer Peer) {
	pm.Logger.Info("Stopping peer gracefully")
	pm.stopAndRemovePeer(peer, nil)
}

// State queries

func (pm *defaultPeerManager) NumPeers() (outbound, inbound, dialing int) {
	peers := pm.peers.List()
	for _, peer := range peers {
		if peer.IsOutbound() {
			if !pm.IsPeerUnconditional(peer.ID()) {
				outbound++
			}
		} else {
			if !pm.IsPeerUnconditional(peer.ID()) {
				inbound++
			}
		}
	}
	dialing = pm.dialing.Size()
	return
}

func (pm *defaultPeerManager) MaxNumOutboundPeers() int {
	return pm.config.MaxNumOutboundPeers
}

func (pm *defaultPeerManager) GetPeer(peerID ID) Peer {
	return pm.peers.Get(peerID)
}

func (pm *defaultPeerManager) Peers() IPeerSet {
	return pm.peers
}

func (pm *defaultPeerManager) IsDialingOrExistingAddress(addr *NetAddress) bool {
	return pm.dialing.Has(string(addr.ID)) ||
		pm.peers.Has(addr.ID) ||
		(!pm.config.AllowDuplicateIP && pm.peers.HasIP(addr.IP))
}

// Configuration methods

func (pm *defaultPeerManager) SetAddrBook(addrBook AddrBook) {
	pm.addrBook = addrBook
}

func (pm *defaultPeerManager) OurAddress(addr *NetAddress) bool {
	_, exists := pm.ourAddrs[addr.String()]
	return exists
}

func (pm *defaultPeerManager) MarkPeerAsGood(peerID ID) {
	if pm.addrBook != nil {
		pm.addrBook.MarkGood(peerID)
	}
}

func (pm *defaultPeerManager) IsPeerUnconditional(id ID) bool {
	_, ok := pm.unconditionalPeerIDs[id]
	return ok
}

// Internal helper methods (moved from Switch)

func (pm *defaultPeerManager) stopAndRemovePeer(peer Peer, reason interface{}) {
	if pm.transport != nil {
		pm.transport.Cleanup(peer)
	}
	if err := peer.Stop(); err != nil {
		pm.Logger.Error("error while stopping peer", "error", err)
	}
	schema.WritePeerUpdate(pm.traceClient, string(peer.ID()), schema.PeerDisconnect, fmt.Sprintf("%v", reason))

	// Check if this is a persistent peer and trigger reconnection
	peerAddr := peer.SocketAddr()

	// Find the persistent peer address by ID (not by full address, since ports can differ)
	var persistentAddr *NetAddress
	for _, pa := range pm.persistentPeersAddrs {
		if pa.ID == peerAddr.ID {
			persistentAddr = pa
			break
		}
	}

	if persistentAddr != nil {
		pm.Logger.Info("Persistent peer disconnected, will attempt to reconnect", "peer", peerAddr, "persistentAddr", persistentAddr)
		go pm.reconnectToPeer(persistentAddr)
	} else {
		pm.Logger.Debug("Non-persistent peer disconnected, no reconnection needed", "peer", peerAddr)
	}

	// Notify reactors through the callback if available
	if pm.stopPeerForErrorFunc != nil {
		// This will be called by Switch to notify reactors
		// We don't call reactors directly to maintain separation
	}

	// Remove from reactor assignments
	pm.mtx.Lock()
	for reactorName, peerIDs := range pm.reactorPeers {
		for i, id := range peerIDs {
			if id == peer.ID() {
				pm.reactorPeers[reactorName] = append(peerIDs[:i], peerIDs[i+1:]...)
				break
			}
		}
	}
	pm.mtx.Unlock()

	// Removing a peer should go last to avoid race conditions
	if pm.peers.Remove(peer) {
		pm.metrics.Peers.Add(float64(-1))
	} else {
		pm.Logger.Debug("error on peer removal", "peer", peer.ID())
	}
}

func (pm *defaultPeerManager) addPeer(p Peer) error {
	if err := pm.filterPeer(p); err != nil {
		return err
	}

	p.SetLogger(pm.Logger.With("peer", p.SocketAddr()))

	// Handle the shut down case
	if !pm.IsRunning() {
		pm.Logger.Error("Won't start a peer - peer manager is not running", "peer", p)
		return nil
	}

	// Start the peer's send/recv routines
	err := p.Start()
	if err != nil {
		pm.Logger.Error("Error starting peer", "err", err, "peer", p)
		return err
	}

	// Add the peer to PeerSet
	if err := pm.peers.Add(p); err != nil {
		switch err.(type) {
		case ErrPeerRemoval:
			pm.Logger.Error("Error starting peer - peer has already errored", "peer", p.ID())
		}
		return err
	}
	pm.metrics.Peers.Add(float64(1))
	schema.WritePeerUpdate(pm.traceClient, string(p.ID()), schema.PeerJoin, "")

	// Assign peer to reactors based on criteria
	pm.assignPeerToReactors(p)

	pm.Logger.Debug("Added peer", "peer", p)
	return nil
}

func (pm *defaultPeerManager) filterPeer(p Peer) error {
	// Check for self-connection
	if pm.nodeKey != nil && p.ID() == PubKeyToID(pm.nodeKey.PubKey()) {
		// Track this address as our own
		pm.ourAddrs[p.SocketAddr().String()] = p.SocketAddr()
		// Also add to address book if available
		if pm.addrBook != nil {
			pm.addrBook.AddOurAddress(p.SocketAddr())
		}
		return ErrRejected{id: p.ID(), addr: *p.SocketAddr(), isSelf: true}
	}

	// Avoid duplicate
	if pm.peers.Has(p.ID()) {
		return ErrRejected{id: p.ID(), isDuplicate: true}
	}

	errc := make(chan error, len(pm.peerFilters))
	for _, f := range pm.peerFilters {
		go func(f PeerFilterFunc, p Peer, errc chan<- error) {
			errc <- f(pm.peers, p)
		}(f, p, errc)
	}

	for i := 0; i < cap(errc); i++ {
		select {
		case err := <-errc:
			if err != nil {
				return ErrRejected{id: p.ID(), err: err, isFiltered: true}
			}
		case <-time.After(pm.filterTimeout):
			return ErrFilterTimeout{}
		}
	}

	return nil
}

// Placeholder implementations for remaining methods
// These will be implemented in subsequent steps

func (pm *defaultPeerManager) GetPeersForReactor(reactorName string) []Peer {
	pm.mtx.RLock()
	defer pm.mtx.RUnlock()

	peerIDs, exists := pm.reactorPeers[reactorName]
	if !exists {
		// Return all peers if no specific criteria set (backward compatibility)
		return pm.peers.List()
	}

	peers := make([]Peer, 0, len(peerIDs))
	for _, id := range peerIDs {
		if peer := pm.peers.Get(id); peer != nil {
			peers = append(peers, peer)
		}
	}
	return peers
}

func (pm *defaultPeerManager) AssignPeerToReactor(peerID ID, reactorName string) error {
	pm.mtx.Lock()
	defer pm.mtx.Unlock()

	if pm.reactorPeers[reactorName] == nil {
		pm.reactorPeers[reactorName] = make([]ID, 0)
	}

	// Check if already assigned
	for _, id := range pm.reactorPeers[reactorName] {
		if id == peerID {
			return nil // Already assigned
		}
	}

	pm.reactorPeers[reactorName] = append(pm.reactorPeers[reactorName], peerID)
	return nil
}

func (pm *defaultPeerManager) SetReactorPeerCriteria(reactorName string, criteria PeerCriteria) {
	pm.mtx.Lock()
	defer pm.mtx.Unlock()
	pm.reactorCriteria[reactorName] = criteria
}

func (pm *defaultPeerManager) assignPeerToReactors(p Peer) {
	pm.mtx.Lock()
	defer pm.mtx.Unlock()

	// For now, assign to all reactors (backward compatibility)
	// TODO: Implement criteria-based assignment in next iteration
	for reactorName := range pm.reactorCriteria {
		pm.reactorPeers[reactorName] = append(pm.reactorPeers[reactorName], p.ID())
	}
}

// Additional methods will be implemented in the next steps
func (pm *defaultPeerManager) NeedMorePeers() bool {
	// Implement based on configuration and current peer count
	outbound, _, _ := pm.NumPeers()
	return outbound < pm.config.MaxNumOutboundPeers
}

func (pm *defaultPeerManager) GetPeersForAddressExchange() []Peer {
	// Return subset suitable for PEX
	return pm.peers.List()
}

func (pm *defaultPeerManager) AddAddressFromPEX(addr *NetAddress, source *NetAddress) error {
	if pm.addrBook != nil {
		return pm.addrBook.AddAddress(addr, source)
	}
	return nil
}

func (pm *defaultPeerManager) AddPersistentPeers(addrs []string) error {
	pm.Logger.Info("Adding persistent peers", "addrs", addrs)
	netAddrs, errs := NewNetAddressStrings(addrs)
	for _, err := range errs {
		pm.Logger.Error("Error in peer's address", "err", err)
	}
	for _, err := range errs {
		if _, ok := err.(ErrNetAddressLookup); ok {
			continue
		}
		return err
	}
	pm.persistentPeersAddrs = netAddrs
	return nil
}

func (pm *defaultPeerManager) AddUnconditionalPeerIDs(ids []string) error {
	pm.Logger.Info("Adding unconditional peer ids", "ids", ids)
	for i, id := range ids {
		err := validateID(ID(id))
		if err != nil {
			return fmt.Errorf("wrong ID #%d: %w", i, err)
		}
		pm.unconditionalPeerIDs[ID(id)] = struct{}{}
	}
	return nil
}

func (pm *defaultPeerManager) IsPeerPersistent(addr *NetAddress) bool {
	for _, pa := range pm.persistentPeersAddrs {
		// Compare by peer ID instead of full address, since connection address can have different port
		if pa.ID == addr.ID {
			return true
		}
	}
	return false
}

func (pm *defaultPeerManager) DialPeersAsync(peers []string) error {
	netAddrs, errs := NewNetAddressStrings(peers)
	for _, err := range errs {
		pm.Logger.Error("Error in peer's address", "err", err)
	}
	for _, err := range errs {
		if _, ok := err.(ErrNetAddressLookup); ok {
			continue
		}
		return err
	}
	pm.dialPeersAsync(netAddrs)
	return nil
}

// Setter methods for Switch integration
func (pm *defaultPeerManager) SetReactorsByCh(reactorsByCh map[byte]Reactor) {
	pm.reactorsByCh = reactorsByCh
}

func (pm *defaultPeerManager) SetMsgTypeByChID(msgTypeByChID map[byte]proto.Message) {
	pm.msgTypeByChID = msgTypeByChID
}

func (pm *defaultPeerManager) SetChDescs(chDescs []*conn.ChannelDescriptor) {
	pm.chDescs = chDescs
}

func (pm *defaultPeerManager) SetStopPeerForErrorFunc(fn func(Peer, interface{})) {
	pm.stopPeerForErrorFunc = fn
}

func (pm *defaultPeerManager) SetTransport(transport Transport) {
	pm.transport = transport
}

func (pm *defaultPeerManager) SetFilterTimeout(timeout time.Duration) {
	pm.filterTimeout = timeout
}

func (pm *defaultPeerManager) SetPeerFilters(filters []PeerFilterFunc) {
	pm.peerFilters = filters
}

func (pm *defaultPeerManager) SetMetrics(metrics *Metrics) {
	pm.metrics = metrics
}

func (pm *defaultPeerManager) SetTraceClient(client trace.Tracer) {
	pm.traceClient = client
}

func (pm *defaultPeerManager) SetNodeKey(nodeKey *NodeKey) {
	pm.nodeKey = nodeKey
}

func (pm *defaultPeerManager) SetNodeInfo(nodeInfo NodeInfo) {
	pm.nodeInfo = nodeInfo
}

// Helper methods that will be moved from Switch

func (pm *defaultPeerManager) dialPeersAsync(netAddrs []*NetAddress) {
	// TODO: Need access to transport's NetAddress() method
	// For now, skip the ourAddr check - this will be fixed when integrating with switch

	if pm.addrBook != nil {
		// add peers to `addrBook`
		for _, netAddr := range netAddrs {
			// TODO: Need to check if netAddr is same as our address
			if err := pm.addrBook.AddAddress(netAddr, netAddr); err != nil {
				if isPrivateAddr(err) {
					pm.Logger.Debug("Won't add peer's address to addrbook", "err", err)
				} else {
					pm.Logger.Error("Can't add peer's address to addrbook", "err", err)
				}
			}
		}
		// Persist some peers to disk right away.
		// NOTE: integration tests depend on this
		pm.addrBook.Save()
	}

	// permute the list, dial them in random order.
	perm := pm.rng.Perm(len(netAddrs))
	for i := 0; i < len(perm); i++ {
		go func(i int) {
			j := perm[i]
			addr := netAddrs[j]

			pm.randomSleep(0)

			err := pm.DialPeer(addr)
			if err != nil {
				switch err.(type) {
				case ErrSwitchConnectToSelf, ErrSwitchDuplicatePeerID, ErrCurrentlyDialingOrExistingAddress:
					pm.Logger.Debug("Error dialing peer", "err", err)
				default:
					pm.Logger.Error("Error dialing peer", "err", err)
				}
			}
		}(i)
	}
}

func (pm *defaultPeerManager) addOutboundPeerWithConfig(addr *NetAddress, cfg *config.P2PConfig) error {
	pm.Logger.Debug("Dialing peer", "address", addr)

	// XXX(xla): Remove the leakage of test concerns in implementation.
	if cfg.TestDialFail {
		go pm.reconnectToPeer(addr)
		return fmt.Errorf("dial err (peerConfig.DialFail == true)")
	}

	if pm.transport == nil {
		return fmt.Errorf("transport not set")
	}

	p, err := pm.transport.Dial(*addr, peerConfig{
		chDescs:       pm.chDescs,
		onPeerError:   pm.StopPeerForError,
		isPersistent:  pm.IsPeerPersistent,
		reactorsByCh:  pm.reactorsByCh,
		msgTypeByChID: pm.msgTypeByChID,
		metrics:       pm.metrics,
		mlc:           pm.mlc,
	})
	if err != nil {
		if e, ok := err.(ErrRejected); ok {
			if e.IsSelf() {
				// Remove the given address from the address book and add to our addresses
				// to avoid dialing in the future.
				if pm.addrBook != nil {
					pm.addrBook.RemoveAddress(addr)
					pm.addrBook.AddOurAddress(addr)
				}
				return err
			}
		}

		// retry persistent peers after
		// any dial error besides IsSelf()
		if pm.IsPeerPersistent(addr) {
			go pm.reconnectToPeer(addr)
		}

		return err
	}

	if err := pm.addPeer(p); err != nil {
		if pm.transport != nil {
			pm.transport.Cleanup(p)
		}
		if p.IsRunning() {
			_ = p.Stop()
		}
		return err
	}

	return nil
}

// randomSleep sleeps for interval plus some random amount of ms
func (pm *defaultPeerManager) randomSleep(interval time.Duration) {
	r := time.Duration(pm.rng.Int63n(dialRandomizerIntervalMilliseconds)) * time.Millisecond
	time.Sleep(r + interval)
}

// reconnectToPeer tries to reconnect to the addr with exponential backoff
func (pm *defaultPeerManager) reconnectToPeer(addr *NetAddress) {
	if pm.reconnecting.Has(string(addr.ID)) {
		return
	}
	pm.reconnecting.Set(string(addr.ID), addr)
	defer pm.reconnecting.Delete(string(addr.ID))

	start := time.Now()
	pm.Logger.Info("Reconnecting to peer", "addr", addr)

	for i := 0; i < reconnectAttempts; i++ {
		if !pm.IsRunning() {
			return
		}

		err := pm.DialPeer(addr)
		if err == nil {
			return // success
		} else if _, ok := err.(ErrCurrentlyDialingOrExistingAddress); ok {
			return
		}

		pm.Logger.Info("Error reconnecting to peer. Trying again", "tries", i, "err", err, "addr", addr)
		// sleep a set amount
		pm.randomSleep(reconnectInterval)
		continue
	}

	pm.Logger.Error("Failed to reconnect to peer. Beginning exponential backoff",
		"addr", addr, "elapsed", time.Since(start))
	for i := 1; i <= reconnectBackOffAttempts; i++ {
		if !pm.IsRunning() {
			return
		}

		// sleep an exponentially increasing amount
		sleepIntervalSeconds := math.Pow(reconnectBackOffBaseSeconds, float64(i))
		pm.randomSleep(time.Duration(sleepIntervalSeconds) * time.Second)

		err := pm.DialPeer(addr)
		if err == nil {
			return // success
		} else if _, ok := err.(ErrCurrentlyDialingOrExistingAddress); ok {
			return
		}
		pm.Logger.Info("Error reconnecting to peer. Trying again", "tries", i, "err", err, "addr", addr)
	}
	pm.Logger.Error("Failed to reconnect to peer. Giving up", "addr", addr, "elapsed", time.Since(start))
}
