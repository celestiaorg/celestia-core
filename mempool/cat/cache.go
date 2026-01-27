package cat

import (
	"time"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// SeenTxSet records transactions that have been
// seen by other peers but not yet by us
type SeenTxSet struct {
	mtx               tmsync.Mutex
	set               map[types.TxKey]timestampedPeerSet
	signerSequenceSet map[string]timestampedPeerSet
}

type timestampedPeerSet struct {
	peers map[uint16]struct{}
	time  time.Time
}

func NewSeenTxSet() *SeenTxSet {
	return &SeenTxSet{
		set:               make(map[types.TxKey]timestampedPeerSet),
		signerSequenceSet: make(map[string]timestampedPeerSet),
	}
}

func (s *SeenTxSet) Add(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		s.set[txKey] = timestampedPeerSet{
			peers: map[uint16]struct{}{peer: {}},
			time:  time.Now().UTC(),
		}
	} else {
		seenSet.peers[peer] = struct{}{}
	}
}

func (s *SeenTxSet) AddWithSignerSequence(signer []byte, sequence uint64, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	signerSequenceKey := signerSequenceKey(signer, sequence)
	seenSet, exists := s.signerSequenceSet[signerSequenceKey]
	// if the signer sequence is not in the set, add it
	if !exists {
		s.signerSequenceSet[signerSequenceKey] = timestampedPeerSet{
			peers: map[uint16]struct{}{peer: {}},
			time:  time.Now().UTC(),
		}
		// if the signer sequence is in the set, just add the peer
	} else {
		seenSet.peers[peer] = struct{}{}
	}
}
func (s *SeenTxSet) RemoveKey(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.set, txKey)
}

func (s *SeenTxSet) RemoveSignerSequence(signer []byte, sequence uint64) {
	if len(signer) == 0 || sequence == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.signerSequenceSet, signerSequenceKey(signer, sequence))
}

func (s *SeenTxSet) Remove(txKey types.TxKey, peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	set, exists := s.set[txKey]
	if exists {
		if len(set.peers) == 1 {
			delete(s.set, txKey)
		} else {
			delete(set.peers, peer)
		}
	}
}

func (s *SeenTxSet) RemovePeer(peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		delete(seenSet.peers, peer)
		if len(seenSet.peers) == 0 {
			delete(s.set, key)
		}
	}
	for key, seenSet := range s.signerSequenceSet {
		delete(seenSet.peers, peer)
		if len(seenSet.peers) == 0 {
			delete(s.signerSequenceSet, key)
		}
	}
}

func (s *SeenTxSet) Prune(limit time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		if seenSet.time.Before(limit) {
			delete(s.set, key)
		}
	}
	for key, seenSet := range s.signerSequenceSet {
		if seenSet.time.Before(limit) {
			delete(s.signerSequenceSet, key)
		}
	}
}

func (s *SeenTxSet) Has(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return false
	}
	_, has := seenSet.peers[peer]
	return has
}

func (s *SeenTxSet) Get(txKey types.TxKey) map[uint16]struct{} {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return nil
	}
	// make a copy of the struct to avoid concurrency issues
	peers := make(map[uint16]struct{}, len(seenSet.peers))
	for peer := range seenSet.peers {
		peers[peer] = struct{}{}
	}
	return peers
}

// Len returns the amount of cached items. Mostly used for testing.
func (s *SeenTxSet) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.set) + len(s.signerSequenceSet) // TODO: separate these out 
}

func (s *SeenTxSet) Reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.set = make(map[types.TxKey]timestampedPeerSet)
	s.signerSequenceSet = make(map[string]timestampedPeerSet)
}
