package consensus

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	cmtcons "github.com/tendermint/tendermint/proto/tendermint/consensus"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

func TestDataRoutine_basic(t *testing.T) {
	N := 2
	css, cleanup := randConsensusNet(N, "consensus_reactor_test", newMockTickerFunc(true), newCounter)
	defer cleanup()
	reactors, blocksSubs, eventBuses := startConsensusNet(t, css, N)
	defer stopConsensusNet(log.TestingLogger(), reactors, eventBuses)

	// wait till everyone makes the first new block
	timeoutWaitGroup(t, N, func(j int) {
		count := 0

		for _ = range blocksSubs[j].Out() {
			count++
			if count == 3 {
				break
			}
		}
	}, css)

	fmt.Println("yeah")

	for _, reactor := range reactors {
		fmt.Println("reactor", reactor.dr.self, "-------------------------------")
		fmt.Println("peer state", reactor.dr.peerstate)
		fmt.Println("proposals", reactor.dr.proposals)
	}
}

func TestDataRoutine_proposal(t *testing.T) {
	reactors := testDRs(t, 2)
	a, b := reactors[0], reactors[1]

	// create a proposal and start gossiping it, but don't gossip the parts yet.
	prop, parts := newTestProp(1, 0, 2)
	a.dr.handleProposal(prop, a.dr.self, false)

	time.Sleep(400 * time.Millisecond)

	// the proposal should be in both reactors
	require.Equal(t, 1, len(a.dr.proposals))
	require.Equal(t, 1, len(b.dr.proposals))

	// haves should be set in both reactors
	require.Equal(t, 1, len(a.dr.peerstate))
	require.Equal(t, 1, len(b.dr.peerstate))

	// a should be marked as having the proposal
	haves, has := b.dr.peerstate[a.dr.self].GetHaves(1, 0)
	require.True(t, has)
	require.True(t, haves.GetIndex(0))

	// b should be marked as wanting the proposal
	wants, has := a.dr.peerstate[b.dr.self].GetWants(1, 0)
	require.True(t, has)
	require.True(t, wants.GetIndex(0))

	// set one of the block parts a and see if it gets gossiped to b
	a.dr.handleBlockPart(a.dr.self, &BlockPartMessage{
		Height: 1,
		Round:  0,
		Part:   parts.GetPart(0),
	})

	time.Sleep(400 * time.Millisecond)

	// b should have requested and downloaded the part by now
	reqs, ok := b.dr.peerstate[a.dr.self].GetRequests(1, 0)
	require.True(t, ok)
	require.NotNil(t, reqs.GetIndex(0))

	_, bparts, _, has := b.dr.GetProposal(1, 0)
	require.True(t, has)
	require.NotNil(t, bparts.GetPart(0))

	// b should be marked as having the block parts
	wants, has = a.dr.peerstate[b.dr.self].GetWants(1, 0)
	require.True(t, has)
	require.False(t, wants.GetIndex(0))
	require.True(t, wants.GetIndex(1))
}

func TestDebug(t *testing.T) {
	c := bits.NewBitArray(1)
	fmt.Println(c.IsEmpty(), c)
}

func newTestProp(height int64, round int32, parts int) (*types.Proposal, *types.PartSet) {
	// a sends proposal to b

	// create random block data
	d := cmtrand.Bytes((int(types.BlockPartSizeBytes) * parts) - 100) // sub 100 to account for encoding overhead
	ps := types.NewPartSetFromData(d, types.BlockPartSizeBytes)
	psh := ps.Header()

	if psh.Total != uint32(parts) {
		panic("invalid parts len")
	}

	bi := types.BlockID{
		Hash:          []byte("add_more_exclamation_marks_code-"),
		PartSetHeader: psh,
	}

	ba := bits.NewBitArray(parts)
	ba.Fill()

	return &types.Proposal{
		Type:      cmtproto.ProposalType,
		Height:    height,
		Round:     round,
		POLRound:  -1,
		BlockID:   bi,
		Timestamp: time.Now(),
		Signature: cmtrand.Bytes(20),
		HaveParts: ba,
	}, ps
}

var _ p2p.Reactor = &dataReactor{}

type dataReactor struct {
	p2p.BaseReactor
	dr *DataRoutine
}

func newDataReactor(s *p2p.Switch) *dataReactor {
	store := store.NewBlockStore(dbm.NewMemDB())
	dr := NewDataRoutine(log.TestingLogger(), trace.NoOpTracer(), store, s, s.NetAddress().ID)
	drr := &dataReactor{
		dr: dr,
	}
	br := p2p.NewBaseReactor("DATA", drr)
	br.SetSwitch(s)
	drr.BaseReactor = *br
	return drr
}

func testDRs(t *testing.T, n int) []*dataReactor {
	drs := make([]*dataReactor, n)

	p2pCfg := cfg.DefaultP2PConfig()

	p2p.MakeConnectedSwitches(p2pCfg, n, func(i int, s *p2p.Switch) *p2p.Switch {
		drs[i] = newDataReactor(s)
		s.AddReactor("DATA", drs[i])
		return s
	},
		p2p.Connect2Switches,
	)

	return drs
}

func (dr *dataReactor) InitPeer(peer p2p.Peer) p2p.Peer {
	return peer
}

func (dr *dataReactor) IsRunning() bool {
	if dr.dr == nil {
		return false
	}
	return true
}

func (dr *dataReactor) GetChannels() []*p2p.ChannelDescriptor {
	// TODO optimize
	return []*p2p.ChannelDescriptor{
		{
			ID:                  StateChannel,
			Priority:            6,
			SendQueueCapacity:   100,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &cmtcons.Message{},
		},
		{
			ID: DataChannel, // maybe split between gossiping current block and catchup stuff
			// once we gossip the whole block there's nothing left to send until next height or round
			Priority:            10,
			SendQueueCapacity:   100,
			RecvBufferCapacity:  50 * 4096,
			RecvMessageCapacity: maxMsgSize,
			MessageType:         &cmtcons.Message{},
		},
	}
}

func (dr *dataReactor) AddPeer(peer p2p.Peer) {
	dr.dr.AddPeer(peer)
}

func (dr *dataReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	dr.dr.RemovePeer(peer)
}

func (dr *dataReactor) Receive(chID byte, peer p2p.Peer, msgBytes []byte) {
	msg := &cmtcons.Message{}
	err := proto.Unmarshal(msgBytes, msg)
	if err != nil {
		panic(err)
	}
	uw, err := msg.Unwrap()
	if err != nil {
		panic(err)
	}
	dr.ReceiveEnvelope(p2p.Envelope{
		ChannelID: chID,
		Src:       peer,
		Message:   uw,
	})
}

func (dr *dataReactor) ReceiveEnvelope(e p2p.Envelope) {
	m := e.Message
	if wm, ok := m.(p2p.Wrapper); ok {
		m = wm.Wrap()
	}
	msg, err := MsgFromProto(m.(*cmtcons.Message))
	if err != nil {
		dr.dr.logger.Error("Error decoding message", "src", e.Src, "chId", e.ChannelID, "err", err)
		return
	}

	if err = msg.ValidateBasic(); err != nil {
		dr.dr.logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		panic(err)
	}

	switch e.ChannelID {
	case StateChannel:
		switch msg := msg.(type) {
		case *NewValidBlockMessage:
			schema.WriteConsensusState(
				dr.dr.tracer,
				msg.Height,
				msg.Round,
				string(e.Src.ID()),
				schema.ConsensusNewValidBlock,
				schema.Download,
			)
			dr.dr.handleValidBlock(e.Src.ID(), msg.Height, msg.Round, msg.BlockPartSetHeader, false)
		default:
			dr.dr.logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
		}

	case DataChannel:
		switch msg := msg.(type) {
		case *ProposalMessage:
			schema.WriteProposal(
				dr.dr.tracer,
				msg.Proposal.Height,
				msg.Proposal.Round,
				string(e.Src.ID()),
				schema.Download,
			)
			dr.dr.handleProposal(msg.Proposal, e.Src.ID(), false)

		case *ProposalPOLMessage:
			schema.WriteConsensusState(
				dr.dr.tracer,
				msg.Height,
				msg.ProposalPOLRound,
				string(e.Src.ID()),
				schema.ConsensusPOL,
				schema.Download,
			)

		case *BlockPartMessage:
			schema.WriteBlockPart(dr.dr.tracer, msg.Height, msg.Round, msg.Part.Index, false, string(e.Src.ID()), schema.Download)
			dr.dr.handleBlockPart(e.Src.ID(), msg)

		case *PartStateMessage:
			go dr.dr.handlePartState(e.Src.ID(), msg.PartState)
			schema.WriteBlockPartState(
				dr.dr.tracer,
				msg.PartState.Height,
				msg.PartState.Round,
				msg.PartState.Parts.GetTrueIndices(),
				msg.PartState.Have,
				string(e.Src.ID()),
				schema.Download,
			)

		case *NewValidBlockMessage:
			// todo(evan): probably don't reuse a message here, and just add a new one to send the psh.
			dr.dr.handleValidBlock(e.Src.ID(), msg.Height, msg.Round, msg.BlockPartSetHeader, true)

		default:
			dr.dr.logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
		}
	}
}

func TestChunkParts(t *testing.T) {
	tests := []struct {
		name       string
		bitArray   *bits.BitArray
		peerCount  int
		redundancy int
		expected   []*bits.BitArray
	}{
		{
			name:       "Basic case with redundancy",
			bitArray:   bits.NewBitArray(6),
			peerCount:  3,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(6, []int{0, 1, 2, 3}),
				createBitArray(6, []int{0, 1, 4, 5}),
				createBitArray(6, []int{2, 3, 4, 5}),
			},
		},
		{
			name:       "No redundancy",
			bitArray:   bits.NewBitArray(6),
			peerCount:  3,
			redundancy: 1,
			expected: []*bits.BitArray{
				createBitArray(6, []int{0, 1}),
				createBitArray(6, []int{2, 3}),
				createBitArray(6, []int{4, 5}),
			},
		},
		{
			name:       "Full overlap",
			bitArray:   bits.NewBitArray(4),
			peerCount:  2,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
		{
			name:       "uneven",
			bitArray:   bits.NewBitArray(4),
			peerCount:  3,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
		{
			name:       "uneven",
			bitArray:   bits.NewBitArray(4),
			peerCount:  9,
			redundancy: 1,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ba := tc.bitArray
			ba.Fill()
			result := chunkParts(ba, tc.peerCount, tc.redundancy)
			require.Equal(t, len(tc.expected), len(result))

			for i, exp := range tc.expected {
				require.Equal(t, exp.String(), result[i].String(), i)
			}
		})
	}
}

func createBitArray(size int, indices []int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, index := range indices {
		ba.SetIndex(index, true)
	}
	return ba
}
