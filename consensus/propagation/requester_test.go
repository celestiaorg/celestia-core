package propagation

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/mock"
	"github.com/tendermint/tendermint/types"
)

func TestRequester_SendRequest(t *testing.T) {
	logger := log.NewNopLogger()
	r := newRequester(logger)

	peer := mock.NewPeer(nil)

	tests := []struct {
		name              string
		setup             func()
		want              *proptypes.WantParts
		expectedSent      bool
		expectedError     error
		expectedQueueSize int
	}{
		{
			name: "successful request",
			setup: func() {
				r.perPeerRequests = make(map[p2p.ID]int)
				r.perPartRequests = make(map[int64]map[int32]map[int]int)
				r.pendingRequests = []*request{}
			},
			want: &proptypes.WantParts{
				Height: 10,
				Round:  1,
				Parts:  bits.NewBitArray(1),
				Prove:  false,
			},
			expectedSent:      true,
			expectedError:     nil,
			expectedQueueSize: 0,
		},
		{
			name: "per-peer limit reached - sending to pending queue",
			setup: func() {
				r.perPeerRequests[peer.ID()] = concurrentPerPeerRequestLimit
			},
			want: &proptypes.WantParts{
				Height: 10,
				Round:  1,
				Parts:  bits.NewBitArray(1),
				Prove:  false,
			},
			expectedSent:      true,
			expectedError:     nil,
			expectedQueueSize: 1,
		},
		{
			name: "per-part limit reached - sending to pending queue",
			setup: func() {
				r.perPartRequests[10] = map[int32]map[int]int{
					1: {0: maxRequestsPerPart},
				}
			},
			want: func() *proptypes.WantParts {
				bitArray := bits.NewBitArray(1)
				bitArray.SetIndex(0, true)
				return &proptypes.WantParts{
					Height: 10,
					Round:  1,
					Parts:  bitArray,
					Prove:  false,
				}
			}(),
			expectedSent:      true,
			expectedError:     nil,
			expectedQueueSize: 1,
		},
		{
			name: "too many pending requests",
			setup: func() {
				r.pendingRequests = make([]*request, maxNumberOfPendingRequests)
			},
			want: &proptypes.WantParts{
				Height: 10,
				Round:  1,
				Parts:  bits.NewBitArray(1),
				Prove:  false,
			},
			expectedSent:      false,
			expectedError:     errors.New("too many pending requests"),
			expectedQueueSize: maxNumberOfPendingRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			sent, err := r.sendRequest(peer, tt.want)

			assert.Equal(t, tt.expectedSent, sent)
			if tt.expectedError != nil {
				assert.EqualError(t, err, tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
			}
			assert.GreaterOrEqual(t, len(r.pendingRequests), tt.expectedQueueSize)
		})
	}
}

func TestReactorMaxConcurrentPerPeerRequests(t *testing.T) {
	reactors, _ := testBlockPropReactors(10, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	cb, originalPs, _, _ := testCompactBlock(t, 30_000_000, 10, 1)

	// add the compact block to all reactors
	for _, reactor := range reactors {
		added, _, _ := reactor.AddProposal(cb)
		require.True(t, added)
	}

	for i := 0; i < concurrentPerPeerRequestLimit+2; i++ {
		reactor1.handleHaves(reactor2.self, &proptypes.HaveParts{
			Height: 10,
			Round:  1,
			Parts: []proptypes.PartMetaData{
				{
					Index: uint32(i),
					Hash:  originalPs.GetPart(i).Proof.LeafHash,
				},
			},
		}, false)
	}
	time.Sleep(300 * time.Millisecond)
	// check that reactor 2 only received concurrentPerPeerRequestLimit of wants
	bitArray, has := reactor2.getPeer(reactor1.self).GetWants(10, 1)
	require.True(t, has)
	assert.Equal(t, concurrentPerPeerRequestLimit, len(bitArray.GetTrueIndices()))
}

func TestReactorMaxConcurrentPerPartRequests(t *testing.T) {
	reactors, _ := testBlockPropReactors(maxRequestsPerPart+2, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]

	cb, originalPs, _, _ := testCompactBlock(t, 1000, 10, 1)

	// add the compact block to all reactors
	for _, reactor := range reactors {
		added, _, _ := reactor.AddProposal(cb)
		require.True(t, added)
	}

	for i := 1; i <= maxRequestsPerPart+1; i++ {
		reactor1.handleHaves(reactors[i].self, &proptypes.HaveParts{
			Height: 10,
			Round:  1,
			Parts: []proptypes.PartMetaData{
				{
					Index: uint32(0),
					Hash:  originalPs.GetPart(0).Proof.LeafHash,
				},
			},
		}, false)
	}
	time.Sleep(500 * time.Millisecond)
	// check that only maxRequestsPerPart number of reactors received a want
	count := 0
	for _, reactor := range reactors {
		peerState := reactor.getPeer(reactor1.self)
		if peerState == nil {
			continue
		}
		bitArray, has := peerState.GetWants(10, 1)
		require.True(t, has)
		if bitArray.GetIndex(0) {
			count++
		}
	}
	assert.Equal(t, maxRequestsPerPart, count)
}

func TestExpiredRequest(t *testing.T) {
	logger := log.NewNopLogger()
	r := newRequester(logger)
	peer1 := mock.NewPeer(nil)
	peer2 := mock.NewPeer(nil)

	// add few expired requests and a valid few
	r.pendingRequests = []*request{
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now().Add(-time.Hour),
		},
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now().Add(-requestTimeout * 2),
		},
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now(),
		},
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now().Add(-requestTimeout * 3),
		},
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now().Add(requestTimeout / 2),
		},
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now().Add(requestTimeout / 3),
		},
		{
			want:       nil,
			targetPeer: peer1,
			timestamp:  time.Now().Add(-requestTimeout * 4),
		},
	}

	r.sendNextRequest(peer2)

	assert.Equal(t, 3, len(r.pendingRequests))
}

// testCompactBlock returns a test compact block with the corresponding orignal part set,
// parity partset, and proofs.
// TODO remove after merging https://github.com/celestiaorg/celestia-core/pull/1685 as this method
// is already added there.
func testCompactBlock(t *testing.T, size int, height int64, round int32) (*proptypes.CompactBlock, *types.PartSet, *types.PartSet, []*merkle.Proof) {
	ps := types.NewPartSetFromData(cmtrand.Bytes(size), types.BlockPartSizeBytes)
	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	psh := ps.Header()
	pseh := pse.Header()

	hashes := extractHashes(ps, pse)
	proofs := extractProofs(ps, pse)

	baseCompactBlock := &proptypes.CompactBlock{
		BpHash:    pseh.Hash,
		Signature: cmtrand.Bytes(64),
		LastLen:   uint32(lastLen),
		Blobs: []proptypes.TxMetaData{
			{Hash: cmtrand.Bytes(32)},
			{Hash: cmtrand.Bytes(32)},
		},
		PartsHashes: hashes,
	}

	// adding the proposal manually so the haves/wants and recovery
	// parts are not rejected.
	p := types.Proposal{
		BlockID: types.BlockID{
			Hash:          cmtrand.Bytes(32),
			PartSetHeader: psh,
		},
		Height: height,
		Round:  round,
	}
	baseCompactBlock.Proposal = p

	return baseCompactBlock, ps, pse, proofs
}
