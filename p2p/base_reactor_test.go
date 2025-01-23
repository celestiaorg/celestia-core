package p2p_test

import (
	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/service"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/conn"
	"github.com/tendermint/tendermint/pkg/trace"
	bcproto "github.com/tendermint/tendermint/proto/tendermint/blockchain"
	"github.com/tendermint/tendermint/proto/tendermint/mempool"
	"net"
	"sync"
	"testing"
	"time"
)

// TestBaseReactorProcessor tests the BaseReactor's message processing by
// queueing encoded messages and adding artificial delay to the first message.
// Depending on the processors used, the ordering of the sender could be lost.
func TestBaseReactorProcessor(t *testing.T) {
	// a reactor that is using the default proessor should be able to queue
	// messages and they get processed in order.
	or := NewOrderedReactor(false)

	msgs := []string{"msg1", "msg2", "msg3"}
	or.fillQueue(t, msgs...)

	time.Sleep(300 * time.Millisecond) // wait plenty of time for the processing to finish

	require.Equal(t, len(msgs), len(or.received))
	require.Equal(t, msgs, or.received)

	// since the orderedReactor adds a delay to the first received message, we
	// expect the parallel processor to not be in the original send order.
	pr := NewOrderedReactor(true)

	pr.fillQueue(t, msgs...)
	time.Sleep(300 * time.Millisecond)
	require.NotEqual(t, msgs, pr.received)
}

var _ p2p.Reactor = &orderedReactor{}

// orderedReactor is used for testing. It saves each envelope in the order it
// receives it.
type orderedReactor struct {
	p2p.BaseReactor

	mtx           *sync.RWMutex
	received      []string
	receivedFirst bool
}

func NewOrderedReactor(parallel bool) *orderedReactor {
	r := &orderedReactor{mtx: &sync.RWMutex{}}
	procOpt := p2p.WithProcessor(p2p.DefaultProcessor(r))
	if parallel {
		procOpt = p2p.WithProcessor(p2p.ParallelProcessor(r, 2))
	}
	r.BaseReactor = *p2p.NewBaseReactor("Ordered Rector", r, procOpt, p2p.WithIncomingQueueSize(10))
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

// Receive adds a delay to the first processed envelope to test ordering.
func (r *orderedReactor) Receive(chID byte, p p2p.Peer, msgBytes []byte) {
	r.mtx.Lock()
	f := r.receivedFirst
	if !f {
		r.receivedFirst = true
		r.mtx.Unlock()
		time.Sleep(100 * time.Millisecond)
	} else {
		r.mtx.Unlock()
	}
	r.mtx.Lock()
	defer r.mtx.Unlock()

	msg := &bcproto.Message{}
	err := proto.Unmarshal(msgBytes, msg)
	if err != nil {
		panic(err)
	}
	uw, err := msg.Unwrap()
	if err != nil {
		panic(err)
	}

	envMsg := uw.(*mempool.Txs)
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

func (ip *imaginaryPeer) TraceClient() trace.Tracer          { return nil }
func (ip *imaginaryPeer) HasIPChanged() bool                 { return false }
func (ip *imaginaryPeer) FlushStop()                         {}
func (ip *imaginaryPeer) ID() p2p.ID                         { return "" }
func (ip *imaginaryPeer) RemoteIP() net.IP                   { return []byte{} }
func (ip *imaginaryPeer) RemoteAddr() net.Addr               { return nil }
func (ip *imaginaryPeer) IsOutbound() bool                   { return true }
func (ip *imaginaryPeer) CloseConn() error                   { return nil }
func (ip *imaginaryPeer) IsPersistent() bool                 { return false }
func (ip *imaginaryPeer) NodeInfo() p2p.NodeInfo             { return nil }
func (ip *imaginaryPeer) Status() conn.ConnectionStatus      { return conn.ConnectionStatus{} }
func (ip *imaginaryPeer) SocketAddr() *p2p.NetAddress        { return nil }
func (ip *imaginaryPeer) Send(byte, []byte) bool             { return true }
func (ip *imaginaryPeer) TrySend(byte, []byte) bool          { return true }
func (ip *imaginaryPeer) Set(key string, value any)          {}
func (ip *imaginaryPeer) Get(key string) any                 { return nil }
func (ip *imaginaryPeer) SetRemovalFailed()                  {}
func (ip *imaginaryPeer) GetRemovalFailed() bool             { return false }
func (ip *imaginaryPeer) Metrics() *p2p.Metrics              { return p2p.NopMetrics() }
func (ip *imaginaryPeer) ChIDToMetricLabel(chID byte) string { return "" }
func (ip *imaginaryPeer) ValueToMetricLabel(i any) string    { return "" }
