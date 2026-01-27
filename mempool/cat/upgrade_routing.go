package cat

import (
	"fmt"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

type txRoute struct {
	peerID   uint16
	txKey    types.TxKey
	signer   []byte
	sequence uint64
}

func (rt txRoute) hasSeq() bool {
	return len(rt.signer) > 0 && rt.sequence > 0
}

func (rt txRoute) hasKey() bool {
	return rt.txKey != (types.TxKey{})
}

func (memR *Reactor) peerIsUpgraded(peerID uint16) bool {
	return memR.upgradedPeers.has(peerID)
}

func (memR *Reactor) peerEligibleForRoute(rt txRoute) bool {
	if memR.requests.CountForPeer(rt.peerID) >= maxRequestsPerPeer {
		return false
	}
	isUpgraded := memR.peerIsUpgraded(rt.peerID)
	if memR.requests.HasRoute(isUpgraded, rt.peerID, rt.txKey, rt.signer, rt.sequence) {
		return false
	}
	return true
}

func (memR *Reactor) requestByRoute(rt txRoute) bool {
	peer := memR.ids.GetPeer(rt.peerID)
	if peer == nil {
		return false
	}
	if memR.peerIsUpgraded(rt.peerID) && rt.hasSeq() {
		return memR.requestTxBySequence(rt.signer, rt.sequence, peer)
	}
	if rt.hasKey() {
		return memR.requestTxWithTxKey(rt.txKey, peer)
	}
	return false
}

// prepareTxsMetadata validates tx metadata from upgraded peers and returns it.
func (memR *Reactor) prepareTxsMetadata(msg *protomem.Txs, txs [][]byte, src p2p.Peer) (bool, [][]byte, []uint64, bool) {
	peerID := memR.ids.GetIDForPeer(src.ID())
	isUpgraded := memR.upgradedPeers.peerUpgradeStatus(peerID, msg)
	if !isUpgraded {
		return false, nil, nil, true
	}

	memR.Logger.Info("received txs from upgraded peer", "src", src)

	signers := msg.GetSigners()
	sequences := msg.GetSequences()
	if len(signers) != len(txs) || len(sequences) != len(txs) {
		memR.Logger.Error("received txs from upgraded peer with incorrect metadata", "src", src)
		memR.Switch.StopPeerForError(src, fmt.Errorf("received txs from upgraded peer with incorrect metadata"), memR.String())
		return false, nil, nil, false
	}

	return true, signers, sequences, true
}

// resolveTxSignerSequence resolves the signer and sequence for a given tx.
func (memR *Reactor) resolveTxSignerSequence(
	isUpgraded bool,
	idx int,
	key types.TxKey,
	signers [][]byte,
	sequences []uint64,
	src p2p.Peer,
) ([]byte, uint64, bool) {
	if !isUpgraded {
		if entry := memR.legacyTxKeyTracker.getByTxKey(key); entry != nil {
			return entry.signer, entry.sequence, true
		}
		return nil, 0, false
	}

	signer := signers[idx]
	sequence := sequences[idx]
	if len(signer) > maxSignerLength {
		memR.Logger.Error("peer sent txs with signer too long", "len", len(signer))
		memR.Switch.StopPeerForError(src, errSignerTooLong, memR.String())
		return nil, 0, false
	}
	return signer, sequence, true
}

// resolveTxKey resolves the tx key for txKey-based peers.
func (memR *Reactor) resolveTxKey(isUpgraded bool, txKeyBytes []byte, src p2p.Peer) types.TxKey {
	if isUpgraded {
		return types.TxKey{}
	}
	if len(txKeyBytes) == 0 {
		memR.Logger.Error("peer sent legacy tx key", "peer", src.ID())
		memR.Switch.StopPeerForError(src, fmt.Errorf("missing tx key"), memR.String())
		return types.TxKey{}
	}
	txKey, err := types.TxKeyFromBytes(txKeyBytes)
	if err != nil {
		memR.Logger.Error("peer sent legacy incorrect tx key", "err", err)
		memR.Switch.StopPeerForError(src, err, memR.String())
		return types.TxKey{}
	}
	return txKey
}

func (memR *Reactor) recordSeenTx(msg *protomem.SeenTx, txKey types.TxKey, peerID uint16) {
	if memR.upgradedPeers.has(peerID) {
		memR.sequenceTracker.recordSeenTx(msg, peerID)
	} else {
		memR.legacyTxKeyTracker.recordSeenTx(msg, txKey, peerID)
	}
}

func (memR *Reactor) requestTx(txKey types.TxKey, signer []byte, sequence uint64, peer p2p.Peer) {
	peerID := memR.ids.GetIDForPeer(peer.ID())
	if !memR.upgradedPeers.has(peerID) {
		memR.requestTxWithTxKey(txKey, peer)
	} else {
		memR.requestTxBySequence(signer, sequence, peer)
	}
}

func (memR *Reactor) resolveWantTx(isUpgraded bool, msg *protomem.WantTx, txKey types.TxKey) (*types.CachedTx, bool) {
	if isUpgraded {
		wtx, has := memR.mempool.store.getTxBySignerSequence(msg.Signer, msg.Sequence)
		if !has {
			memR.Logger.Debug("peer requested tx by sequence but we don't have it")
			return nil, false
		}
		memR.Logger.Trace(
			"resolved WantTx by sequence",
			"signer", string(msg.Signer),
			"sequence", msg.Sequence,
			"txKey", wtx.key().String(),
		)
		return wtx.tx, true
	}

	wtx := memR.mempool.store.get(txKey)
	if wtx == nil {
		return nil, false
	}

	return wtx.tx, true
}
