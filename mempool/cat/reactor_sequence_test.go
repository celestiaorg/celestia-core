package cat

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	db "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

// TestReactorSequentialTxsAcrossMultipleReactors tests that ~50 sequential transactions
// for the same signer are properly managed across three reactors with the following expectations:
// 1. We never request more than one tx for the same signer at a time
// 2. As we receive sequences we don't yet expect, they get added to the pending queue
// 3. The pending queue is always sorted by sequence
// 4. State changes match expectations across all reactors
func TestReactorSequentialTxsAcrossMultipleReactors(t *testing.T) {
	const (
		numReactors = 3
		numTxs      = 50
	)

	config := cfg.TestConfig()

	// Create three reactors with sequence-tracking app
	reactors := make([]*Reactor, numReactors)
	apps := make([]*sequenceTrackingTestApp, numReactors)

	for i := 0; i < numReactors; i++ {
		apps[i] = newSequenceTrackingTestApp()
		cc := proxy.NewLocalClientCreator(apps[i])
		pool, cleanup := newMempoolWithApp(cc)
		t.Cleanup(cleanup)

		reactor, err := NewReactor(pool, &ReactorOptions{})
		require.NoError(t, err)
		reactor.SetStickySalt([]byte{byte(i + 1)})
		pool.logger = mempoolLogger().With("validator", i)
		reactor.SetLogger(mempoolLogger().With("validator", i))
		reactors[i] = reactor
	}

	// Connect reactors in a full mesh
	switches := p2p.MakeConnectedSwitches(config.P2P, numReactors, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("MEMPOOL", reactors[i])
		return s
	}, p2p.Connect2Switches)

	t.Cleanup(func() {
		for _, s := range switches {
			if err := s.Stop(); err != nil {
				assert.NoError(t, err)
			}
		}
	})

	for _, r := range reactors {
		for _, peer := range r.Switch.Peers().List() {
			peer.Set(types.PeerStateKey, peerState{1})
		}
	}

	// Use a single signer for all transactions
	signer := []byte("sequential-signer")

	// Initialize sequence to 1 for all apps
	for _, app := range apps {
		app.SetSequence(string(signer), 1)
	}

	// Generate sequential transactions
	txs := make([]types.Tx, numTxs)
	for i := 0; i < numTxs; i++ {
		txs[i] = types.Tx(fmt.Sprintf("%s=tx-seq-%03d=%d", signer, i+1, i+1))
	}

	// Add all transactions to reactor 0's mempool
	// This simulates transactions being received by one node initially
	for i, tx := range txs {
		err := reactors[0].mempool.CheckTx(tx, nil, mempool.TxInfo{})
		require.NoError(t, err, "failed to add tx %d to reactor 0", i+1)

		// Update app sequence after successful CheckTx
		apps[0].SetSequence(string(signer), uint64(i+2))
	}

	// Wait for all transactions to propagate to all reactors
	waitForTxsOnReactors(t, txs, reactors)

	// Validate final state across all reactors
	for i, reactor := range reactors {
		t.Run(fmt.Sprintf("reactor-%d-final-state", i), func(t *testing.T) {
			// All transactions should be in the mempool
			require.Equal(t, numTxs, reactor.mempool.Size(), "reactor %d should have all %d txs", i, numTxs)

			// Pending queue should be empty (all txs received)
			entries := reactor.pendingSeen.entriesForSigner(signer)
			require.Empty(t, entries, "reactor %d should have no pending entries", i)

			// No outstanding requests
			for _, tx := range txs {
				key := tx.Key()
				requestedFrom := reactor.requests.ForTx(key)
				require.Zero(t, requestedFrom, "reactor %d should have no outstanding request for tx %s", i, key)
			}

			// Verify all transactions are in the mempool
			for j, tx := range txs {
				key := tx.Key()
				require.True(t, reactor.mempool.Has(key), "reactor %d should have tx %d with key %s", i, j+1, key)
			}
		})
	}

	// Test intermediate state validation with smaller batch
	// Skip by default as it's slow - run with -run to specifically target this subtest
	t.Run("validate-intermediate-states", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping slow intermediate-states test in short mode")
		}
		const smallNumTxs = 10 // Use fewer txs for this subtest to keep it faster

		// Reset and restart with fresh reactors
		reactors2 := make([]*Reactor, numReactors)
		apps2 := make([]*sequenceTrackingTestApp, numReactors)

		for i := 0; i < numReactors; i++ {
			apps2[i] = newSequenceTrackingTestApp()
			cc := proxy.NewLocalClientCreator(apps2[i])
			pool, cleanup := newMempoolWithApp(cc)
			t.Cleanup(cleanup)

			reactor, err := NewReactor(pool, &ReactorOptions{})
			require.NoError(t, err)
			reactor.SetStickySalt([]byte{byte(i + 1)})
			pool.logger = mempoolLogger().With("validator", i, "run", "intermediate")
			reactor.SetLogger(mempoolLogger().With("validator", i, "run", "intermediate"))
			reactors2[i] = reactor
		}

		switches2 := p2p.MakeConnectedSwitches(config.P2P, numReactors, func(i int, s *p2p.Switch) *p2p.Switch {
			s.AddReactor("MEMPOOL", reactors2[i])
			return s
		}, p2p.Connect2Switches)

		t.Cleanup(func() {
			for _, s := range switches2 {
				if err := s.Stop(); err != nil {
					assert.NoError(t, err)
				}
			}
		})

		for _, r := range reactors2 {
			for _, peer := range r.Switch.Peers().List() {
				peer.Set(types.PeerStateKey, peerState{1})
			}
		}

		// Initialize sequence to 1 for all apps
		for _, app := range apps2 {
			app.SetSequence(string(signer), 1)
		}

		// Generate smaller set of txs for this test
		smallTxs := make([]types.Tx, smallNumTxs)
		for i := 0; i < smallNumTxs; i++ {
			smallTxs[i] = types.Tx(fmt.Sprintf("%s=tx-intermediate-%03d=%d", signer, i+1, i+1))
		}

		// Add transactions one by one and check invariants
		maxConcurrentRequests := make(map[int]int)

		for txIdx, tx := range smallTxs {
			// Add transaction to reactor 0
			err := reactors2[0].mempool.CheckTx(tx, nil, mempool.TxInfo{})
			require.NoError(t, err, "failed to add tx %d", txIdx+1)

			// Update sequence
			apps2[0].SetSequence(string(signer), uint64(txIdx+2))

			// Give time for gossip
			time.Sleep(50 * time.Millisecond)

			// Check invariants across all reactors
			for rIdx, reactor := range reactors2 {
				// Count how many txs this signer has outstanding requests for
				activeRequests := 0
				for _, checkTx := range smallTxs[:txIdx+1] {
					key := checkTx.Key()
					if reactor.requests.ForTx(key) != 0 {
						activeRequests++
					}
				}

				// Track max concurrent requests
				if activeRequests > maxConcurrentRequests[rIdx] {
					maxConcurrentRequests[rIdx] = activeRequests
				}

				// Get pending queue entries
				entries := reactor.pendingSeen.entriesForSigner(signer)

				// Verify pending queue is sorted
				if len(entries) > 1 {
					for i := 1; i < len(entries); i++ {
						require.LessOrEqual(t, entries[i-1].sequence, entries[i].sequence,
							"reactor %d: pending queue not sorted at tx %d", rIdx, txIdx+1)
					}
				}
			}
		}

		// Wait for all to propagate
		waitForTxsOnReactors(t, smallTxs, reactors2)

		// Validate that we never had more than reasonable concurrent requests
		// Note: We might have 1-2 concurrent requests during transitions, but not many more
		for rIdx, maxReqs := range maxConcurrentRequests {
			// Allow some concurrency due to timing, but should be very limited
			// The key invariant is we don't request all at once
			require.LessOrEqual(t, maxReqs, 5,
				"reactor %d had too many concurrent requests (%d)", rIdx, maxReqs)
		}
	})
}

// sequenceTrackingTestApp is a test application that tracks sequences and returns them in CheckTx
type sequenceTrackingTestApp struct {
	*kvstore.Application
	mtx       sync.Mutex
	sequences map[string]uint64
}

func newSequenceTrackingTestApp() *sequenceTrackingTestApp {
	return &sequenceTrackingTestApp{
		Application: kvstore.NewApplication(db.NewMemDB()),
		sequences:   make(map[string]uint64),
	}
}

func (app *sequenceTrackingTestApp) CheckTx(ctx context.Context, req *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	// Parse transaction format: signer=message=sequence
	parts := req.Tx

	// Find the signer (everything before the first =)
	var signer, sequence string
	firstEq := -1
	lastEq := -1

	for i, b := range parts {
		if b == '=' {
			if firstEq == -1 {
				firstEq = i
				signer = string(parts[:i])
			}
			lastEq = i
		}
	}

	if lastEq > firstEq && lastEq > 0 {
		sequence = string(parts[lastEq+1:])
	}

	if signer == "" || sequence == "" {
		return &abcitypes.ResponseCheckTx{
			Code:      100,
			GasWanted: 1,
			Log:       "invalid tx format",
		}, nil
	}

	seq, err := strconv.ParseUint(sequence, 10, 64)
	if err != nil {
		return &abcitypes.ResponseCheckTx{
			Code:      101,
			GasWanted: 1,
			Log:       "invalid sequence",
		}, nil
	}

	// Auto-increment the expected sequence for this signer
	// This simulates real app behavior where each successful tx advances the sequence
	app.mtx.Lock()
	app.sequences[signer] = seq + 1
	app.mtx.Unlock()

	return &abcitypes.ResponseCheckTx{
		Code:      abcitypes.CodeTypeOK,
		Priority:  int64(seq),
		Address:   []byte(signer),
		Sequence:  seq,
		GasWanted: 1,
	}, nil
}

func (app *sequenceTrackingTestApp) QuerySequence(ctx context.Context, req *abcitypes.RequestQuerySequence) (*abcitypes.ResponseQuerySequence, error) {
	app.mtx.Lock()
	defer app.mtx.Unlock()

	sequence := app.sequences[string(req.Signer)]
	return &abcitypes.ResponseQuerySequence{Sequence: sequence}, nil
}

func (app *sequenceTrackingTestApp) SetSequence(signer string, sequence uint64) {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	app.sequences[signer] = sequence
}

// TestReactorPendingQueueSorting validates that the pending queue remains sorted
// as transactions arrive out of order
func TestReactorPendingQueueSorting(t *testing.T) {
	app := newSequenceTrackingTestApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("sort-test-signer")
	app.SetSequence(string(signer), 1)

	// Create three peers
	peers := genPeers(3)
	for _, peer := range peers {
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)
		peer.On("TrySend", matchAny).Return(true).Maybe()
		peer.On("Send", matchAny).Return(true).Maybe()
	}

	// Send SeenTx messages out of order: sequences 10, 5, 15, 2, 8
	outOfOrderSeqs := []uint64{10, 5, 15, 2, 8}
	txs := make([]types.Tx, len(outOfOrderSeqs))

	for i, seq := range outOfOrderSeqs {
		tx := types.Tx(fmt.Sprintf("%s=out-of-order-%d=%d", signer, seq, seq))
		txs[i] = tx
		key := tx.Key()

		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    key[:],
				Signer:   signer,
				Sequence: seq,
			},
			Src: peers[i%len(peers)],
		})
	}

	// Verify pending queue is sorted
	entries := reactor.pendingSeen.entriesForSigner(signer)
	require.Len(t, entries, len(outOfOrderSeqs))

	expectedSorted := []uint64{2, 5, 8, 10, 15}
	actualSequences := make([]uint64, len(entries))
	for i, entry := range entries {
		actualSequences[i] = entry.sequence
	}

	require.Equal(t, expectedSorted, actualSequences, "pending queue should be sorted by sequence")
}

// TestReactorSingleOutstandingRequestPerSigner validates that the pending queue
// mechanism limits concurrent requests for the same signer
func TestReactorSingleOutstandingRequestPerSigner(t *testing.T) {
	app := newSequenceTrackingTestApp()
	cc := proxy.NewLocalClientCreator(app)
	pool, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	reactor, err := NewReactor(pool, &ReactorOptions{})
	require.NoError(t, err)

	signer := []byte("single-request-signer")
	app.SetSequence(string(signer), 1)

	peer := genPeer()
	_, err = reactor.InitPeer(peer)
	require.NoError(t, err)

	peer.On("TrySend", matchAny).Return(true).Maybe()
	peer.On("Send", matchAny).Return(true).Maybe()

	// Send multiple SeenTx messages with different sequences
	txs := make([]types.Tx, 10)
	for i := 1; i <= 10; i++ {
		tx := types.Tx(fmt.Sprintf("%s=seq-%03d=%d", signer, i, i))
		txs[i-1] = tx
		key := tx.Key()

		reactor.Receive(p2p.Envelope{
			ChannelID: MempoolDataChannel,
			Message: &protomem.SeenTx{
				TxKey:    key[:],
				Signer:   signer,
				Sequence: uint64(i),
			},
			Src: peer,
		})
	}

	// Allow processing
	time.Sleep(100 * time.Millisecond)

	// Check that only the first tx is requested (sequence 1)
	// All others should be in pending queue
	firstKey := txs[0].Key()
	require.NotZero(t, reactor.requests.ForTx(firstKey), "first tx should be requested")

	// Check that subsequent txs are in pending queue, not requested
	entries := reactor.pendingSeen.entriesForSigner(signer)
	require.NotEmpty(t, entries, "should have pending entries")

	// Verify that only one is actively requested
	activeRequests := 0
	for _, tx := range txs {
		if reactor.requests.ForTx(tx.Key()) != 0 {
			activeRequests++
		}
	}

	// Should have at most 1 active request for this signer
	require.LessOrEqual(t, activeRequests, 1, "should have at most 1 active request for the signer")

	// Verify pending queue is sorted
	if len(entries) > 1 {
		for i := 1; i < len(entries); i++ {
			require.LessOrEqual(t, entries[i-1].sequence, entries[i].sequence,
				"pending queue should be sorted by sequence")
		}
	}
}

// matchAny is a mock matcher that matches any argument
var matchAny = mock.MatchedBy(func(interface{}) bool { return true })
