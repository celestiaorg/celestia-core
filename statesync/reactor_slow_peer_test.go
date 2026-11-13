package statesync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	ssproto "github.com/cometbft/cometbft/proto/tendermint/statesync"
)

// chunkServer is an AppConnSnapshot that serves the same chunk for every
// request and counts LoadSnapshotChunk calls.
type chunkServer struct {
	chunk []byte
	loads atomic.Int64
}

func (*chunkServer) Error() error { return nil }

func (*chunkServer) ListSnapshots(context.Context, *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	return &abci.ResponseListSnapshots{}, nil
}

func (*chunkServer) OfferSnapshot(context.Context, *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	return &abci.ResponseOfferSnapshot{}, nil
}

func (c *chunkServer) LoadSnapshotChunk(context.Context, *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	c.loads.Add(1)
	return &abci.ResponseLoadSnapshotChunk{Chunk: c.chunk}, nil
}

func (*chunkServer) ApplySnapshotChunk(context.Context, *abci.RequestApplySnapshotChunk) (*abci.ResponseApplySnapshotChunk, error) {
	return &abci.ResponseApplySnapshotChunk{}, nil
}

// countingReactor registers the statesync channels and counts chunk
// responses it receives.
type countingReactor struct {
	p2p.BaseReactor
	chunkResponses atomic.Int64
}

func newCountingReactor(opts ...p2p.ReactorOptions) *countingReactor {
	r := &countingReactor{}
	r.BaseReactor = *p2p.NewBaseReactor("Counting", r, opts...)
	return r
}

func (*countingReactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:                  SnapshotChannel,
			Priority:            5,
			SendQueueCapacity:   10,
			RecvMessageCapacity: snapshotMsgSize,
			MessageType:         &ssproto.Message{},
		},
		{
			ID:                  ChunkChannel,
			Priority:            3,
			SendQueueCapacity:   10,
			RecvMessageCapacity: chunkMsgSize,
			MessageType:         &ssproto.Message{},
		},
	}
}

func (r *countingReactor) Receive(e p2p.Envelope) {
	if _, ok := e.Message.(*ssproto.ChunkResponse); ok {
		r.chunkResponses.Add(1)
	}
}

// TestReceiveDoesNotBlockOnSlowPeer checks that a peer which stops reading
// from its connection does not delay replies to other peers.
func TestReceiveDoesNotBlockOnSlowPeer(t *testing.T) {
	const (
		chunkSize = 1 << 20 // 1 MiB
		// slowRequests is large enough to fill the send queue and the TCP
		// buffers between the server and the slow peer.
		slowRequests = 64
	)

	cfg := cmtcfg.DefaultP2PConfig()
	cfg.AllowDuplicateIP = true
	cfg.SendRate = 1 << 30
	cfg.RecvRate = 1 << 30

	app := &chunkServer{chunk: make([]byte, chunkSize)}
	// The slow peer never drains its one-slot incoming queue, so it stops
	// reading from the connection.
	slow := newCountingReactor(
		p2p.WithIncomingQueueSize(1),
		p2p.WithProcessor(func(ctx context.Context, _ <-chan p2p.UnmarshalResult) { <-ctx.Done() }),
	)
	honest := newCountingReactor()

	switches := make([]*p2p.Switch, 3)
	for i := range switches {
		switches[i] = p2p.MakeSwitch(cfg, i, func(i int, sw *p2p.Switch) *p2p.Switch {
			switch i {
			case 0:
				sw.AddReactor("STATESYNC", NewReactor(*cmtcfg.DefaultStateSyncConfig(), app, nil, NopMetrics()))
			case 1:
				sw.AddReactor("STATESYNC", slow)
			case 2:
				sw.AddReactor("STATESYNC", honest)
			}
			return sw
		})
		switches[i].SetLogger(log.NewNopLogger())
	}
	require.NoError(t, p2p.StartSwitches(switches))
	t.Cleanup(func() {
		for _, sw := range switches {
			_ = sw.Stop()
		}
	})
	server, slowSw, honestSw := switches[0], switches[1], switches[2]

	require.NoError(t, slowSw.DialPeerWithAddress(server.NetAddress()))
	require.NoError(t, honestSw.DialPeerWithAddress(server.NetAddress()))
	require.Eventually(t, func() bool { return server.Peers().Size() == 2 }, 5*time.Second, 10*time.Millisecond)

	serverID := server.NodeInfo().ID()
	slowPeer := slowSw.Peers().Get(serverID)
	honestPeer := honestSw.Peers().Get(serverID)
	require.NotNil(t, slowPeer)
	require.NotNil(t, honestPeer)

	for i := 0; i < slowRequests; i++ {
		require.True(t, slowPeer.Send(p2p.Envelope{
			ChannelID: ChunkChannel,
			Message:   &ssproto.ChunkRequest{Height: 1, Format: 1, Index: uint32(i)},
		}))
	}

	// Wait until the server has handled every request from the slow peer, or
	// until it stops making progress.
	deadline := time.Now().Add(10 * time.Second)
	last, lastChange := int64(-1), time.Now()
	for time.Now().Before(deadline) {
		n := app.loads.Load()
		if n >= slowRequests {
			break
		}
		if n != last {
			last, lastChange = n, time.Now()
		} else if time.Since(lastChange) > time.Second {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.True(t, honestPeer.Send(p2p.Envelope{
		ChannelID: ChunkChannel,
		Message:   &ssproto.ChunkRequest{Height: 1, Format: 1, Index: 0},
	}))
	require.Eventually(t, func() bool { return honest.chunkResponses.Load() == 1 }, 3*time.Second, 10*time.Millisecond,
		"the honest peer did not receive a chunk response")
	require.True(t, server.Peers().Has(slowSw.NodeInfo().ID()), "the slow peer should stay connected")
}
