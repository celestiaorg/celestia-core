package propagation

import (
	"testing"
	"time"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/state"
)

func TestRequestFromPeer(t *testing.T) {
	t.Run("maximum concurrent request count - want not sent", func(t *testing.T) {
		reactors, prop, _, _ := createTestCreatorsWithProposal(t, 2, 1, 2, 1000000)
		reactor1 := reactors[0]
		reactor2 := reactors[1]

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)

		// restart the wants sending routine
		close(p2.receivedHaves)
		p2.receivedHaves = make(chan request, 3000)
		go reactor1.requestFromPeer(p2)

		// set the concurrent limit to the max
		p2.concurrentReqs.Store(perPeerConcurrentRequestLimit(len(reactor1.getPeers()), int(prop.BlockID.PartSetHeader.Total)*2))

		// send a have
		p2.receivedHaves <- request{
			height: prop.Height,
			round:  prop.Round,
			index:  uint32(0),
		}
		time.Sleep(200 * time.Millisecond)

		p1 := reactor2.getPeer(reactor1.self)
		require.NotNil(t, p1)
		wants, has := p1.GetWants(prop.Height, prop.Round)
		if has {
			assert.True(t, wants.IsEmpty())
		}
	})

	t.Run("maximum concurrent request count - want sent after receiving a part", func(t *testing.T) {
		reactors, prop, _, _ := createTestCreatorsWithProposal(t, 2, 1, 2, 1000000)
		reactor1 := reactors[0]
		reactor2 := reactors[1]

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)

		// restart the wants sending routine
		close(p2.receivedHaves)
		p2.receivedHaves = make(chan request, 3000)
		go reactor1.requestFromPeer(p2)

		p2.concurrentReqs.Store(perPeerConcurrentRequestLimit(len(reactor1.getPeers()), int(prop.BlockID.PartSetHeader.Total)*2))
		p2.receivedHaves <- request{
			height: prop.Height,
			round:  prop.Round,
			index:  0,
		}
		p2.receivedParts <- partData{height: prop.Height, round: prop.Round}
		time.Sleep(200 * time.Millisecond)

		p1 := reactor2.getPeer(reactor1.self)
		require.NotNil(t, p1)
		wants, has := p1.GetWants(prop.Height, prop.Round)
		require.True(t, has)
		assert.False(t, wants.IsEmpty())
		assert.True(t, wants.GetIndex(0))
	})

	t.Run("not requesting part - already requested from another peer", func(t *testing.T) {
		reactors, prop, _, _ := createTestCreatorsWithProposal(t, 3, 1, 2, 1000000)
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor3 := reactors[2]

		// set the request to be sent from another peer
		p3 := reactor1.getPeer(reactor3.self)
		require.NotNil(t, p3)
		p3.initialize(prop.Height, prop.Round, int(prop.BlockID.PartSetHeader.Total))
		reqs, has := p3.GetRequests(prop.Height, prop.Round)
		require.True(t, has)
		reqs.SetIndex(0, true)

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)

		p2.concurrentReqs.Store(0)
		p2.receivedHaves <- request{
			height: prop.Height,
			round:  prop.Round,
			index:  0,
		}
		time.Sleep(200 * time.Millisecond)

		p1 := reactor2.getPeer(reactor1.self)
		require.NotNil(t, p1)
		wants, has := p1.GetWants(prop.Height, prop.Round)
		if has {
			assert.False(t, wants.GetIndex(0))
		}
	})
	t.Run("part requested successfully + checking if haves were broadcasted", func(t *testing.T) {
		reactors, prop, _, _ := createTestCreatorsWithProposal(t, 3, 1, 2, 1000000)
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor3 := reactors[2]

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)
		p2.concurrentReqs.Store(0)
		p2.receivedHaves <- request{
			height: prop.Height,
			round:  prop.Round,
			index:  0,
		}
		p2.RequestsReady()

		time.Sleep(200 * time.Millisecond)

		p1 := reactor2.getPeer(reactor1.self)
		require.NotNil(t, p1)
		wants, has := p1.GetWants(prop.Height, prop.Round)
		require.True(t, has)
		assert.False(t, wants.IsEmpty())
		assert.True(t, wants.GetIndex(0))

		// check if haves have been received by the third reactor
		p1 = reactor3.getPeer(reactor1.self)
		require.NotNil(t, p1)
		haves, has := p1.GetHaves(prop.Height, prop.Round)
		require.True(t, has)
		assert.False(t, haves.IsEmpty())
		assert.True(t, haves.GetIndex(0))
	})
	t.Run("batch parts requested successfully + checking if haves were broadcasted", func(t *testing.T) {
		reactors, prop, _, _ := createTestCreatorsWithProposal(t, 3, 1, 2, 1000000)
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor3 := reactors[2]

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)
		p2.concurrentReqs.Store(0)

		// restart the wants sending routine
		close(p2.receivedHaves)
		p2.receivedHaves = make(chan request, 3000)
		go reactor1.requestFromPeer(p2)

		for i := 0; i < 10; i++ {
			p2.receivedHaves <- request{
				height: prop.Height,
				round:  prop.Round,
				index:  uint32(i),
			}
		}
		p2.RequestsReady()
		time.Sleep(200 * time.Millisecond)

		p1 := reactor2.getPeer(reactor1.self)
		require.NotNil(t, p1)
		wants, has := p1.GetWants(prop.Height, prop.Round)
		require.True(t, has)
		assert.False(t, wants.IsEmpty())
		for i := 0; i < 10; i++ {
			assert.True(t, wants.GetIndex(i), i)
		}

		// check if haves have been received by the third reactor
		p1 = reactor3.getPeer(reactor1.self)
		require.NotNil(t, p1)
		haves, has := p1.GetHaves(prop.Height, prop.Round)
		require.True(t, has)
		assert.False(t, haves.IsEmpty())
		for i := 0; i < 10; i++ {
			assert.True(t, haves.GetIndex(i), i)
		}
	})
}

func TestReqLimit(t *testing.T) {
	tests := []struct {
		name       string
		partsCount int
		expected   int
	}{
		{
			name:       "single part",
			partsCount: 1,
			expected:   34,
		},
		{
			name:       "multiple parts exact division",
			partsCount: 2,
			expected:   17,
		},
		{
			name:       "multiple parts rounding up",
			partsCount: 3,
			expected:   12,
		},
		{
			name:       "parts count exceeds 34",
			partsCount: 35,
			expected:   1,
		},
		{
			name:       "minimum allowed value",
			partsCount: 34,
			expected:   1,
		},
		{
			name:       "very large parts count",
			partsCount: 1000,
			expected:   1,
		},
		{
			name:       "zero parts count",
			partsCount: 0,
			expected:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReqLimit(tt.partsCount)
			if got != tt.expected {
				t.Errorf("ReqLimit(%d) = %d, want %d", tt.partsCount, got, tt.expected)
			}
		})
	}
}

func TestMultipleRequestsPerPart(t *testing.T) {
	reactors, prop, _, cb := createTestCreatorsWithProposal(t, 3, 1, 1, 10)
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	reactor1.handleHaves(reactor2.self, &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts: []proptypes.PartMetaData{{
			Index: 0,
			Hash:  cb.PartsHashes[0],
		}},
	})

	reactor1.handleHaves(reactor3.self, &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts: []proptypes.PartMetaData{{
			Index: 0,
			Hash:  cb.PartsHashes[0],
		}},
	})

	time.Sleep(200 * time.Millisecond)

	// verify that two wants has been sent
	p1 := reactor2.getPeer(reactor1.self)
	haves, has := p1.GetWants(prop.Height, prop.Round)
	require.True(t, has)
	assert.True(t, haves.GetIndex(0))

	p1 = reactor3.getPeer(reactor1.self)
	haves, has = p1.GetWants(prop.Height, prop.Round)
	require.True(t, has)
	assert.True(t, haves.GetIndex(0))
}

func createTestCreatorsWithProposal(t *testing.T, reactorsCount int, height int64, txCount, txSize int) (
	[]*Reactor,
	*types.Proposal,
	*types.PartSet,
	*proptypes.CompactBlock,
) {
	reactors, _ := createTestReactors(reactorsCount, cfg.DefaultP2PConfig(), false, "/tmp/test")

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(sm, height, txCount, txSize)
	cb, _ := createCompactBlock(t, prop, ps, metaData)

	for _, reactor := range reactors {
		added := reactor.AddProposal(cb)
		require.True(t, added)
	}
	return reactors, prop, ps, cb
}
