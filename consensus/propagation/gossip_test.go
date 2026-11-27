package propagation

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/state"
)

// compactBlockPeer is a mock peer that records the compact blocks sent to it
// and accepts every message.
type compactBlockPeer struct {
	*mock.Peer
	mtx  sync.Mutex
	sent []*propproto.CompactBlock
}

func (p *compactBlockPeer) TrySend(e p2p.Envelope) bool {
	if cb, ok := e.Message.(*propproto.CompactBlock); ok {
		p.mtx.Lock()
		p.sent = append(p.sent, cb)
		p.mtx.Unlock()
	}
	return true
}

func (p *compactBlockPeer) sentCompactBlocks() []*propproto.CompactBlock {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return append([]*propproto.CompactBlock(nil), p.sent...)
}

func addCompactBlockPeers(r *Reactor, n int) []*compactBlockPeer {
	peers := make([]*compactBlockPeer, n)
	for i := range peers {
		peers[i] = &compactBlockPeer{Peer: mock.NewPeer(net.IP{127, 0, 0, 1})}
		r.setPeer(peers[i].ID(), newPeerState(context.Background(), peers[i], log.NewNopLogger()))
	}
	return peers
}

// newGossipTestReactor returns a reactor with no switch peers that accepts
// compact blocks signed by mockPrivVal at the given height and round 0.
func newGossipTestReactor(t *testing.T, height int64) *Reactor {
	t.Helper()
	reactors, switches := testBlockPropReactors(1, defaultTestP2PConf())
	stopSwitchesOnCleanup(t, switches)
	r := reactors[0]
	r.SetConsensusState(height, 0, mockPubKey)
	return r
}

// TestCompactBlockIsGossipedOnceToEveryPeerExceptSender asserts that a new
// valid compact block is sent exactly once to every peer except the peer it
// came from.
func TestCompactBlockIsGossipedOnceToEveryPeerExceptSender(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	const height = int64(3)
	r := newGossipTestReactor(t, height)
	peers := addCompactBlockPeers(r, 5)
	sender := peers[0]

	cb, _, _, _ := testCompactBlock(t, sm, pv, height, 0)
	r.handleCompactBlock(cb, sender.ID(), false)
	// a duplicate from another peer is not gossiped again.
	r.handleCompactBlock(cb, peers[1].ID(), false)

	select {
	case prop := <-r.GetProposalChan():
		require.True(t, prop.Proposal.BlockID.Equals(cb.Proposal.BlockID))
	default:
		t.Fatal("compact block was not forwarded to consensus")
	}

	require.Zero(t, len(sender.sentCompactBlocks()), "compact block sent back to its sender")
	for i, p := range peers[1:] {
		sent := p.sentCompactBlocks()
		require.Len(t, sent, 1, "peer %d", i+1)
		got, err := proptypes.CompactBlockFromProto(sent[0])
		require.NoError(t, err)
		require.True(t, got.Proposal.BlockID.Equals(cb.Proposal.BlockID), "peer %d", i+1)
	}
}

// TestCompactBlockReplacedByCommitmentIsNotGossiped asserts that a compact
// block whose entry is replaced by a commitment for a different part set
// header, while the compact block waits to be forwarded to consensus, is not
// gossiped afterwards, and that the committed entry is kept.
func TestCompactBlockReplacedByCommitmentIsNotGossiped(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	const height = int64(3)
	r := newGossipTestReactor(t, height)
	peers := addCompactBlockPeers(r, 3)
	sender := peers[0]

	cbA, _, _, _ := testCompactBlock(t, sm, pv, height, 0)
	cbB, _, _, _ := testCompactBlock(t, sm, pv, height, 0)
	require.False(t, cbA.Proposal.BlockID.Equals(cbB.Proposal.BlockID))

	// fill proposalChan so that forwarding cbB to consensus waits.
	for len(r.proposalChan) < cap(r.proposalChan) {
		r.proposalChan <- ProposalAndSrc{}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.handleCompactBlock(cbB, sender.ID(), false)
	}()

	// once cbB's entry is claimed for forwarding, the receive path cannot get
	// past the send to the full proposalChan until it is drained below.
	require.Eventually(t, func() bool {
		r.pmtx.Lock()
		defer r.pmtx.Unlock()
		entry := r.proposals[height][0]
		return entry != nil && entry.forwarded
	}, 10*time.Second, time.Millisecond)

	// consensus commits A at the same height and round.
	pshA := cbA.Proposal.BlockID.PartSetHeader
	r.AddCommitment(height, 0, &pshA)

	// drain proposalChan to let the receive path finish.
	for drained := false; !drained; {
		select {
		case <-r.proposalChan:
		case <-done:
			drained = true
		}
	}

	for i, p := range peers {
		require.Zero(t, len(p.sentCompactBlocks()), "peer %d was sent the replaced compact block", i)
	}

	_, parts, _, has := r.getAllState(height, 0, true)
	require.True(t, has)
	require.True(t, parts.Original().Header().Equals(pshA))
}
