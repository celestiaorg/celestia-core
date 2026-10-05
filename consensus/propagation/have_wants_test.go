package propagation

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestInvalidHavePartHash(t *testing.T) {
	t.Skip("skipping TestInvalidHavePartHash until the issue is fixed")
	p2pCfg := cfg.DefaultP2PConfig()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r1, r2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	prop, ps, _, metaData := createTestProposal(t, sm, pv, 1, 0, 2, 1000000)
	parityBlock, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	partHashes := extractHashes(ps, parityBlock)
	proofs := extractProofs(ps, parityBlock)
	cb := &proptypes.CompactBlock{
		Proposal:    *prop,
		LastLen:     uint32(lastLen),
		Signature:   cmtrand.Bytes(64), // todo: sign the proposal with a real signature
		BpHash:      parityBlock.Hash(),
		Blobs:       metaData,
		PartsHashes: partHashes,
	}
	cb.SetProofCache(proofs)

	added := r1.AddProposal(cb)
	require.True(t, added)

	// make sure the peers are connected
	p1 := r1.getPeer(r2.self)
	require.NotNil(t, p1)

	// send a valid have
	haves := &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts:  []proptypes.PartMetaData{{Index: 0, Hash: partHashes[0]}},
	}
	r1.handleHaves(r2.self, haves)
	time.Sleep(100 * time.Millisecond)

	// make sure r1 processed the have and is still connected
	p1 = r1.getPeer(r2.self)
	require.NotNil(t, p1)
	p1Haves, has := p1.GetHaves(prop.Height, prop.Round)
	require.True(t, has)
	assert.True(t, p1Haves.GetIndex(0))

	// send an invalid have
	haves = &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts:  []proptypes.PartMetaData{{Index: 1, Hash: []byte{0x01}}},
	}
	r1.handleHaves(r2.self, haves)
	time.Sleep(100 * time.Millisecond)

	// make sure r1 disconnected from r2
	p1 = r1.getPeer(r2.self)
	assert.Nil(t, p1)
}

// TestHandleHavesDoesNotBlock verifies that handleHaves does not block the
// calling goroutine when the peer's receivedHaves channel is full. This is a
// regression test for a vulnerability where a missing default case in the
// select statement could allow a malicious peer to halt the propagation
// reactor's message-processing goroutine.
func TestHandleHavesDoesNotBlock(t *testing.T) {
	p2pCfg := cfg.DefaultP2PConfig()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r1, r2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	prop, ps, _, metaData := createTestProposal(t, sm, pv, 1, 0, 2, 1000000)
	parityBlock, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	partHashes := extractHashes(ps, parityBlock)
	proofs := extractProofs(ps, parityBlock)
	cb := &proptypes.CompactBlock{
		Proposal:    *prop,
		LastLen:     uint32(lastLen),
		Signature:   cmtrand.Bytes(64),
		BpHash:      parityBlock.Hash(),
		Blobs:       metaData,
		PartsHashes: partHashes,
	}
	cb.SetProofCache(proofs)

	added := r1.AddProposal(cb)
	require.True(t, added)

	p1 := r1.getPeer(r2.self)
	require.NotNil(t, p1)

	// Fill the peer's receivedHaves channel to capacity.
	for i := 0; i < cap(p1.receivedHaves); i++ {
		p1.receivedHaves <- request{height: 1, round: 0, index: uint32(i % len(partHashes))}
	}
	require.Equal(t, cap(p1.receivedHaves), len(p1.receivedHaves))

	// Call handleHaves from a goroutine. With the default case, this should
	// return almost immediately. Without it, this would block indefinitely.
	done := make(chan struct{})
	go func() {
		defer close(done)
		haves := &proptypes.HaveParts{
			Height: prop.Height,
			Round:  prop.Round,
			Parts:  []proptypes.PartMetaData{{Index: 0, Hash: partHashes[0]}},
		}
		r1.handleHaves(r2.self, haves)
	}()

	select {
	case <-done:
		// handleHaves returned without blocking.
	case <-time.After(time.Second):
		t.Fatal("handleHaves blocked when receivedHaves channel was full")
	}
}

func TestCountRemainingParts(t *testing.T) {
	tests := []struct {
		name           string
		totalParts     int
		existingParts  int
		expectedResult int32
	}{
		{
			name:           "Exactly half parts - should need one more",
			totalParts:     10,
			existingParts:  5,
			expectedResult: 0,
		},
		{
			name:           "More than threshold - should need 0",
			totalParts:     10,
			existingParts:  7,
			expectedResult: 0,
		},
		{
			name:           "Exactly threshold - should need 0",
			totalParts:     8,
			existingParts:  4,
			expectedResult: 0,
		},
		{
			name:           "Zero existing parts - need full threshold",
			totalParts:     8,
			existingParts:  0,
			expectedResult: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countRemainingParts(tt.totalParts, tt.existingParts)
			if got != tt.expectedResult {
				t.Errorf("countRemainingParts(%d, %d) = %d; want %d",
					tt.totalParts, tt.existingParts, got, tt.expectedResult)
			}
		})
	}
}

// countingPeer records every RecoveryPart sent to it on the data channel.
type countingPeer struct {
	p2p.Peer
	mtx  sync.Mutex
	sent map[uint32]int
}

func (c *countingPeer) TrySend(e p2p.Envelope) bool {
	if rp, ok := e.Message.(*propproto.RecoveryPart); ok && e.ChannelID == DataChannel {
		c.mtx.Lock()
		c.sent[rp.Index]++
		c.mtx.Unlock()
	}
	return true
}

// TestHandleWantsReplay verifies that replayed WantParts do not make the node
// resend parts it already served to the same peer.
func TestHandleWantsReplay(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	r1, r2 := reactors[0], reactors[1]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	// swap in the counting peer first so r2 never hears about the proposal
	// and only the wants sent below reach r1.
	ps := r1.getPeer(r2.self)
	require.NotNil(t, ps)
	cp := &countingPeer{Peer: ps.peer, sent: make(map[uint32]int)}
	ps.peer = cp

	prop, partSet, _, metaData := createTestProposal(t, sm, pv, 1, 0, 100, 1000)
	require.NoError(t, r1.ProposeBlock(prop, partSet, metaData))

	_, parts, _, has := r1.getAllState(prop.Height, prop.Round, false)
	require.True(t, has)
	// combined part set: original plus parity parts
	total := int(parts.Total())

	want := bits.NewBitArray(total)
	want.Fill()
	sendWants := func(prove bool, replays int) {
		for i := 0; i < replays; i++ {
			r1.handleWants(r2.self, &proptypes.WantParts{
				Parts:             want,
				Height:            prop.Height,
				Round:             prop.Round,
				Prove:             prove,
				MissingPartsCount: int32(total),
			})
		}
	}

	sendWants(false, 100)
	for i := 0; i < total; i++ {
		assert.Equal(t, 1, cp.sent[uint32(i)], "part %d", i)
	}

	// A request that needs proofs is served once more, then deduplicated too.
	sendWants(true, 100)
	for i := 0; i < total; i++ {
		assert.Equal(t, 2, cp.sent[uint32(i)], "part %d", i)
	}
}
