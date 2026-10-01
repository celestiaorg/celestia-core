package propagation

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

type parityRecordingPeer struct {
	p2p.Peer
	id   p2p.ID
	sent chan p2p.Envelope
}

func (p *parityRecordingPeer) ID() p2p.ID { return p.id }
func (p *parityRecordingPeer) TrySend(e p2p.Envelope) bool {
	p.sent <- e
	return true
}

func newParityTestReactor(t *testing.T, data []byte) (*Reactor, *proptypes.CompactBlock, *types.PartSet, *types.PartSet) {
	t.Helper()
	r := NewReactor("relay", Config{Store: store.NewBlockStore(dbm.NewMemDB())})
	t.Cleanup(r.cancel)
	r.ticker.Stop()
	r.started.Store(true)
	r.height = 1
	r.SetLogger(log.NewNopLogger())
	ops, err := types.NewPartSetFromData(data, types.BlockPartSizeBytes)
	require.NoError(t, err)
	eps, lastLen, err := types.Encode(ops, types.BlockPartSizeBytes)
	require.NoError(t, err)
	cb := &proptypes.CompactBlock{
		Proposal: types.Proposal{Height: 1, Round: 0, BlockID: types.BlockID{PartSetHeader: ops.Header()}},
		LastLen:  uint32(lastLen), BpHash: eps.Hash(), PartsHashes: extractHashes(ops, eps),
	}
	cb.SetProofCache(extractProofs(ops, eps))
	require.True(t, r.AddProposal(cb))
	return r, cb, ops, eps
}

func addParityTestPeer(t *testing.T, r *Reactor, id p2p.ID) (*parityRecordingPeer, *PeerState) {
	t.Helper()
	peer := &parityRecordingPeer{id: id, sent: make(chan p2p.Envelope, 100)}
	state := newPeerState(context.Background(), peer, log.NewNopLogger())
	r.setPeer(peer.id, state)
	t.Cleanup(state.cancel)
	return peer, state
}

func TestCompletedOriginalsServePromisedParity(t *testing.T) {
	r, _, ops, eps := newParityTestReactor(t, bytes.Repeat([]byte{17}, 4*int(types.BlockPartSizeBytes)))
	upstream, up := addParityTestPeer(t, r, "upstream")
	downstream, _ := addParityTestPeer(t, r, "downstream")

	// Relay asks upstream for parity and promises those same parts downstream.
	wantedParity := bits.NewBitArray(8)
	for i := 4; i < 8; i++ {
		wantedParity.SetIndex(i, true)
	}
	wants := &proptypes.WantParts{Height: 1, Round: 0, Parts: wantedParity, MissingPartsCount: 4}
	require.NoError(t, r.sendWantsThenBroadcastHaves(up, wants))
	advert := (<-downstream.sent).Message.(*protoprop.HaveParts)
	require.Len(t, advert.Parts, 4)
	r.handleWants(downstream.id, wants)

	// Other paths supply all originals before the requested parity arrives.
	for i := uint32(0); i < ops.Total(); i++ {
		r.handleRecoveryPart(r.self, &proptypes.RecoveryPart{Height: 1, Round: 0, Index: i, Data: ops.GetPart(int(i)).Bytes})
	}
	_, combined, _, found := r.getAllState(1, 0, false)
	require.True(t, found)
	require.True(t, combined.IsComplete())
	require.Len(t, r.partChan, int(ops.Total()))

	// Even the later arrival of every requested parity part must eventually
	// satisfy the downstream request previously induced by our own haves.
	for i := uint32(0); i < eps.Total(); i++ {
		r.handleRecoveryPart(upstream.id, &proptypes.RecoveryPart{Height: 1, Round: 0, Index: ops.Total() + i, Data: eps.GetPart(int(i)).Bytes})
	}
	got := make(map[uint32]bool)
	require.Eventually(t, func() bool {
		for {
			select {
			case envelope := <-downstream.sent:
				if part, ok := envelope.Message.(*protoprop.RecoveryPart); ok && part.Index >= ops.Total() {
					got[part.Index] = true
				}
			default:
				return len(got) == 4
			}
		}
	}, time.Second, time.Millisecond, "relay advertised parity but never serves it after completing its originals")
}

func TestGenerateParityAfterMixedRecovery(t *testing.T) {
	r, cb, ops, eps := newParityTestReactor(t, bytes.Repeat([]byte{17}, 3*int(types.BlockPartSizeBytes)+1001))
	peer, _ := addParityTestPeer(t, r, "downstream")
	for _, index := range []uint32{0, 1, 4, 5} {
		part := ops.GetPart(int(index))
		if index >= ops.Total() {
			part = eps.GetPart(int(index - ops.Total()))
		}
		r.handleRecoveryPart(r.self, &proptypes.RecoveryPart{Height: 1, Round: 0, Index: index, Data: part.Bytes})
	}
	require.Len(t, r.partChan, int(ops.Total()))
	_, combined, _, _ := r.getAllState(1, 0, false)
	require.True(t, combined.IsComplete())

	var calls sync.WaitGroup
	for range 20 {
		calls.Go(func() { r.generateParity(cb, combined) })
	}
	calls.Wait()
	require.Eventually(t, func() bool { return combined.Parity().IsComplete() }, time.Second, time.Millisecond)
	for i := uint32(0); i < eps.Total(); i++ {
		part, ok := combined.GetPart(ops.Total() + i)
		require.True(t, ok)
		require.Equal(t, eps.GetPartBytes(int(i)), part.Bytes.Bytes())
	}
	fullHaves := 0
	require.Eventually(t, func() bool {
		for {
			select {
			case e := <-peer.sent:
				if haves, ok := e.Message.(*protoprop.HaveParts); ok && len(haves.Parts) == int(combined.Total()) {
					fullHaves++
				}
			default:
				return fullHaves > 0
			}
		}
	}, time.Second, time.Millisecond)
	require.Equal(t, 1, fullHaves)
}

func TestGenerateParityAfterMempoolRecovery(t *testing.T) {
	tx := types.Tx(bytes.Repeat([]byte{17}, 4*int(types.BlockPartSizeBytes)-4))
	framed := proptypes.NewUnmarshalledTx(proptypes.TxMetaData{}, tx.Key(), tx).Bytes()
	r, cb, ops, eps := newParityTestReactor(t, framed)
	cb.Blobs = []proptypes.TxMetaData{{Start: 0, End: uint32(len(framed)), Hash: tx.Hash()}}
	r.mempool = &mockMempool{txs: map[types.TxKey]*types.CachedTx{tx.Key(): {Tx: tx}}}
	r.recoverPartsFromMempool(cb)
	require.Len(t, r.partChan, int(ops.Total()))
	_, combined, _, _ := r.getAllState(1, 0, false)
	require.True(t, combined.IsComplete())
	require.Eventually(t, func() bool { return combined.Parity().IsComplete() }, time.Second, time.Millisecond)
	require.Equal(t, eps.Hash(), combined.Parity().Hash())
}

func TestPublishParityDropsStaleResults(t *testing.T) {
	for _, change := range []string{"prune", "replacement", "shutdown"} {
		t.Run(change, func(t *testing.T) {
			r, cb, ops, eps := newParityTestReactor(t, bytes.Repeat([]byte{17}, int(types.BlockPartSizeBytes)+1001))
			peer, _ := addParityTestPeer(t, r, "downstream")
			_, combined, _, _ := r.getAllState(1, 0, false)
			for i := uint32(0); i < ops.Total(); i++ {
				_, err := combined.AddOriginalPart(ops.GetPart(int(i)))
				require.NoError(t, err)
			}
			switch change {
			case "prune":
				r.prune(2)
			case "replacement":
				r.DeleteRound(1, 0)
				require.True(t, r.AddProposal(cb))
			case "shutdown":
				r.cancel()
			}
			r.publishParity(cb, combined, eps)
			require.Zero(t, combined.Parity().Count())
			require.Empty(t, peer.sent)
		})
	}
}

func TestParityEncodingDoesNotHoldConsensusLocks(t *testing.T) {
	r, cb, ops, _ := newParityTestReactor(t, bytes.Repeat([]byte{17}, int(types.BlockPartSizeBytes)+1001))
	_, combined, _, _ := r.getAllState(1, 0, false)
	for i := uint32(0); i < ops.Total(); i++ {
		_, err := combined.AddOriginalPart(ops.GetPart(int(i)))
		require.NoError(t, err)
	}
	encoding := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() { close(release); <-finished }()
	r.generateParityWith(cb, combined, func(original *types.PartSet, size uint32) (*types.PartSet, int, error) {
		close(encoding)
		defer close(finished)
		<-release
		return types.Encode(original, size)
	})
	select {
	case <-encoding:
	case <-time.After(time.Second):
		t.Fatal("parity encoding did not start")
	}
	accessible := make(chan struct{})
	go func() {
		defer close(accessible)
		_, parts, _, _ := r.getAllState(1, 0, false)
		_ = parts.IsComplete()
		_ = parts.Original().GetPart(0)
	}()
	select {
	case <-accessible:
	case <-time.After(time.Second):
		t.Fatal("parity encoding blocked access to completed originals")
	}
	// A duplicate completion cannot start another encoder while one is active.
	r.generateParityWith(cb, combined, func(*types.PartSet, uint32) (*types.PartSet, int, error) {
		t.Error("duplicate parity generation")
		return nil, 0, nil
	})
}

type pausedParityPeer struct {
	*parityRecordingPeer
	entered chan struct{}
	resume  chan struct{}
}

func (p *pausedParityPeer) TrySend(e p2p.Envelope) bool {
	if part, ok := e.Message.(*protoprop.RecoveryPart); ok && part.Index == 0 {
		close(p.entered)
		<-p.resume
	}
	return p.parityRecordingPeer.TrySend(e)
}

func TestParityPublicationDuringWantRegistration(t *testing.T) {
	r, cb, ops, eps := newParityTestReactor(t, bytes.Repeat([]byte{17}, 4*int(types.BlockPartSizeBytes)))
	_, combined, _, _ := r.getAllState(1, 0, false)
	for i := uint32(0); i < ops.Total(); i++ {
		_, err := combined.AddOriginalPart(ops.GetPart(int(i)))
		require.NoError(t, err)
	}
	peer := &pausedParityPeer{
		parityRecordingPeer: &parityRecordingPeer{id: "downstream", sent: make(chan p2p.Envelope, 100)},
		entered:             make(chan struct{}), resume: make(chan struct{}),
	}
	state := newPeerState(context.Background(), peer, log.NewNopLogger())
	r.setPeer(peer.id, state)
	t.Cleanup(state.cancel)
	wanted := bits.NewBitArray(int(combined.Total()))
	wanted.SetIndex(0, true)
	wanted.SetIndex(int(ops.Total()), true)
	wants := &proptypes.WantParts{Height: 1, Round: 0, Parts: wanted, MissingPartsCount: 2}
	require.NoError(t, wants.ValidateBasic())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.handleWants(peer.id, wants)
	}()
	select {
	case <-peer.entered:
	case <-time.After(time.Second):
		t.Fatal("handleWants never reached original send")
	}
	// This is the worker's publication path, after canSend was snapshotted
	// and before handleWants registers the missing parity request.
	published := make(chan struct{})
	go func() {
		defer close(published)
		r.publishParity(cb, combined, eps)
	}()
	require.Eventually(t, func() bool { return combined.HasPart(int(ops.Total())) }, time.Second, time.Millisecond)
	// Publication must wait for the paused handler to register missing wants.
	select {
	case <-published:
		t.Fatal("publication completed before missing wants were registered")
	case <-time.After(10 * time.Millisecond):
	}
	close(peer.resume)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleWants did not finish")
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("parity publication did not finish")
	}
	require.False(t, state.WantsPart(1, 0, ops.Total()))
	require.Zero(t, state.GetRemainingRequests(1, 0))
	var sent []uint32
	for len(peer.sent) > 0 {
		if part, ok := (<-peer.sent).Message.(*protoprop.RecoveryPart); ok {
			sent = append(sent, part.Index)
		}
	}
	require.Equal(t, []uint32{0, ops.Total()}, sent)
}

func TestPartDeliveryConsumesQuotaOnce(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent publishers", true: "direct send"}[direct], func(t *testing.T) {
			r, _, ops, _ := newParityTestReactor(t, bytes.Repeat([]byte{17}, 2*int(types.BlockPartSizeBytes)))
			peer, state := addParityTestPeer(t, r, "downstream")
			_, combined, _, _ := r.getAllState(1, 0, false)
			for i := uint32(0); i < ops.Total(); i++ {
				_, err := combined.AddOriginalPart(ops.GetPart(int(i)))
				require.NoError(t, err)
			}
			pending := bits.NewBitArray(int(combined.Total()))
			pending.SetIndex(0, true)
			pending.SetIndex(1, true)
			state.AddWants(1, 0, pending)
			state.SetRemainingRequests(1, 0, 2)
			if direct {
				requested := bits.NewBitArray(int(combined.Total()))
				requested.SetIndex(0, true)
				r.handleWants(peer.id, &proptypes.WantParts{Height: 1, Round: 0, Parts: requested, MissingPartsCount: 2})
			}
			part := ops.GetPart(0)
			var deliveries sync.WaitGroup
			for range 20 {
				deliveries.Go(func() {
					r.clearWants(&proptypes.RecoveryPart{Height: 1, Round: 0, Index: 0, Data: part.Bytes}, part.Proof)
				})
			}
			deliveries.Wait()
			require.False(t, state.WantsPart(1, 0, 0))
			require.Equal(t, 1, state.GetRemainingRequests(1, 0))
			require.Len(t, peer.sent, 1)
			part = ops.GetPart(1)
			r.clearWants(&proptypes.RecoveryPart{Height: 1, Round: 0, Index: 1, Data: part.Bytes}, part.Proof)
			require.Zero(t, state.GetRemainingRequests(1, 0))
			require.Len(t, peer.sent, 2)
		})
	}
}
