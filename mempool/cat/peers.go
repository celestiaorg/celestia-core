package cat

import (
	"fmt"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
)

// firstPeerID is the first valid short ID to be assigned to a peer. 
// ID 0 (UnknownPeerID) is reserved for internal use (e.g., local transaction broadcasting).
const firstPeerID = mempool.UnknownPeerID + 1

// mempoolIDs is a thread-safe structure that maps long P2P Peer IDs (p2p.ID) 
// to short uint16 IDs, which are used internally by the Mempool Reactor for efficient 
// tracking of peer messages and state (e.g., seen transactions).
type mempoolIDs struct {
	mtx       tmsync.RWMutex
	peerMap   map[p2p.ID]uint16  // Maps Peer ID (long) to short ID (uint16)
	activeIDs map[uint16]p2p.Peer // Maps short ID to the active Peer object
	nextID    uint16             // The next available ID candidate to check
}

// newMempoolIDs initializes the mempoolIDs structure.
func newMempoolIDs() *mempoolIDs {
	return &mempoolIDs{
		peerMap:   make(map[p2p.ID]uint16),
		activeIDs: make(map[uint16]p2p.Peer),
		// Start searching for IDs from 1, reserving 0 (UnknownPeerID).
		nextID: firstPeerID, 
	}
}

// ReserveForPeer assigns the next unused short ID to the given peer.
// If the peer is already registered (e.g., during reconnection), the existing 
// ID is retained, but the Peer object in activeIDs is updated.
func (ids *mempoolIDs) ReserveForPeer(peer p2p.Peer) {
	ids.mtx.Lock()
	defer ids.mtx.Unlock()

	// Check if this peer ID already exists (e.g., successful reconnection)
	if curID, ok := ids.peerMap[peer.ID()]; ok {
		// Update the active Peer object associated with the existing short ID
		ids.activeIDs[curID] = peer
		return
	}

	// Assign a new short ID
	curID := ids.nextPeerID()
	ids.peerMap[peer.ID()] = curID
	ids.activeIDs[curID] = peer
}

// nextPeerID finds the next unused peer ID.
// NOTE: This method assumes ids's mutex is already locked by the caller.
func (ids *mempoolIDs) nextPeerID() uint16 {
	// Panic if the maximum allowed number of active IDs has been reached.
	if len(ids.activeIDs) == mempool.MaxActiveIDs {
		panic(fmt.Sprintf("node has reached the maximum limit of %d active IDs", mempool.MaxActiveIDs))
	}

	// Linear search for the next available ID starting from ids.nextID.
	// This handles wrapping and gaps created by reclaimed IDs.
	currentID := ids.nextID
	_, idExists := ids.activeIDs[currentID]
	for idExists {
		currentID++
		// The use of uint16 ensures wrapping (e.g., 65535 + 1 = 0), though ID 0 is reserved.
		// The max active ID check prevents infinite looping if the map is full.
		if currentID == mempool.UnknownPeerID {
			currentID = firstPeerID // Skip reserved ID 0
		}
		
		_, idExists = ids.activeIDs[currentID]
	}
	
	// Update nextID to continue the search from the point after the current assignment.
	ids.nextID = currentID + 1
	return currentID
}

// Reclaim removes the short ID reserved for the peer and returns it to the unused pool.
// Returns the reclaimed ID if found, otherwise returns 0 (UnknownPeerID).
func (ids *mempoolIDs) Reclaim(peerID p2p.ID) uint16 {
	ids.mtx.Lock()
	defer ids.mtx.Unlock()

	removedID, ok := ids.peerMap[peerID]
	if ok {
		delete(ids.activeIDs, removedID)
		delete(ids.peerMap, peerID)
		return removedID
	}
	// Return 0 if the peer ID was not found in the map.
	return mempool.UnknownPeerID 
}

// GetIDForPeer returns the shorthand ID reserved for the peer.
// Returns 0 if the peer ID is not found.
func (ids *mempoolIDs) GetIDForPeer(peerID p2p.ID) uint16 {
	ids.mtx.RLock()
	defer ids.mtx.RUnlock()

	id, exists := ids.peerMap[peerID]
	if !exists {
		return mempool.UnknownPeerID
	}
	return id
}

// GetPeer returns the peer object associated with the given shorthand ID.
// Returns nil if the ID is not currently active.
func (ids *mempoolIDs) GetPeer(id uint16) p2p.Peer {
	ids.mtx.RLock()
	defer ids.mtx.RUnlock()

	// Note: Accessing a non-existent key in a map returns the zero value (nil for interface/pointer types).
	return ids.activeIDs[id]
}

// GetAll returns a copy of all active peers mapped by their short IDs.
func (ids *mempoolIDs) GetAll() map[uint16]p2p.Peer {
	ids.mtx.RLock()
	defer ids.mtx.RUnlock()

	// Mandated: return a copy to prevent external modification of the internal map.
	peers := make(map[uint16]p2p.Peer, len(ids.activeIDs))
	for id, peer := range ids.activeIDs {
		peers[id] = peer
	}
	return peers
}

// Len returns the number of active peers.
func (ids *mempoolIDs) Len() int {
	ids.mtx.RLock()
	defer ids.mtx.RUnlock()

	return len(ids.activeIDs)
}
