package propagation

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/state"
	"testing"
	"time"
)

func TestWantsSendingRoutine2(t *testing.T) {
	t.Run("maximum concurrent request count - want not sent", func(t *testing.T) {
		reactors, _ := createTestReactors(2, cfg.DefaultP2PConfig(), false, "/tmp/test/wants_sending_routine")
		reactor1 := reactors[0]
		reactor2 := reactors[1]

		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
		cb, _ := createCompactBlock(t, prop, ps, metaData)

		for _, reactor := range reactors {
			added := reactor.AddProposal(cb)
			require.True(t, added)
		}

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)

		// restart the wants sending routine
		close(p2.requestChan)
		p2.requestChan = make(chan request, 3000)
		go reactor1.wantsSendingRoutine(p2)

		// set the concurrent limit to the max
		p2.requestCount.Store(perPeerPerBlockConcurrentRequestLimit())

		// send a have
		p2.requestChan <- request{
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
		reactors, _ := createTestReactors(2, cfg.DefaultP2PConfig(), false, "/tmp/test/wants_sending_routine")
		reactor1 := reactors[0]
		reactor2 := reactors[1]

		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
		cb, _ := createCompactBlock(t, prop, ps, metaData)

		for _, reactor := range reactors {
			added := reactor.AddProposal(cb)
			require.True(t, added)
		}

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)

		// restart the wants sending routine
		close(p2.requestChan)
		p2.requestChan = make(chan request, 3000)
		go reactor1.wantsSendingRoutine(p2)

		p2.requestCount.Store(perPeerPerBlockConcurrentRequestLimit())
		p2.requestChan <- request{
			height: prop.Height,
			round:  prop.Round,
			index:  0,
		}
		p2.receivedPart <- struct{}{}
		time.Sleep(200 * time.Millisecond)

		p1 := reactor2.getPeer(reactor1.self)
		require.NotNil(t, p1)
		wants, has := p1.GetWants(prop.Height, prop.Round)
		require.True(t, has)
		assert.False(t, wants.IsEmpty())
		assert.True(t, wants.GetIndex(0))
	})

	t.Run("not requesting part - already requested from another peer", func(t *testing.T) {
		reactors, _ := createTestReactors(3, cfg.DefaultP2PConfig(), false, "/tmp/test/wants_sending_routine")
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor3 := reactors[2]

		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
		cb, _ := createCompactBlock(t, prop, ps, metaData)

		for _, reactor := range reactors {
			added := reactor.AddProposal(cb)
			require.True(t, added)
		}

		// set the request to be sent from another peer
		p3 := reactor1.getPeer(reactor3.self)
		require.NotNil(t, p3)
		p3.initialize(prop.Height, prop.Round, int(prop.BlockID.PartSetHeader.Total))
		reqs, has := p3.GetRequests(prop.Height, prop.Round)
		require.True(t, has)
		reqs.SetIndex(0, true)

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)

		p2.requestCount.Store(0)
		p2.requestChan <- request{
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
		reactors, _ := createTestReactors(3, cfg.DefaultP2PConfig(), false, "/tmp/test/wants_sending_routine")
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor3 := reactors[2]

		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
		cb, _ := createCompactBlock(t, prop, ps, metaData)

		for _, reactor := range reactors {
			added := reactor.AddProposal(cb)
			require.True(t, added)
		}

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)
		p2.requestCount.Store(0)
		p2.requestChan <- request{
			height: prop.Height,
			round:  prop.Round,
			index:  0,
		}
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
		reactors, _ := createTestReactors(3, cfg.DefaultP2PConfig(), false, "/tmp/test/wants_sending_routine")
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor3 := reactors[2]

		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
		cb, _ := createCompactBlock(t, prop, ps, metaData)

		for _, reactor := range reactors {
			added := reactor.AddProposal(cb)
			require.True(t, added)
		}

		p2 := reactor1.getPeer(reactor2.self)
		require.NotNil(t, p2)
		p2.requestCount.Store(0)

		// restart the wants sending routine
		close(p2.requestChan)
		p2.requestChan = make(chan request, 3000)
		go reactor1.wantsSendingRoutine(p2)

		for i := 0; i < 10; i++ {
			p2.requestChan <- request{
				height: prop.Height,
				round:  prop.Round,
				index:  uint32(i),
			}
		}
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
