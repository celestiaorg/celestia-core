package statesync

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	ssproto "github.com/cometbft/cometbft/proto/tendermint/statesync"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// fuzzMaxSnapshotChunks keeps chunk queues small so each input stays fast. The
// reactor must reject anything above it, so larger counts never reach a queue.
const fuzzMaxSnapshotChunks = 16

var fuzzAppHash = []byte("fuzz_app_hash")

// fuzzBytes hands out fields from fuzz input, returning zero values once
// exhausted.
type fuzzBytes struct{ b []byte }

func (r *fuzzBytes) empty() bool { return len(r.b) == 0 }

func (r *fuzzBytes) u8() byte {
	if len(r.b) == 0 {
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *fuzzBytes) u32() uint32 {
	var buf [4]byte
	n := copy(buf[:], r.b)
	r.b = r.b[n:]
	return binary.BigEndian.Uint32(buf[:])
}

func (r *fuzzBytes) u64() uint64 {
	var buf [8]byte
	n := copy(buf[:], r.b)
	r.b = r.b[n:]
	return binary.BigEndian.Uint64(buf[:])
}

// height returns a small value most of the time, so fields match across
// messages, and an arbitrary one when the high bit is set.
func (r *fuzzBytes) height() uint64 {
	if b := r.u8(); b&0x80 == 0 {
		return uint64(b & 0x03)
	}
	return r.u64()
}

func (r *fuzzBytes) small32(mask byte) uint32 {
	if b := r.u8(); b&0x80 == 0 {
		return uint32(b & mask)
	}
	return r.u32()
}

// bytes returns nil for a 0xff length prefix, and otherwise up to that many
// bytes (possibly empty, but non-nil).
func (r *fuzzBytes) bytes() []byte {
	n := int(r.u8())
	if n == 0xff {
		return nil
	}
	if n > len(r.b) {
		n = len(r.b)
	}
	out := append([]byte{}, r.b[:n]...)
	r.b = r.b[n:]
	return out
}

// fuzzPeer is a running mock peer with a fixed ID that counts sent messages.
type fuzzPeer struct {
	*p2pmock.Peer
	id   p2p.ID
	sent int
}

func newFuzzPeer(id string) *fuzzPeer {
	return &fuzzPeer{Peer: p2pmock.NewPeer(net.IP{10, 0, 0, 1}), id: p2p.ID(id)}
}

func (p *fuzzPeer) ID() p2p.ID { return p.id }

func (p *fuzzPeer) Send(p2p.Envelope) bool {
	p.sent++
	return true
}

// fuzzSnapshotConn is a scripted ABCI snapshot connection.
type fuzzSnapshotConn struct {
	offer    abci.ResponseOfferSnapshot_Result
	script   *fuzzBytes
	applies  int
	accepted map[uint32]bool
}

func (*fuzzSnapshotConn) Error() error { return nil }

func (*fuzzSnapshotConn) ListSnapshots(context.Context, *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	snapshots := make([]*abci.Snapshot, 0, recentSnapshots+2)
	for i := uint64(1); i <= recentSnapshots+2; i++ {
		snapshots = append(snapshots, &abci.Snapshot{Height: i, Format: 1, Chunks: 1, Hash: []byte{byte(i)}})
	}
	return &abci.ResponseListSnapshots{Snapshots: snapshots}, nil
}

func (c *fuzzSnapshotConn) OfferSnapshot(context.Context, *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	return &abci.ResponseOfferSnapshot{Result: c.offer}, nil
}

func (*fuzzSnapshotConn) LoadSnapshotChunk(_ context.Context, req *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	if req.Chunk%2 == 1 {
		return &abci.ResponseLoadSnapshotChunk{}, nil
	}
	return &abci.ResponseLoadSnapshotChunk{Chunk: []byte{byte(req.Chunk)}}, nil
}

func (c *fuzzSnapshotConn) ApplySnapshotChunk(_ context.Context, req *abci.RequestApplySnapshotChunk) (*abci.ResponseApplySnapshotChunk, error) {
	c.applies++
	// Bound RETRY loops: once the budget is spent every chunk is accepted.
	if c.applies > 4*fuzzMaxSnapshotChunks || c.script.empty() {
		c.accepted[req.Index] = true
		return &abci.ResponseApplySnapshotChunk{Result: abci.ResponseApplySnapshotChunk_ACCEPT}, nil
	}
	b := c.script.u8()
	resp := &abci.ResponseApplySnapshotChunk{Result: abci.ResponseApplySnapshotChunk_Result(b % 7)}
	switch resp.Result {
	case abci.ResponseApplySnapshotChunk_ACCEPT:
		c.accepted[req.Index] = true
	case abci.ResponseApplySnapshotChunk_ABORT, abci.ResponseApplySnapshotChunk_RETRY_SNAPSHOT,
		abci.ResponseApplySnapshotChunk_REJECT_SNAPSHOT:
		// Refetches and sender rejections are only requested together with a
		// result that ends applyChunks, since otherwise the queue would wait
		// for a refetch that no fetcher is running to deliver.
		if b&0x80 != 0 {
			resp.RefetchChunks = []uint32{c.script.small32(0x1f)}
			resp.RejectSenders = []string{req.Sender, string(c.script.bytes())}
		}
	}
	return resp, nil
}

type fuzzQueryConn struct{ info *abci.ResponseInfo }

func (*fuzzQueryConn) Error() error { return nil }

func (*fuzzQueryConn) Echo(context.Context, string) (*abci.ResponseEcho, error) {
	return &abci.ResponseEcho{}, nil
}

func (c *fuzzQueryConn) Info(context.Context, *abci.RequestInfo) (*abci.ResponseInfo, error) {
	return c.info, nil
}

func (*fuzzQueryConn) Query(context.Context, *abci.RequestQuery) (*abci.ResponseQuery, error) {
	return &abci.ResponseQuery{}, nil
}

// fuzzStateProvider returns a fixed trusted app hash, state and commit.
type fuzzStateProvider struct{}

func (fuzzStateProvider) AppHash(context.Context, uint64) ([]byte, error) { return fuzzAppHash, nil }

func (fuzzStateProvider) Commit(_ context.Context, height uint64) (*types.Commit, error) {
	return &types.Commit{Height: int64(height), BlockID: types.BlockID{Hash: []byte("blockhash")}}, nil
}

func (fuzzStateProvider) State(_ context.Context, height uint64) (sm.State, error) {
	s := sm.State{
		ChainID:         "fuzz",
		Version:         cmtstate.Version{Consensus: cmtversion.Consensus{App: testAppVersion}},
		LastBlockHeight: int64(height),
		AppHash:         fuzzAppHash,
		ConsensusParams: *types.DefaultConsensusParams(),
	}
	s.ConsensusParams.Version.App = testAppVersion
	return s, nil
}

func newFuzzSwitch(tb testing.TB) *p2p.Switch {
	cfg := config.DefaultP2PConfig()
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKeyFromSecret([]byte("statesync-fuzz"))}
	transport := p2p.NewMultiplexTransport(p2p.DefaultNodeInfo{}, nodeKey, p2p.MConnConfig(cfg), trace.NoOpTracer())
	sw := p2p.NewSwitch(cfg, transport)
	sw.SetLogger(log.NewNopLogger())
	require.NotNil(tb, sw)
	return sw
}

// FuzzStateSyncReactor drives the state-sync reactor and an in-progress syncer
// with a sequence of peer messages, then runs Sync on the best snapshot with a
// scripted ABCI app. Peers are stopped iff validateMsg rejects their message,
// the snapshot pool stays bounded per peer, chunk queues stay within the
// configured chunk limit, and Sync only succeeds when the app was verified.
func FuzzStateSyncReactor(f *testing.F) {
	enc := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	u32 := func(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
	snap := []byte{0, 0, 1, 1, 2, 3, 1, 2, 3, 0}                                 // height 1, format 1, chunks 2, hash {1,2,3}, no metadata
	chunkMsg := func(i byte) []byte { return []byte{1, 0, 1, 1, i, 0, 2, 9, 9} } // height 1, format 1, index i
	f.Add(enc(snap, []byte{5, 0}, chunkMsg(0), chunkMsg(1), []byte{0xff, 1, 0}))
	f.Add(enc(snap, []byte{5, 0}, chunkMsg(0), []byte{0xff, 1, 0, 3, 1, 0x82, 0, 2, 'a', 'a'}))
	f.Add(enc([]byte{0, 1, 1, 1, 0x80}, u32(1<<31), []byte{1, 7, 0}))                      // huge chunk count
	f.Add(enc([]byte{0, 0, 0, 1, 1, 0, 1, 0, 0}))                                          // zero height, empty hash
	f.Add(enc([]byte{2, 0, 3, 0, 1, 1, 3, 1, 0, 0, 4, 1}, []byte{1, 2, 3}))                // requests and raw bytes
	f.Add(enc(snap, []byte{5, 0}, []byte{1, 0, 1, 1, 7, 1, 0}, []byte{6, 0}, chunkMsg(0))) // missing flag, remove peer
	for _, msg := range []proto.Message{
		&ssproto.SnapshotsRequest{},
		&ssproto.SnapshotsResponse{Height: 1, Format: 1, Chunks: 2, Hash: []byte{1}},
		&ssproto.ChunkRequest{Height: 1, Format: 1, Index: 1},
		&ssproto.ChunkResponse{Height: 1, Format: 1, Index: 1, Chunk: []byte{1}},
		&ssproto.ChunkResponse{Height: 1, Format: 1, Index: 1, Missing: true},
	} {
		raw, err := proto.Marshal(msg.(p2p.Wrapper).Wrap())
		require.NoError(f, err)
		f.Add(enc([]byte{4, 0, 0, byte(len(raw))}, raw))
		f.Add(enc([]byte{4, 0, 1, byte(len(raw))}, raw))
	}

	sw := newFuzzSwitch(f)
	tempDir := f.TempDir()

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		in := &fuzzBytes{b: data}

		cfg := *config.DefaultStateSyncConfig()
		cfg.MaxSnapshotChunks = fuzzMaxSnapshotChunks
		cfg.ChunkFetchers = 0 // Sync must not spawn fetchers that outlive the input
		conn := &fuzzSnapshotConn{script: &fuzzBytes{}, accepted: map[uint32]bool{}}
		query := &fuzzQueryConn{}
		r := NewReactor(cfg, conn, query, NopMetrics())
		r.SetSwitch(sw)
		require.NoError(t, r.Start())
		t.Cleanup(func() { require.NoError(t, r.Stop()) })

		s := newSyncer(cfg, log.NewNopLogger(), conn, query, fuzzStateProvider{}, tempDir)
		var rejected []p2p.ID
		s.setOnPeerRejected(func(id p2p.ID) { rejected = append(rejected, id) })
		r.syncer = s

		peers := []*fuzzPeer{newFuzzPeer("aa"), newFuzzPeer("bb"), newFuzzPeer("cc")}
		var (
			queue *chunkQueue
			best  *snapshot
		)
		t.Cleanup(func() {
			if queue != nil {
				require.NoError(t, queue.Close())
			}
		})

		receive := func(peer *fuzzPeer, ch byte, msg proto.Message) {
			wantErr := validateMsg(msg, cfg.MaxSnapshotChunks)
			sentBefore := peer.sent
			r.Receive(p2p.Envelope{ChannelID: ch, Src: peer, Message: msg})
			require.Equal(t, wantErr == nil, peer.IsRunning(), "peer must be stopped iff its message is invalid: %v", wantErr)
			if errors.Is(wantErr, ErrExceedsMaxSnapshotChunks) {
				require.True(t, s.snapshots.peerBlacklist[peer.ID()], "peer advertising too many chunks must be rejected")
			}
			require.LessOrEqual(t, peer.sent-sentBefore, recentSnapshots)
		}

		for ops := 0; !in.empty() && ops < 64; ops++ {
			op := in.u8()
			if op == 0xff {
				break // the rest of the input scripts the restore
			}
			peer := peers[int(in.u8())%len(peers)]
			if !peer.IsRunning() {
				// The switch no longer delivers messages from a stopped peer.
				continue
			}
			switch op % 8 {
			case 0:
				receive(peer, SnapshotChannel, &ssproto.SnapshotsResponse{
					Height: in.height(), Format: in.small32(0x01), Chunks: in.small32(0x1f),
					Hash: in.bytes(), Metadata: in.bytes(),
				})
			case 1:
				receive(peer, ChunkChannel, &ssproto.ChunkResponse{
					Height: in.height(), Format: in.small32(0x01), Index: in.small32(0x1f),
					Missing: in.u8()&1 == 1, Chunk: in.bytes(),
				})
			case 2:
				receive(peer, SnapshotChannel, &ssproto.SnapshotsRequest{})
			case 3:
				before := peer.sent
				msg := &ssproto.ChunkRequest{Height: in.height(), Format: in.small32(0x01), Index: in.small32(0x1f)}
				receive(peer, ChunkChannel, msg)
				if msg.Height != 0 {
					require.Equal(t, before+1, peer.sent, "valid chunk request must be answered")
				}
			case 4:
				// Raw bytes follow the BaseReactor decode path; decode failures
				// disconnect the peer before Receive is reached.
				ch := SnapshotChannel + in.u8()%2
				var envelope ssproto.Message
				if proto.Unmarshal(in.bytes(), &envelope) != nil {
					continue
				}
				msg, err := envelope.Unwrap()
				if err != nil {
					continue
				}
				receive(peer, ch, msg)
			case 5:
				if queue == nil {
					if best = s.snapshots.Best(); best != nil {
						require.LessOrEqual(t, best.Chunks, cfg.MaxSnapshotChunks)
						var err error
						queue, err = newChunkQueue(best, tempDir)
						require.NoError(t, err)
						s.mtx.Lock()
						s.chunks = queue
						s.mtx.Unlock()
					}
				}
			case 6:
				r.RemovePeer(peer, nil)
				require.Empty(t, s.snapshots.peerIndex[peer.ID()])
			case 7:
				if queue != nil {
					require.NoError(t, queue.Discard(in.small32(0x1f)))
					queue.Retry(in.small32(0x1f))
				}
			}

			// Pool invariants: every stored snapshot passed validation and
			// each peer is limited to recentSnapshots entries.
			for id, keys := range s.snapshots.peerIndex {
				require.LessOrEqual(t, len(keys), recentSnapshots, "peer %v", id)
			}
			require.LessOrEqual(t, len(s.snapshots.snapshots), recentSnapshots*len(peers))
			for _, sn := range s.snapshots.snapshots {
				require.NoError(t, validateMsg(&ssproto.SnapshotsResponse{
					Height: sn.Height, Format: sn.Format, Chunks: sn.Chunks, Hash: sn.Hash,
				}, cfg.MaxSnapshotChunks))
			}
			if queue != nil {
				require.LessOrEqual(t, len(queue.chunkFiles), int(best.Chunks))
				for index := range queue.chunkFiles {
					require.Less(t, index, best.Chunks)
				}
			}
		}

		if queue == nil {
			return
		}

		// Stop accepting peer chunks, fill the gaps locally so applyChunks
		// never waits for a fetch, then restore with a scripted app.
		s.mtx.Lock()
		s.chunks = nil
		s.mtx.Unlock()
		for i := uint32(0); i < best.Chunks; i++ {
			if !queue.Has(i) {
				_, err := queue.Add(&chunk{Height: best.Height, Format: best.Format, Index: i, Chunk: []byte{}})
				require.NoError(t, err)
			}
		}
		conn.offer = abci.ResponseOfferSnapshot_Result(in.u8() % 7)
		info := &abci.ResponseInfo{
			AppVersion: testAppVersion, LastBlockAppHash: fuzzAppHash, LastBlockHeight: int64(best.Height),
		}
		switch in.u8() % 4 {
		case 1:
			info.AppVersion++
		case 2:
			info.LastBlockAppHash = []byte("wrong")
		case 3:
			info.LastBlockHeight++
		}
		query.info = info
		conn.script = in

		state, _, err := s.Sync(best, queue)
		if err == nil {
			require.Equal(t, abci.ResponseOfferSnapshot_ACCEPT, conn.offer)
			require.Equal(t, fuzzAppHash, info.LastBlockAppHash)
			require.Equal(t, best.Height, uint64(info.LastBlockHeight))
			require.Equal(t, uint64(testAppVersion), info.AppVersion)
			require.Equal(t, fuzzAppHash, best.trustedAppHash)
			require.Equal(t, int64(best.Height), state.LastBlockHeight)
			for i := uint32(0); i < best.Chunks; i++ {
				require.True(t, conn.accepted[i], "chunk %d was never accepted", i)
			}
		} else if conn.offer == abci.ResponseOfferSnapshot_ABORT {
			require.ErrorIs(t, err, errAbort)
		}
		for _, id := range rejected {
			require.True(t, s.snapshots.peerBlacklist[id])
		}
	})
}
