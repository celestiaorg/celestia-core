package behavior

import (
	"github.com/tendermint/tendermint/p2p"
)

// Peerbehavior is a struct describing a behavior a peer performed.
// `peerID` identifies the peer and reason characterizes the specific
// behavior performed by the peer.
type Peerbehavior struct {
	peerID p2p.ID
	reason interface{}
}

type badMessage struct {
	explanation string
}

// BadMessage returns a badMessage Peerbehavior.
func BadMessage(peerID p2p.ID, explanation string) Peerbehavior {
	return Peerbehavior{peerID: peerID, reason: badMessage{explanation}}
}

type messageOutOfOrder struct {
	explanation string
}

// MessageOutOfOrder returns a messagOutOfOrder Peerbehavior.
func MessageOutOfOrder(peerID p2p.ID, explanation string) Peerbehavior {
	return Peerbehavior{peerID: peerID, reason: messageOutOfOrder{explanation}}
}

type consensusVote struct {
	explanation string
}

// ConsensusVote returns a consensusVote Peerbehavior.
func ConsensusVote(peerID p2p.ID, explanation string) Peerbehavior {
	return Peerbehavior{peerID: peerID, reason: consensusVote{explanation}}
}

type blockPart struct {
	explanation string
}

// BlockPart returns blockPart Peerbehavior.
func BlockPart(peerID p2p.ID, explanation string) Peerbehavior {
	return Peerbehavior{peerID: peerID, reason: blockPart{explanation}}
}
