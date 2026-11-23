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
	unblock     chan struct{}
	unblockOnce sync.Once
	// addAttempts counts the AddEvidence calls: one per processed message.
	addAttempts atomic.Int32
}

func (l *blockingLogger) Info(msg string, _ ...interface{}) {
	if msg == "Attempting to add evidence" {
		l.addAttempts.Add(1)
	}
	<-l.unblock
}

func (l *blockingLogger) release() {
	l.unblockOnce.Do(func() { close(l.unblock) })
}

// stalledEvidenceReactor returns a reactor that is stalled processing a
// message until the returned logger is released, and an evidence message to
// send to it.
func stalledEvidenceReactor(t *testing.T) (*evidence.Reactor, *blockingLogger, []byte) {
	t.Helper()
	const height = int64(21)
	pool, val := defaultTestPool(t, height)

	reactor := evidence.NewReactor(pool)
	reactor.SetLogger(log.NewNopLogger())
	// The reactor's SetLogger also sets the pool's logger, so set the
	// blocking logger on the pool afterwards.
	logger := &blockingLogger{Logger: log.NewNopLogger(), unblock: make(chan struct{})}
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

	send(reactor, newQueueTestPeer(), message)
	require.Eventually(t, func() bool {
		return logger.addAttempts.Load() == 1
	}, 5*time.Second, 10*time.Millisecond)

	return reactor, logger, message
}

func newQueueTestPeer() queueTestPeer {
	return queueTestPeer{mock.NewPeer(net.IP{1, 2, 3, 4})}
}

func send(reactor *evidence.Reactor, peer queueTestPeer, message []byte) {
	reactor.QueueUnprocessedEnvelope(
		p2p.UnprocessedEnvelope{Src: peer, ChannelID: evidence.EvidenceChannel, Message: message})
}

// A peer must not be blocked while the reactor is busy with another message.
// Blocking stalls the peer's whole connection, because the message is queued
// from the connection's receive routine.
func TestReactorDoesNotBlockPeersWhenQueueIsFull(t *testing.T) {
	reactor, _, message := stalledEvidenceReactor(t)

	sendsDone := make(chan struct{})
	go func() {
		defer close(sendsDone)
		for i := 0; i < evidence.ReactorIncomingMessageQueueSize+10; i++ {
			send(reactor, newQueueTestPeer(), message)
		}
	}()

	select {
	case <-sendsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("queueing a message blocked while the reactor was busy")
	}
}

// One peer must not be able to fill the queue: only a limited number of its
// messages can wait at a time, and the rest are dropped.
func TestReactorLimitsWaitingMessagesPerPeer(t *testing.T) {
	reactor, logger, message := stalledEvidenceReactor(t)

	// Disconnecting must not give the peer a new budget.
	floodingPeer := newQueueTestPeer()
	for i := 0; i < evidence.ReactorIncomingMessageQueueSize; i++ {
		send(reactor, floodingPeer, message)
		reactor.RemovePeer(floodingPeer, nil)
	}
	send(reactor, newQueueTestPeer(), message)
	logger.release()

	// The message that stalled the reactor, the flooding peer's share of the
	// queue and one message from another peer.
	const wantProcessed = 1 + evidence.MaxWaitingMessagesPerPeer + 1
	require.Eventually(t, func() bool {
		return logger.addAttempts.Load() == wantProcessed
	}, 5*time.Second, 10*time.Millisecond)

	// Once its messages have been processed, the flooding peer can queue again.
	send(reactor, floodingPeer, message)
	require.Eventually(t, func() bool {
		return logger.addAttempts.Load() == wantProcessed+1
	}, 5*time.Second, 10*time.Millisecond)
}

// A message that fails to decode is rejected before it is queued, so it does
// not use up the peer's budget.
func TestReactorRejectsUndecodableMessages(t *testing.T) {
	reactor, logger, message := stalledEvidenceReactor(t)

	peer := newQueueTestPeer()
	for i := 0; i < evidence.ReactorIncomingMessageQueueSize; i++ {
		send(reactor, peer, []byte("not an evidence list"))
	}
	for i := 0; i < evidence.MaxWaitingMessagesPerPeer; i++ {
		send(reactor, peer, message)
	}
	logger.release()

	const wantProcessed = 1 + evidence.MaxWaitingMessagesPerPeer
	require.Eventually(t, func() bool {
		return logger.addAttempts.Load() == wantProcessed
	}, 5*time.Second, 10*time.Millisecond)
}
