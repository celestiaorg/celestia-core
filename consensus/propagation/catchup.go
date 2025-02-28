package propagation

import (
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/types"
)

// sendPsh sends the partset header to the provided peer.
// This method will:
// - get the proposal with the provided height and round
// - get the patset header from the block meta?
// - send the partset header to the provided peer
// TODO rename this Psh to something less Psh
func (blockProp *Reactor) sendPsh(peer p2p.ID, height int64, round int32) bool {
	return true
}

// HandleValidBlock is called when the node finds a peer with a valid block. If this
// node doesn't have a block, it asks the sender for the portions that it
// doesn't have.
// This method will:
// - get the provided peer from the peer state
// - get the proposal referenced by the provided height and round
// - if it has the proposal:
//   - if the proposal is complete return
//   - otherwise, create a new bit array with the size of the partset header total and fill it with true indices
//   - broadcast the haves of that block
//
// - send the wants of all the block parts to the peer that sent it to us
// - set the requests
// - request all the previous blocks if any are missing
func (blockProp *Reactor) HandleValidBlock(peer p2p.ID, height int64, round int32, psh types.PartSetHeader, exitEarly bool) {
}

// requestAllPreviousBlocks is called when a node is catching up and needs to
// request all previous blocks from a peer.
// What this method will do:
// - get the peer from state
// - get the reactor latest height
// - send want parts for all the necessary blocks between [reactor.latestHeight, height)
// while setting the want's round to a value < 0.
//
//nolint:unused
func (blockProp *Reactor) requestAllPreviousBlocks(peer p2p.ID, height int64) {
}
