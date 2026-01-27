package cat

import (
	"sync"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
)

type upgradedPeers struct {
	mu    sync.Mutex
	peers map[uint16]struct{}
}

func newUpgradedPeers() *upgradedPeers {
	return &upgradedPeers{
		peers: make(map[uint16]struct{}),
	}
}

func (u *upgradedPeers) mark(peerID uint16) {
	if peerID == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.peers[peerID] = struct{}{}
}

func (u *upgradedPeers) remove(peerID uint16) {
	if peerID == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.peers, peerID)
}

func (u *upgradedPeers) has(peerID uint16) bool {
	if peerID == 0 {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.peers[peerID]
	return ok
}

// IsUpgradedMessage checks if the peer is upgraded
// Min and Max sequence should be set
// TxKey should be empty
func (u *upgradedPeers) IsUpgradedMessage(msg interface{}) bool {
	switch msg := msg.(type) {
	case *protomem.SeenTx:
		hasMinMaxSeq := msg.MinSequence > 0 && msg.MaxSequence > 0
		hasTxKey := len(msg.TxKey) > 0
		// if peer has min and max sequence and no tx key, then peer is upgraded
		return hasMinMaxSeq && !hasTxKey
	case *protomem.WantTx:
		hasTxKey := len(msg.TxKey) > 0
		hasSignerSeq := len(msg.Signer) > 0 && msg.Sequence > 0
		return !hasTxKey && hasSignerSeq
	case *protomem.Txs:
		// if it has signers and sequences set then peer is upgraded
		if len(msg.Signers) > 0 && len(msg.Sequences) > 0 {
			return true
		}
		return false

	default:
		return false
	}

}

// peerUpgradeStatus checks if the peer is being tracked as upgraded
// otherwise it checks if the message is an upgraded message
func (u *upgradedPeers) peerUpgradeStatus(peerID uint16, msg interface{}) bool {
	if u.has(peerID) {
		return true
	}
	isUpgraded := u.IsUpgradedMessage(msg)
	if isUpgraded {
		u.mark(peerID)
	}
	return isUpgraded
}
