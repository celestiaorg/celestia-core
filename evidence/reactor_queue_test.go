package evidence_test

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/evidence"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// queueTestPeer adds the methods the base reactor needs to the mock peer.
type queueTestPeer struct{ *mock.Peer }

func (queueTestPeer) Metrics() *p2p.Metrics         { return p2p.NopMetrics() }
func (queueTestPeer) ValueToMetricLabel(any) string { return "evidence" }
func (queueTestPeer) TraceClient() trace.Tracer     { return trace.NoOpTracer() }

// blockingLogger blocks every Info call until release is called. The evidence
// pool logs at the start of AddEvidence, so this stalls the reactor's
// processing of a message.
type blockingLogger struct {
	log.Logger
	entered     chan struct{}
	unblock     chan struct{}
	unblockOnce sync.Once
}

func newBlockingLogger() *blockingLogger {
	return &blockingLogger{
		Logger:  log.NewNopLogger(),
		entered: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
}

func (l *blockingLogger) Info(string, ...interface{}) {
	select {
	case l.entered <- struct{}{}:
	default:
	}
	<-l.unblock
}

func (l *blockingLogger) With(...interface{}) log.Logger { return l }

// release unblocks Info. It is safe to call more than once.
func (l *blockingLogger) release() {
	l.unblockOnce.Do(func() { close(l.unblock) })
}

// A peer must not be blocked while the reactor is busy with another message.
// Blocking stalls the peer's whole connection, because the message is queued
// from the connection's receive routine.
func TestReactorDoesNotBlockPeersWhenQueueIsFull(t *testing.T) {
	const height = int64(21)
	pool, val := defaultTestPool(t, height)
	reactor := evidence.NewReactor(pool)
	reactor.SetLogger(log.NewNopLogger())
	// The reactor's SetLogger also sets the pool's logger, so set the
	// blocking logger on the pool afterwards.
	logger := newBlockingLogger()
	pool.SetLogger(logger)
	t.Cleanup(reactor.BaseReactor.OnStop)
	t.Cleanup(logger.release)

	ev, err := types.NewMockDuplicateVoteEvidenceWithValidator(
		height, defaultEvidenceTime.Add(21*time.Minute), val, evidenceChainID)
	require.NoError(t, err)
	evProto, err := types.EvidenceToProto(ev)
	require.NoError(t, err)
	evidenceList := cmtproto.EvidenceList{Evidence: []cmtproto.Evidence{*evProto}}
	message, err := evidenceList.Marshal()
	require.NoError(t, err)

	envelope := p2p.UnprocessedEnvelope{
		Src:       queueTestPeer{mock.NewPeer(net.IP{1, 2, 3, 4})},
		ChannelID: evidence.EvidenceChannel,
		Message:   message,
	}

	// The reactor takes the first message and stalls while processing it.
	reactor.QueueUnprocessedEnvelope(envelope)
	select {
	case <-logger.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the reactor did not start processing the first message")
	}

	// Fill the queue and then send more. None of the sends may block.
	sendsDone := make(chan struct{})
	go func() {
		defer close(sendsDone)
		for i := 0; i < evidence.ReactorIncomingMessageQueueSize+10; i++ {
			reactor.QueueUnprocessedEnvelope(envelope)
		}
	}()

	select {
	case <-sendsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("queueing a message blocked while the reactor was busy")
	}
}
