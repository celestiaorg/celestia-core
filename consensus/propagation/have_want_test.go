package propagation

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/state"
	"testing"
	"time"
)

func TestWantsSendingRoutine(t *testing.T) {
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

	prop2, ps2, _, metaData2 := createTestProposal(sm, 2, 2, 1000000)
	cb2, _ := createCompactBlock(t, prop2, ps2, metaData2)

	for _, reactor := range reactors {
		added := reactor.AddProposal(cb)
		require.True(t, added)
	}

	tests := []struct {
		name     string
		setup    func()
		testCase func(t *testing.T)
	}{
		{
			name: "maximum concurrent request count - want not sent",
			setup: func() {
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
			},
			testCase: func(t *testing.T) {
				p1 := reactor2.getPeer(reactor1.self)
				require.NotNil(t, p1)
				wants, has := p1.GetWants(prop.Height, prop.Round)
				if has {
					assert.True(t, wants.IsEmpty())
				}
			},
		},
		{
			name: "maximum concurrent request count - want sent after receiving a part",
			setup: func() {
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
			},
			testCase: func(t *testing.T) {
				p1 := reactor2.getPeer(reactor1.self)
				require.NotNil(t, p1)
				wants, has := p1.GetWants(prop.Height, prop.Round)
				require.True(t, has)
				assert.False(t, wants.IsEmpty())
				assert.True(t, wants.GetIndex(0))
			},
		},
		{
			name: "not requesting part - already requested by another peer",
			setup: func() {

			},
			testCase: func(t *testing.T) {

			},
		},
		{
			name: "part requested successfully + checking if haves were broadcasted",
			setup: func() {
				p2 := reactor1.getPeer(reactor2.self)
				require.NotNil(t, p2)
				p2.requestCount.Store(0)
				p2.requestChan <- request{
					height: prop.Height,
					round:  prop.Round,
					index:  0,
				}
				time.Sleep(200 * time.Millisecond)
			},
			testCase: func(t *testing.T) {
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
			},
		},
		{
			name: "batch parts requested successfully + checking if haves were broadcasted",
			setup: func() {
				// register a new height to avoid interference between tests
				for _, reactor := range reactors {
					added := reactor.AddProposal(cb2)
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
						height: prop2.Height,
						round:  prop2.Round,
						index:  uint32(i),
					}
				}
				time.Sleep(200 * time.Millisecond)
			},
			testCase: func(t *testing.T) {
				p1 := reactor2.getPeer(reactor1.self)
				require.NotNil(t, p1)
				wants, has := p1.GetWants(prop2.Height, prop2.Round)
				require.True(t, has)
				assert.False(t, wants.IsEmpty())
				for i := 0; i < 10; i++ {
					assert.True(t, wants.GetIndex(i), i)
				}

				// check if haves have been received by the third reactor
				p1 = reactor3.getPeer(reactor1.self)
				require.NotNil(t, p1)
				haves, has := p1.GetHaves(prop2.Height, prop2.Round)
				require.True(t, has)
				assert.False(t, haves.IsEmpty())
				for i := 0; i < 10; i++ {
					assert.True(t, haves.GetIndex(i), i)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			tt.testCase(t)
		})
	}
}
