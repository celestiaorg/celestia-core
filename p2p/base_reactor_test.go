package p2p_test

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/conn"
	"github.com/cometbft/cometbft/proto/tendermint/mempool"
)

// TestBaseReactorProcessor tests the BaseReactor's message processing by
// queueing encoded messages and adding artificial delay to the first message.
// Depending on the processors used, the ordering of the sender could be lost.
func TestBaseReactorProcessor(t *testing.T) {
	// a reactor using the default processor should be able to queue
	// messages, and they get processed in order.
	or := NewOrderedReactor()

	msgs := []string{"msg1", "msg2", "msg3"}
	or.fillQueue(t, msgs...)

	time.Sleep(300 * time.Millisecond) // wait plenty of time for the processing to finish

	or.Lock()
	require.Equal(t, len(msgs), len(or.received))
	require.Equal(t, msgs, or.received)
	or.Unlock()
}

var _ p2p.Reactor = &orderedReactor{}

// orderedReactor is used for testing. It saves each envelope in the order it
// receives it.
type orderedReactor struct {
	p2p.BaseReactor

	sync.Mutex
	received      []string
	receivedFirst bool
}

func NewOrderedReactor() *orderedReactor {
	r := &orderedReactor{Mutex: sync.Mutex{}}
	r.BaseReactor = *p2p.NewBaseReactor("Ordered Reactor", r, p2p.WithIncomingQueueSize(10))
	return r
}

func (r *orderedReactor) GetChannels() []*conn.ChannelDescriptor {
	return []*conn.ChannelDescriptor{
		{
			ID:                  0x99,
			Priority:            1,
			RecvMessageCapacity: 10,
			MessageType:         &mempool.Txs{},
		},
	}
}

// ReceiveEnvelope adds a delay to the first processed envelope to test ordering.
func (r *orderedReactor) ReceiveEnvelope(e p2p.Envelope) {
	r.Lock()
	f := r.receivedFirst
	if !f {
		r.receivedFirst = true
		r.Unlock()
		time.Sleep(100 * time.Millisecond)
	} else {
		r.Unlock()
	}
	r.Lock()
	defer r.Unlock()

	envMsg := e.Message.(*mempool.Txs)
	r.received = append(r.received, string(envMsg.Txs[0]))
}

func (r *orderedReactor) fillQueue(t *testing.T, msgs ...string) {
	peer := &imaginaryPeer{}
	for _, msg := range msgs {
		s, err := proto.Marshal(&mempool.Txs{Txs: [][]byte{[]byte(msg)}})
		require.NoError(t, err)
		r.QueueUnprocessedEnvelope(p2p.UnprocessedEnvelope{
			Src:       peer,
			Message:   s,
			ChannelID: 0x99,
		})
	}
}

var _ p2p.IntrospectivePeer = &imaginaryPeer{}

type imaginaryPeer struct {
	service.BaseService
}

func (ip *imaginaryPeer) TraceClient() trace.Tracer       { return trace.NoOpTracer() }
func (ip *imaginaryPeer) HasIPChanged() bool              { return false }
func (ip *imaginaryPeer) FlushStop()                      {}
func (ip *imaginaryPeer) ID() p2p.ID                      { return "" }
func (ip *imaginaryPeer) RemoteIP() net.IP                { return []byte{} }
func (ip *imaginaryPeer) RemoteAddr() net.Addr            { return nil }
func (ip *imaginaryPeer) IsOutbound() bool                { return true }
func (ip *imaginaryPeer) CloseConn() error                { return nil }
func (ip *imaginaryPeer) IsPersistent() bool              { return false }
func (ip *imaginaryPeer) NodeInfo() p2p.NodeInfo          { return nil }
func (ip *imaginaryPeer) Status() conn.ConnectionStatus   { return conn.ConnectionStatus{} }
func (ip *imaginaryPeer) SocketAddr() *p2p.NetAddress     { return nil }
func (ip *imaginaryPeer) Send(p2p.Envelope) bool          { return true }
func (ip *imaginaryPeer) TrySend(p2p.Envelope) bool       { return true }
func (ip *imaginaryPeer) Set(key string, value any)       {}
func (ip *imaginaryPeer) Get(key string) any              { return nil }
func (ip *imaginaryPeer) SetRemovalFailed()               {}
func (ip *imaginaryPeer) GetRemovalFailed() bool          { return false }
func (ip *imaginaryPeer) Metrics() *p2p.Metrics           { return p2p.NopMetrics() }
func (ip *imaginaryPeer) ValueToMetricLabel(i any) string { return "" }
