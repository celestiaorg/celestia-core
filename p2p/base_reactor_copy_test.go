package p2p

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p/conn"
	p2pproto "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

// copiedReactor embeds BaseReactor by value, as every production reactor does.
type copiedReactor struct {
	BaseReactor
	received chan struct{}
}

func newCopiedReactor() *copiedReactor {
	r := &copiedReactor{received: make(chan struct{}, 10)}
	r.BaseReactor = *NewBaseReactor("copied", r)
	return r
}

func (r *copiedReactor) GetChannels() []*conn.ChannelDescriptor {
	return []*conn.ChannelDescriptor{{
		ID:                  0x77,
		Priority:            1,
		SendQueueCapacity:   10,
		RecvMessageCapacity: 1 << 20,
		MessageType:         &p2pproto.PexAddrs{},
	}}
}

func (r *copiedReactor) Receive(Envelope) { r.received <- struct{}{} }

// OnStop does not call BaseReactor.OnStop, like most production reactors.
func (r *copiedReactor) OnStop() {}

// testPeer adds the introspection methods the processor needs to a mock peer.
type testPeer struct {
	Peer
}

func (testPeer) Metrics() *Metrics             { return NopMetrics() }
func (testPeer) ValueToMetricLabel(any) string { return "" }
func (testPeer) TraceClient() trace.Tracer     { return trace.NoOpTracer() }

type rawBytes []byte

func (m rawBytes) Marshal() ([]byte, error) { return m, nil }
func (rawBytes) Reset()                     {}
func (rawBytes) String() string             { return "raw" }
func (rawBytes) ProtoMessage()              {}

func TestBaseReactorCopyDisconnectsOnDecodeError(t *testing.T) {
	sws := MakeConnectedSwitches(cmtcfg.DefaultP2PConfig(), 2, func(_ int, sw *Switch) *Switch {
		sw.AddReactor("copied", newCopiedReactor())
		return sw
	}, Connect2Switches)
	t.Cleanup(func() {
		for _, sw := range sws {
			_ = sw.Stop()
		}
	})
	require.Equal(t, 1, sws[1].Peers().Size())
	// Field 1 with wire type 7 does not decode.
	require.True(t, sws[0].Peers().List()[0].Send(Envelope{ChannelID: 0x77, Message: rawBytes{0x0f}}))
	require.Eventually(t, func() bool { return sws[1].Peers().Size() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestBaseReactorCopyStopsProcessorWithoutBaseOnStop(t *testing.T) {
	r := newCopiedReactor()
	require.NoError(t, r.Start())
	require.NoError(t, r.Stop())
	// Stop cancels the processor itself, even though OnStop is overridden.
	require.Error(t, r.ctx.Err(), "processor context not canceled by Stop")

	msg, err := (&p2pproto.PexAddrs{Addrs: []p2pproto.NetAddress{{ID: "x"}}}).Marshal()
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		r.QueueUnprocessedEnvelope(UnprocessedEnvelope{Src: testPeer{CreateRandomPeer(false)}, Message: msg, ChannelID: 0x77})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queueing blocked after the reactor stopped")
	}
	select {
	case <-r.received:
		t.Fatal("Receive called after the reactor stopped")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBaseReactorDecodeErrorDropsPartialMessage(t *testing.T) {
	r := newCopiedReactor()
	// A valid empty address followed by a truncated one.
	msg := []byte{0x0a, 0x00, 0x0a, 0x05, 'x'}
	res := r.unmarshalEnvelope(UnprocessedEnvelope{Src: testPeer{CreateRandomPeer(false)}, Message: msg, ChannelID: 0x77})
	require.Error(t, res.Err)
	require.Nil(t, res.Msg)
}
