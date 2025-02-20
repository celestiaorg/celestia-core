package propagation

import (
	"fmt"
	"reflect"

	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
)

const (
	// TODO: set a valid max msg size
	maxMsgSize = 1048576

	// DataChannel the propagation reactor channel handling the haves, the compact block,
	// and the recovery parts.
	DataChannel = byte(0x50)

	// WantChannel the propagation reactor channel handling the wants.
	WantChannel = byte(0x51)
)

type Reactor struct {
	p2p.BaseReactor // BaseService + p2p.Switch

	peerstate map[p2p.ID]*PeerState

	// ProposalCache temporarily stores recently active proposals and their
	// block data for gossiping.
	*ProposalCache

	mtx         *sync.RWMutex
	traceClient trace.Tracer
	self        p2p.ID
}

func NewReactor(self p2p.ID, tracer trace.Tracer, store *store.BlockStore, options ...ReactorOption) *Reactor {
	reactor := &Reactor{
		self:          self,
		traceClient:   tracer,
		peerstate:     make(map[p2p.ID]*PeerState),
		mtx:           &sync.RWMutex{},
		ProposalCache: NewProposalCache(store),
	}
	reactor.BaseReactor = *p2p.NewBaseReactor("BlockProp", reactor)

	for _, option := range options {
		option(reactor)
	}
	return reactor
}

type ReactorOption func(*Reactor)

func (blockProp *Reactor) OnStart() error {
	// TODO: implement
	return nil
}

func (blockProp *Reactor) OnStop() {
	// TODO: implement
}

func GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			// TODO: set better values
			ID:                  WantChannel,
			Priority:            6,
			SendQueueCapacity:   100,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propproto.Message{},
		},
		{
			// TODO: set better values
			ID:                  DataChannel,
			Priority:            10,
			SendQueueCapacity:   1000,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &propproto.Message{},
		},
	}
}

func (blockProp *Reactor) AddPeer(peer p2p.Peer) {
	// TODO: implement
}

func (blockProp *Reactor) ReceiveEnvelope(e p2p.Envelope) {
	if !blockProp.IsRunning() {
		blockProp.Logger.Debug("Receive", "src", e.Src, "chId", e.ChannelID)
		return
	}

	m := e.Message
	if wm, ok := m.(p2p.Wrapper); ok {
		m = wm.Wrap()
	}
	msg, err := proptypes.MsgFromProto(m.(*propproto.Message))
	if err != nil {
		blockProp.Logger.Error("Error decoding message", "src", e.Src, "chId", e.ChannelID, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err)
		return
	}

	if err = msg.ValidateBasic(); err != nil {
		blockProp.Logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		blockProp.Switch.StopPeerForError(e.Src, err)
		return
	}
	switch e.ChannelID {
	case DataChannel:
		switch msg := msg.(type) {
		case *proptypes.HaveParts:
			// TODO check if we need to bypass request limits
			blockProp.handleHaves(e.Src.ID(), msg, false)
		case *proptypes.RecoveryPart:
			blockProp.handleRecoveryPart(e.Src.ID(), msg)
		default:
			blockProp.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
		}
	case WantChannel:
		switch msg := msg.(type) {
		case *proptypes.WantParts:
			blockProp.handleWants(e.Src.ID(), msg)
		}
	default:
		blockProp.Logger.Error(fmt.Sprintf("Unknown chId %X", e.ChannelID))
	}
}

func (blockProp *Reactor) Receive(e p2p.Envelope) {
	blockProp.ReceiveEnvelope(e)
}

// getPeer returns the peer state for the given peer. If the peer does not exist,
// nil is returned.
//
//nolint:unused
func (blockProp *Reactor) getPeer(peer p2p.ID) *PeerState {
	blockProp.mtx.RLock()
	defer blockProp.mtx.RUnlock()
	return blockProp.peerstate[peer]
}

// getPeers returns a list of all peers that the data routine is aware of.
//
//nolint:unused
func (blockProp *Reactor) getPeers() []*PeerState {
	blockProp.mtx.RLock()
	defer blockProp.mtx.RUnlock()
	peers := make([]*PeerState, 0, len(blockProp.peerstate))
	for _, peer := range blockProp.peerstate {
		peers = append(peers, peer)
	}
	return peers
}

// setPeer sets the peer state for the given peer.
//
//nolint:unused
func (blockProp *Reactor) setPeer(peer p2p.ID, state *PeerState) {
	blockProp.mtx.Lock()
	defer blockProp.mtx.Unlock()
	blockProp.peerstate[peer] = state
}

// ProposeBlock is called when the consensus routine has created a new proposal
// and it needs to be gossiped to the rest of the network.
func (blockProp *Reactor) ProposeBlock(proposal *types.Proposal, haves *bits.BitArray) {
	blockProp.HandleProposal(proposal, blockProp.self, haves)
}

// HandleProposal adds a proposal to the data routine. This should be called any
// time a proposal is received from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
// This function will:
// - check if the from peer is provided. If not, set it to self.
// - add the proposal to the reactor's state.
// - if adding the proposal to the state is successful, broadcast the proposal to the peers.
// Note: this method will not propagate the haves after the proposal and the compact block is propagated.
// Check broadcastSelfProposalHaves for that.
func (blockProp *Reactor) HandleProposal(proposal *types.Proposal, from p2p.ID, haves *bits.BitArray) {
}

// broadcastProposal gossips the provided proposal to all peers. This should
// only be called upon receiving a proposal for the first time or after creating
// a proposal block.
//
//nolint:unused
func (blockProp *Reactor) broadcastProposal(proposal *types.Proposal, from p2p.ID) {
}

// broadcastSelfProposalHaves broadcasts the haves to all the connected peers when we're the proposers.
// Note: the haves are chunked so that every peer only receives a portion of the haves.
//
//nolint:unused
func (blockProp *Reactor) broadcastSelfProposalHaves(proposal *types.Proposal, from p2p.ID, haves *bits.BitArray) {
}

// chunkParts takes a bit array then returns an array of chunked bit arrays.
// TODO document how the redundancy and the peer count are used here.
func chunkParts(p *bits.BitArray, peerCount, redundancy int) []*bits.BitArray {
	size := p.Size()
	if peerCount == 0 {
		peerCount = 1
	}
	chunkSize := size / peerCount
	// round up to use the ceil
	if size%peerCount != 0 || chunkSize == 0 {
		chunkSize++
	}

	// Create empty bit arrays for each peer
	parts := make([]*bits.BitArray, peerCount)
	for i := 0; i < peerCount; i++ {
		parts[i] = bits.NewBitArray(size)
	}

	chunks := chunkIndexes(size, chunkSize)
	cursor := 0
	for p := 0; p < peerCount; p++ {
		for r := 0; r < redundancy; r++ {
			start, end := chunks[cursor][0], chunks[cursor][1]
			for i := start; i < end; i++ {
				parts[p].SetIndex(i, true)
			}
			cursor++
			if cursor >= len(chunks) {
				cursor = 0
			}
		}
	}

	return parts
}

// chunkIndexes
// TODO document and explain the parameters
func chunkIndexes(totalSize, chunkSize int) [][2]int {
	if totalSize <= 0 || chunkSize <= 0 {
		panic(fmt.Sprintf("invalid input: totalSize=%d, chunkSize=%d \n", totalSize, chunkSize))
		// return nil // Handle invalid input gracefully
	}

	var chunks [][2]int
	for start := 0; start < totalSize; start += chunkSize {
		end := start + chunkSize
		if end > totalSize {
			end = totalSize // Ensure the last chunk doesn't exceed the total size
		}
		chunks = append(chunks, [2]int{start, end})
	}

	return chunks
}

// handleHaves is called when a peer sends a have message. This is used to
// determine if the sender has or is getting portions of the proposal that this
// node doesn't have. If the sender has parts that this node doesn't have, this
// node will request those parts.
// The peer must always send the proposal before sending parts. If they did
// not, this node must disconnect from them.
// This method will:
// - get the provided peer from the peer state
// - get the proposal referenced in the haves message
// - set the provided haves as the peer's haves
// - if the returned parts from the proposal are complete, we return
// - otherwise, we check if the sender has parts that we don't have.
// - if they do, we check if we already requested those parts enough times (a limit will be defined)
// of we already requested the parts from them.
// - if so, we just gossip the haves to our connected peers.
// - otherwise, we send the wants for the missing parts to that peer before broadcasting the haves.
// - finally, we keep track of the want requests in the proposal state.
func (blockProp *Reactor) handleHaves(peer p2p.ID, haves *proptypes.HaveParts, bypassRequestLimit bool) {
}

// broadcastHaves gossips the provided have msg to all peers except to the
// original sender. This should only be called upon receiving a new have for the
// first time.
//
//nolint:unused
func (blockProp *Reactor) broadcastHaves(haves *proptypes.HaveParts, from p2p.ID) {
}

// handleWants is called when a peer sends a want message. This is used to send
// peers data that this node already has and store the wants to send them data
// in the future.
// This method will:
// - get the provided peer from the state
// - get the proposal from the proposal cache
// - if the round provided in the wants is < 0, send the peer the partset header
// - if we have the wanted parts, send them to that peer.
// - if they want other parts that we don't have, store that in the peer state.
func (blockProp *Reactor) handleWants(peer p2p.ID, wants *proptypes.WantParts) {
}

// sendPsh sends the partset header to the provided peer.
// This method will:
// - get the proposal with the provided height and round
// - get the patset header from the block meta?
// - send the partset header to the provided peer
// TODO rename this Psh to something less Psh
//
//nolint:unused
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

// bitArrayToParts a hack to get a list of have parts from a bit array
// TODO: remove when we have verification
//
//nolint:unused
func bitArrayToParts(array *bits.BitArray) []proptypes.PartMetaData {
	parts := make([]proptypes.PartMetaData, len(array.GetTrueIndices()))
	for i, index := range array.GetTrueIndices() {
		parts[i] = proptypes.PartMetaData{Index: uint32(index)}
	}
	return parts
}

// bitArrayToParts hack to get a list of have parts from a bit array
// TODO: remove when we have verification
//
//nolint:unused
func bitArrayToProtoParts(array *bits.BitArray) []*propproto.PartMetaData {
	parts := make([]*propproto.PartMetaData, len(array.GetTrueIndices()))
	for i, index := range array.GetTrueIndices() {
		parts[i] = &propproto.PartMetaData{Index: uint32(index)}
	}
	return parts
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

// handleRecoveryPart is called when a peer sends a block part message. This is used
// to store the part and clear any wants for that part.
// This method will:
// - if the peer is not provided, we set it to self
// - get the peer from the peer state
// - get the proposal referenced by the recovery part height and round
// - if the parts are complete, return
// - add the received part to the parts
// - if the parts are decodable, clear all the wants of that block from the proposal state
// - otherwise, clear the want related to this part from the state
func (blockProp *Reactor) handleRecoveryPart(peer p2p.ID, part *proptypes.RecoveryPart) {
}

// clearWants checks the wantState to see if any peers want the given part, if
// so, it attempts to send them that part.
// This method will:
// - get all the peers
// - check if any of the peers need that part
// - if so, send it to them
// - if not, remove that want.
//
//nolint:unused
func (blockProp *Reactor) clearWants(part *proptypes.RecoveryPart) {
}
