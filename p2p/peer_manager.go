package p2p

import (
	"fmt"
	"math"
	"time"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/cmap"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace/schema"
)

type PeerManager struct {
	service.BaseService

	sw       *Switch
	addrBook AddrBook
	config   *config.P2PConfig

	peers        *PeerSet
	dialing      *cmap.CMap
	reconnecting *cmap.CMap
	// peers addresses with whom we'll maintain constant connection
	persistentPeersAddrs []*NetAddress
	unconditionalPeerIDs map[ID]struct{}
	filterTimeout        time.Duration
	peerFilters          []PeerFilterFunc

	rng *rand.Rand // seed for randomizing dial times and orders

	metrics *Metrics
}

func NewPeerManager() *PeerManager {
	pm := &PeerManager{
		BaseService:          *service.NewBaseService(nil, "PeerManager", nil),
		peers:                NewPeerSet(),
		dialing:              cmap.NewCMap(),
		reconnecting:         cmap.NewCMap(),
		persistentPeersAddrs: make([]*NetAddress, 0),
		unconditionalPeerIDs: make(map[ID]struct{}),
		filterTimeout:        defaultFilterTimeout,
	}
	// Ensure we have a completely undeterministic PRNG.
	pm.rng = rand.NewRand()

	return pm
}

func (pm *PeerManager) OnStart() error {
	// Start accepting Peers.
	go pm.acceptRoutine()

	return nil
}

func (pm *PeerManager) OnStop() {
	// Stop peers
	for _, p := range pm.peers.List() {
		pm.stopAndRemovePeer(p, nil)
	}
}

// SwitchFilterTimeout sets the timeout used for peer filters.
func SwitchFilterTimeout(timeout time.Duration) PeerManagerOption {
	return func(pm *PeerManager) { pm.filterTimeout = timeout }
}

// SwitchPeerFilters sets the filters for rejection of new peers.
func SwitchPeerFilters(filters ...PeerFilterFunc) PeerManagerOption {
	return func(pm *PeerManager) { pm.peerFilters = filters }
}

func (pm *PeerManager) stopAndRemovePeer(peer Peer, reason interface{}) {
	pm.sw.stopAndRemovePeer(peer, reason)

	// Removing a peer should go last to avoid a situation where a peer
	// reconnect to our node and the switch calls InitPeer before
	// RemovePeer is finished.
	// https://github.com/tendermint/tendermint/issues/3338
	if pm.peers.Remove(peer) {
		pm.metrics.Peers.Add(float64(-1))
	} else {
		// Removal of the peer has failed. The function above sets a flag within the peer to mark this.
		// We keep this message here as information to the developer.
		pm.Logger.Debug("error on peer removal", ",", "peer", peer.ID())
	}
}

// NumPeers returns the count of outbound/inbound and outbound-dialing peers.
// unconditional peers are not counted here.
func (pm *PeerManager) NumPeers() (outbound, inbound, dialing int) {
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

// Peers returns the set of peers that are connected to the switch.
func (pm *PeerManager) Peers() IPeerSet {
	return pm.peers
}

func (pm *PeerManager) IsPeerUnconditional(id ID) bool {
	_, ok := pm.unconditionalPeerIDs[id]
	return ok
}

// reconnectToPeer tries to reconnect to the addr, first repeatedly
// with a fixed interval (approximately 2 minutes), then with
// exponential backoff (approximately close to 24 hours).
// If no success after all that, it stops trying, and leaves it
// to the PEX/Addrbook to find the peer with the addr again
// NOTE: this will keep trying even if the handshake or auth fails.
// TODO: be more explicit with error types so we only retry on certain failures
//   - ie. if we're getting ErrDuplicatePeer we can stop
//     because the addrbook got us the peer back already
func (pm *PeerManager) reconnectToPeer(addr *NetAddress) {
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

		err := pm.DialPeerWithAddress(addr)
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

		err := pm.DialPeerWithAddress(addr)
		if err == nil {
			return // success
		} else if _, ok := err.(ErrCurrentlyDialingOrExistingAddress); ok {
			return
		}
		pm.Logger.Info("Error reconnecting to peer. Trying again", "tries", i, "err", err, "addr", addr)
	}
	pm.Logger.Error("Failed to reconnect to peer. Giving up", "addr", addr, "elapsed", time.Since(start))
}

// MaxNumOutboundPeers returns a maximum number of outbound peers.
func (pm *PeerManager) MaxNumOutboundPeers() int {
	return pm.config.MaxNumOutboundPeers
}

// StopPeerForError disconnects from a peer due to external error.
// If the peer is persistent, it will attempt to reconnect.
// TODO: make record depending on reason.
func (pm *PeerManager) StopPeerForError(peer Peer, reason interface{}) {
	if !peer.IsRunning() {
		return
	}

	pm.Logger.Error("Stopping peer for error", "peer", peer, "err", reason)
	pm.stopAndRemovePeer(peer, reason)

	if peer.IsPersistent() {
		addr, err := pm.getPeerAddress(peer)
		if err != nil {
			pm.Logger.Error("Failed to get address for persistent peer", "peer", peer, "err", err)
			return
		}
		go pm.reconnectToPeer(addr)
	}

	if peer.HasIPChanged() {
		addr, err := pm.getPeerAddress(peer)
		if err != nil {
			pm.Logger.Error("Failed to get address for peer with changed IP", "peer", peer, "err", err)
		}
		go pm.reconnectToPeer(addr)
	}
}

// getPeerAddress returns the appropriate NetAddress for a given peer,
// handling both outbound and inbound peers.
func (pm *PeerManager) getPeerAddress(peer Peer) (*NetAddress, error) {
	if peer.IsOutbound() {
		return peer.SocketAddr(), nil
	}
	// For inbound peers, get the self-reported address
	addr, err := peer.NodeInfo().NetAddress()
	if err != nil {
		pm.Logger.Error("Failed to get address for inbound peer",
			"peer", peer, "err", err)
		return nil, err
	}
	return addr, nil
}

// StopPeerGracefully disconnects from a peer gracefully.
// TODO: handle graceful disconnects.
func (pm *PeerManager) StopPeerGracefully(peer Peer) {
	pm.Logger.Info("Stopping peer gracefully")
	pm.stopAndRemovePeer(peer, nil)
}

// SetAddrBook allows to set address book on PeerManager.
func (pm *PeerManager) SetAddrBook(addrBook AddrBook) {
	pm.addrBook = addrBook
}

// MarkPeerAsGood marks the given peer as good when it did something useful
// like contributed to consensus.
func (pm *PeerManager) MarkPeerAsGood(peer Peer) {
	if pm.addrBook != nil {
		pm.addrBook.MarkGood(peer.ID())
	}
}

//---------------------------------------------------------------------
// Dialing

type privateAddr interface {
	PrivateAddr() bool
}

func isPrivateAddr(err error) bool {
	te, ok := err.(privateAddr)
	return ok && te.PrivateAddr()
}

// DialPeersAsync dials a list of peers asynchronously in random order.
// Used to dial peers from config on startup or from unsafe-RPC (trusted sources).
// It ignores ErrNetAddressLookup. However, if there are other errors, first
// encounter is returned.
// Nop if there are no peers.
func (pm *PeerManager) DialPeersAsync(peers []string) error {
	netAddrs, errs := NewNetAddressStrings(peers)
	// report all the errors
	for _, err := range errs {
		pm.Logger.Error("Error in peer's address", "err", err)
	}
	// return first non-ErrNetAddressLookup error
	for _, err := range errs {
		if _, ok := err.(ErrNetAddressLookup); ok {
			continue
		}
		return err
	}
	pm.dialPeersAsync(netAddrs)
	return nil
}

func (pm *PeerManager) dialPeersAsync(netAddrs []*NetAddress) {
	ourAddr := pm.sw.NetAddress() // TODO(tzdybal)

	// TODO: this code feels like it's in the wrong place.
	// The integration tests depend on the addrBook being saved
	// right away but maybe we can change that. Recall that
	// the addrBook is only written to disk every 2min
	if pm.addrBook != nil {
		// add peers to `addrBook`
		for _, netAddr := range netAddrs {
			// do not add our address or ID
			if !netAddr.Same(ourAddr) {
				if err := pm.addrBook.AddAddress(netAddr, ourAddr); err != nil {
					if isPrivateAddr(err) {
						pm.Logger.Debug("Won't add peer's address to addrbook", "err", err)
					} else {
						pm.Logger.Error("Can't add peer's address to addrbook", "err", err)
					}
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

			if addr.Same(ourAddr) {
				pm.Logger.Debug("Ignore attempt to connect to ourselves", "addr", addr, "ourAddr", ourAddr)
				return
			}

			pm.randomSleep(0)

			err := pm.DialPeerWithAddress(addr)
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

// DialPeerWithAddress dials the given peer and runs sw.addPeer if it connects
// and authenticates successfully.
// If we're currently dialing this address or it belongs to an existing peer,
// ErrCurrentlyDialingOrExistingAddress is returned.
func (pm *PeerManager) DialPeerWithAddress(addr *NetAddress) error {
	if pm.IsDialingOrExistingAddress(addr) {
		return ErrCurrentlyDialingOrExistingAddress{addr.String()}
	}

	pm.dialing.Set(string(addr.ID), addr)
	defer pm.dialing.Delete(string(addr.ID))

	return pm.addOutboundPeerWithConfig(addr, pm.config)
}

// IsDialingOrExistingAddress returns true if switch has a peer with the given
// address or dialing it at the moment.
func (pm *PeerManager) IsDialingOrExistingAddress(addr *NetAddress) bool {
	return pm.dialing.Has(string(addr.ID)) ||
		pm.peers.Has(addr.ID) || // TODO(tzdybal)
		(!pm.config.AllowDuplicateIP && pm.peers.HasIP(addr.IP))
}

// AddPersistentPeers allows you to set persistent peers. It ignores
// ErrNetAddressLookup. However, if there are other errors, first encounter is
// returned.
func (pm *PeerManager) AddPersistentPeers(addrs []string) error {
	pm.Logger.Info("Adding persistent peers", "addrs", addrs)
	netAddrs, errs := NewNetAddressStrings(addrs)
	// report all the errors
	for _, err := range errs {
		pm.Logger.Error("Error in peer's address", "err", err)
	}
	// return first non-ErrNetAddressLookup error
	for _, err := range errs {
		if _, ok := err.(ErrNetAddressLookup); ok {
			continue
		}
		return err
	}
	pm.persistentPeersAddrs = netAddrs
	return nil
}

func (pm *PeerManager) AddUnconditionalPeerIDs(ids []string) error {
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

func (pm *PeerManager) AddPrivatePeerIDs(ids []string) error {
	validIDs := make([]string, 0, len(ids))
	for i, id := range ids {
		err := validateID(ID(id))
		if err != nil {
			return fmt.Errorf("wrong ID #%d: %w", i, err)
		}
		validIDs = append(validIDs, id)
	}

	pm.addrBook.AddPrivateIDs(validIDs)

	return nil
}

func (pm *PeerManager) IsPeerPersistent(na *NetAddress) bool {
	for _, pa := range pm.persistentPeersAddrs {
		if pa.Equals(na) {
			return true
		}
	}
	return false
}

func (pm *PeerManager) acceptRoutine() {
	for {
		p, err := pm.transport.Accept(peerConfig{
			chDescs:       pm.chDescs,
			onPeerError:   pm.StopPeerForError,
			reactorsByCh:  pm.reactorsByCh,
			msgTypeByChID: pm.msgTypeByChID,
			metrics:       pm.metrics,
			mlc:           pm.mlc,
			isPersistent:  pm.IsPeerPersistent,
		})
		if err != nil {
			switch err := err.(type) {
			case ErrRejected:
				if err.IsSelf() {
					// Remove the given address from the address book and add to our addresses
					// to avoid dialing in the future.
					addr := err.Addr()
					pm.addrBook.RemoveAddress(&addr)
					pm.addrBook.AddOurAddress(&addr)
				}

				pm.Logger.Info(
					"Inbound Peer rejected",
					"err", err,
					"numPeers", pm.peers.Size(),
				)

				continue
			case ErrFilterTimeout:
				pm.Logger.Error(
					"Peer filter timed out",
					"err", err,
				)

				continue
			case ErrTransportClosed:
				pm.Logger.Error(
					"Stopped accept routine, as transport is closed",
					"numPeers", pm.peers.Size(),
				)
			default:
				pm.Logger.Error(
					"Accept on transport errored",
					"err", err,
					"numPeers", pm.peers.Size(),
				)
				// We could instead have a retry loop around the acceptRoutine,
				// but that would need to stop and let the node shutdown eventually.
				// So might as well panic and let process managers restart the node.
				// There's no point in letting the node run without the acceptRoutine,
				// since it won't be able to accept new connections.
				panic(fmt.Errorf("accept routine exited: %v", err))
			}

			break
		}

		if !pm.IsPeerUnconditional(p.NodeInfo().ID()) {
			// Ignore connection if we already have enough peers.
			_, in, _ := pm.NumPeers()
			if in >= pm.config.MaxNumInboundPeers {
				pm.Logger.Info(
					"Ignoring inbound connection: already have enough inbound peers",
					"address", p.SocketAddr(),
					"have", in,
					"max", pm.config.MaxNumInboundPeers,
				)

				pm.transport.Cleanup(p)

				continue
			}

		}

		if err := pm.addPeer(p); err != nil {
			pm.transport.Cleanup(p)
			if p.IsRunning() {
				_ = p.Stop()
			}
			pm.Logger.Info(
				"Ignoring inbound connection: error while adding peer",
				"err", err,
				"id", p.ID(),
			)
		}
	}
}

// dial the peer; make secret connection; authenticate against the dialed ID;
// add the peer.
// if dialing fails, start the reconnect loop. If handshake fails, it's over.
// If peer is started successfully, reconnectLoop will start when
// StopPeerForError is called.
func (sw *PeerManager) addOutboundPeerWithConfig(
	addr *NetAddress,
	cfg *config.P2PConfig,
) error {
	sw.Logger.Debug("Dialing peer", "address", addr)

	// XXX(xla): Remove the leakage of test concerns in implementation.
	if cfg.TestDialFail {
		go sw.reconnectToPeer(addr)
		return fmt.Errorf("dial err (peerConfig.DialFail == true)")
	}

	p, err := sw.transport.Dial(*addr, peerConfig{
		chDescs:       sw.chDescs,
		onPeerError:   sw.StopPeerForError,
		isPersistent:  sw.IsPeerPersistent,
		reactorsByCh:  sw.reactorsByCh,
		msgTypeByChID: sw.msgTypeByChID,
		metrics:       sw.metrics,
		mlc:           sw.mlc,
	})
	if err != nil {
		if e, ok := err.(ErrRejected); ok {
			if e.IsSelf() {
				// Remove the given address from the address book and add to our addresses
				// to avoid dialing in the future.
				sw.addrBook.RemoveAddress(addr)
				sw.addrBook.AddOurAddress(addr)

				return err
			}
		}

		// retry persistent peers after
		// any dial error besides IsSelf()
		if sw.IsPeerPersistent(addr) {
			go sw.reconnectToPeer(addr)
		}

		return err
	}

	if err := sw.addPeer(p); err != nil {
		sw.transport.Cleanup(p)
		if p.IsRunning() {
			_ = p.Stop()
		}
		return err
	}

	return nil
}

func (pm *PeerManager) filterPeer(p Peer) error {
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

// addPeer starts up the Peer and adds it to the PeerManager. Error is returned if
// the peer is filtered out or failed to start or can't be added.
func (pm *PeerManager) addPeer(p Peer) error {
	if err := pm.filterPeer(p); err != nil {
		return err
	}

	p.SetLogger(pm.Logger.With("peer", p.SocketAddr()))

	// Handle the shut down case where the switch has stopped but we're
	// concurrently trying to add a peer.
	if !pm.IsRunning() {
		// XXX should this return an error or just log and terminate?
		pm.Logger.Error("Won't start a peer - switch is not running", "peer", p)
		return nil
	}

	pm.sw.addPeer(p)

	// Start the peer's send/recv routines.
	// Must start it before adding it to the peer set
	// to prevent Start and Stop from being called concurrently.
	err := p.Start()
	if err != nil {
		// Should never happen
		pm.Logger.Error("Error starting peer", "err", err, "peer", p)
		return err
	}

	// Add the peer to PeerSet. Do this before starting the reactors
	// so that if Receive errors, we will find the peer and remove it.
	// Add should not err since we already checked peers.Has().
	if err := pm.peers.Add(p); err != nil {
		switch err.(type) {
		case ErrPeerRemoval:
			pm.Logger.Error("Error starting peer ",
				" err ", "Peer has already errored and removal was attempted.",
				"peer", p.ID())
		}
		return err
	}
	pm.metrics.Peers.Add(float64(1))
	schema.WritePeerUpdate(pm.traceClient, string(p.ID()), schema.PeerJoin, "")

	// Start all the reactor protocols on the peer.
	for _, reactor := range pm.reactors {
		reactor.AddPeer(p)
	}

	pm.Logger.Debug("Added peer", "peer", p)

	return nil
}

// sleep for interval plus some random amount of ms on [0, dialRandomizerIntervalMilliseconds]
func (pm *PeerManager) randomSleep(interval time.Duration) {
	r := time.Duration(pm.rng.Int63n(dialRandomizerIntervalMilliseconds)) * time.Millisecond
	time.Sleep(r + interval)
}
