package evidence_test

import (
	"net"
	"sync"
	"sync/atomic"
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
	// addAttempts counts the AddEvidence calls, one per processed message
	// in these tests.
	addAttempts atomic.Int32
}

func newBlockingLogger() *blockingLogger {
	return &blockingLogger{
		Logger:  log.NewNopLogger(),
		entered: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
}

func (l *blockingLogger) Info(msg string, _ ...interface{}) {
	if msg == "Attempting to add evidence" {
		l.addAttempts.Add(1)
	}
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

// stalledEvidenceReactor returns a reactor whose processing of a message
// stalls until the returned logger is released, and an evidence message to
// send to it.
func stalledEvidenceReactor(t *testing.T) (*evidence.Reactor, *blockingLogger, []byte) {
	t.Helper()
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

	return reactor, logger, message
}

func newQueueTestPeer() queueTestPeer {
	return queueTestPeer{mock.NewPeer(net.IP{1, 2, 3, 4})}
}

func evidenceEnvelope(peer p2p.IntrospectivePeer, message []byte) p2p.UnprocessedEnvelope {
	return p2p.UnprocessedEnvelope{Src: peer, ChannelID: evidence.EvidenceChannel, Message: message}
}

// stallReactor sends one message and waits until the reactor is stalled
// processing it.
func stallReactor(t *testing.T, reactor *evidence.Reactor, logger *blockingLogger, message []byte) {
	t.Helper()
	reactor.QueueUnprocessedEnvelope(evidenceEnvelope(newQueueTestPeer(), message))
	select {
	case <-logger.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the reactor did not start processing the first message")
	}
}

// A peer must not be blocked while the reactor is busy with another message.
// Blocking stalls the peer's whole connection, because the message is queued
// from the connection's receive routine.
func TestReactorDoesNotBlockPeersWhenQueueIsFull(t *testing.T) {
	reactor, logger, message := stalledEvidenceReactor(t)
	stallReactor(t, reactor, logger, message)

	// Fill the queue from different peers and then send more. None of the
	// sends may block.
	sendsDone := make(chan struct{})
	go func() {
		defer close(sendsDone)
		for i := 0; i < evidence.ReactorIncomingMessageQueueSize+10; i++ {
			reactor.QueueUnprocessedEnvelope(evidenceEnvelope(newQueueTestPeer(), message))
		}
	}()

	select {
	case <-sendsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("queueing a message blocked while the reactor was busy")
	}
}

// One peer must not be able to fill the queue: only a limited number of its
// messages can wait at a time, and the rest are dropped. Other peers can
// still queue.
func TestReactorLimitsWaitingMessagesPerPeer(t *testing.T) {
	reactor, logger, message := stalledEvidenceReactor(t)
	stallReactor(t, reactor, logger, message)

	floodingPeer := newQueueTestPeer()
	for i := 0; i < evidence.ReactorIncomingMessageQueueSize; i++ {
		reactor.QueueUnprocessedEnvelope(evidenceEnvelope(floodingPeer, message))
	}
	otherPeer := newQueueTestPeer()
	reactor.QueueUnprocessedEnvelope(evidenceEnvelope(otherPeer, message))

	logger.release()

	// The message that stalled the reactor, the flooding peer's share of the
	// queue and one message from the other peer.
	const wantProcessed = 1 + evidence.MaxWaitingMessagesPerPeer + 1
	require.Eventually(t, func() bool {
		return logger.addAttempts.Load() == wantProcessed
	}, 5*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool {
		return logger.addAttempts.Load() > wantProcessed
	}, 200*time.Millisecond, 10*time.Millisecond)

	// Once its messages have been processed, the flooding peer can queue again.
	reactor.QueueUnprocessedEnvelope(evidenceEnvelope(floodingPeer, message))
	require.Eventually(t, func() bool {
		return logger.addAttempts.Load() == wantProcessed+1
	}, 5*time.Second, 10*time.Millisecond)
}
