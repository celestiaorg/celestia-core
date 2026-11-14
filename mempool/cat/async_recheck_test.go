package cat

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/proxy"
	cmttypes "github.com/cometbft/cometbft/types"
)

// checkStateApp models an SDK app's check state: Commit resets it to the committed
// sequences and recheck replays pending txs in order. Txs are "signer/seq".
type checkStateApp struct {
	abci.BaseApplication

	mtx       sync.Mutex
	committed map[string]uint64
	check     map[string]uint64

	recheckGate  chan struct{} // if set, each recheck waits for a token
	inflight     atomic.Int32
	rechecks     atomic.Int32
	commitRaced  atomic.Bool // Commit ran while a CheckTx was in flight
	recheckDelay time.Duration
}

func newCheckStateApp() *checkStateApp {
	return &checkStateApp{committed: map[string]uint64{}, check: map[string]uint64{}}
}

func checkStateTx(signer string, seq uint64) cmttypes.Tx {
	return cmttypes.Tx(fmt.Sprintf("%s/%d", signer, seq))
}

func parseCheckStateTx(tx []byte) (string, uint64) {
	parts := strings.Split(string(tx), "/")
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		panic(err)
	}
	return parts[0], seq
}

func (app *checkStateApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	app.inflight.Add(1)
	defer app.inflight.Add(-1)
	if req.Type == abci.CheckTxType_Recheck {
		app.rechecks.Add(1)
		if app.recheckGate != nil {
			<-app.recheckGate
		}
		time.Sleep(app.recheckDelay)
	}
	signer, seq := parseCheckStateTx(req.Tx)
	app.mtx.Lock()
	defer app.mtx.Unlock()
	next, ok := app.check[signer]
	if !ok {
		next = app.committed[signer]
	}
	if seq != next {
		return &abci.ResponseCheckTx{Code: 1, Log: fmt.Sprintf("want seq %d got %d", next, seq)}, nil
	}
	app.check[signer] = next + 1
	return &abci.ResponseCheckTx{Code: abci.CodeTypeOK, GasWanted: 1, Address: []byte(signer), Sequence: seq}, nil
}

func (app *checkStateApp) QuerySequence(_ context.Context, req *abci.RequestQuerySequence) (*abci.ResponseQuerySequence, error) {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	seq, ok := app.check[string(req.Signer)]
	if !ok {
		seq = app.committed[string(req.Signer)]
	}
	return &abci.ResponseQuerySequence{Sequence: seq}, nil
}

// commit applies the block and resets the check state, like BaseApp.Commit.
func (app *checkStateApp) commit(txs []cmttypes.Tx) {
	if app.inflight.Load() != 0 {
		app.commitRaced.Store(true)
	}
	app.mtx.Lock()
	defer app.mtx.Unlock()
	for _, tx := range txs {
		signer, seq := parseCheckStateTx(tx)
		app.committed[signer] = seq + 1
	}
	app.check = map[string]uint64{}
}

func setupCheckStatePool(t testing.TB, app *checkStateApp, mutate func(*cmtcfg.MempoolConfig)) *TxPool {
	t.Helper()
	cfg := cmtcfg.TestMempoolConfig()
	cfg.AsyncRecheck = true
	if mutate != nil {
		mutate(cfg)
	}
	conn, err := proxy.NewConnSyncLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, conn.Start())
	t.Cleanup(func() { require.NoError(t, conn.Stop()) })
	return NewTxPool(log.NewNopLogger(), cfg, conn, 0)
}

// commitBlock does what BlockExecutor.Commit does: lock, ABCI Commit, Update.
func commitBlock(t testing.TB, txmp *TxPool, app *checkStateApp, height int64, txs []cmttypes.Tx) {
	t.Helper()
	txmp.Lock()
	require.NoError(t, txmp.FlushAppConn())
	app.commit(txs)
	cached := make([]*cmttypes.CachedTx, len(txs))
	for i, tx := range txs {
		cached[i] = tx.ToCachedTx()
	}
	require.NoError(t, txmp.UpdateAsyncRecheck(height, cached, abciResponses(len(txs), abci.CodeTypeOK), nil, nil))
}

func addSeqTxs(t testing.TB, txmp *TxPool, signer string, from, to uint64) {
	t.Helper()
	for s := from; s < to; s++ {
		require.NoError(t, txmp.CheckTx(checkStateTx(signer, s), nil, mempool.TxInfo{}))
	}
}

// waitForPass returns once no recheck pass holds the lock.
func waitForPass(txmp *TxPool) {
	txmp.mtx.Lock()
	txmp.mtx.Unlock() //nolint:staticcheck // empty critical section waits for the pass
}

func drain(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func keysOf(txs []*cmttypes.CachedTx) []string {
	out := make([]string, len(txs))
	for i, tx := range txs {
		out[i] = string(tx.Tx)
	}
	return out
}

// While the pass runs, a reap returns at once with only the txs the pass has
// already kept, each signer cut at its first unchecked tx; once it ends, the
// reap equals a synchronous recheck's.
func TestAsyncRecheck_ReapTakesOnlyRechecked(t *testing.T) {
	run := func(async bool) (*TxPool, *checkStateApp) {
		app := newCheckStateApp()
		txmp := setupCheckStatePool(t, app, func(c *cmtcfg.MempoolConfig) { c.AsyncRecheck = async })
		addSeqTxs(t, txmp, "a", 0, 4)
		addSeqTxs(t, txmp, "b", 0, 2)
		addSeqTxs(t, txmp, "c", 0, 2)
		if async {
			app.recheckGate = make(chan struct{})
		}
		// b/5 is not in the pool: it moves b's committed sequence, so b/0 and b/1 go stale.
		commitBlock(t, txmp, app, 1, []cmttypes.Tx{checkStateTx("a", 0), checkStateTx("b", 5)})
		return txmp, app
	}
	syncPool, _ := run(false)
	want := keysOf(syncPool.ReapMaxBytesMaxGas(-1, -1))
	require.Equal(t, []string{"a/1", "a/2", "a/3", "c/0", "c/1"}, want)

	txmp, app := run(true)
	reap := func() []string {
		reaped := make(chan []*cmttypes.CachedTx, 1)
		go func() { reaped <- txmp.ReapMaxBytesMaxGas(-1, -1) }()
		select {
		case txs := <-reaped:
			return keysOf(txs)
		case <-time.After(5 * time.Second):
			t.Fatal("reap waited for the recheck pass")
			return nil
		}
	}
	require.Empty(t, reap())
	// Pass order: a/1 a/2 a/3 b/0 b/1 c/0 c/1. Keep a/1 and a/2, hold the pass at a/3.
	app.recheckGate <- struct{}{}
	app.recheckGate <- struct{}{}
	require.Eventually(t, func() bool { return app.rechecks.Load() == 3 }, 5*time.Second, time.Millisecond)
	require.Equal(t, []string{"a/1", "a/2"}, reap())
	// a/3 kept, b/0 and b/1 evicted, pass held at c/0: b never shows up.
	for i := 0; i < 3; i++ {
		app.recheckGate <- struct{}{}
	}
	require.Eventually(t, func() bool { return app.rechecks.Load() == 6 }, 5*time.Second, time.Millisecond)
	require.Equal(t, []string{"a/1", "a/2", "a/3"}, reap())

	close(app.recheckGate)
	waitForPass(txmp)
	require.Equal(t, want, reap())
}

// A new tx is not checked until the pass has replayed the pending txs, so its
// sequence is checked against the rebuilt check state.
func TestAsyncRecheck_AdmissionWaitsForPass(t *testing.T) {
	app := newCheckStateApp()
	txmp := setupCheckStatePool(t, app, nil)
	addSeqTxs(t, txmp, "a", 0, 3)

	app.recheckGate = make(chan struct{})
	commitBlock(t, txmp, app, 1, []cmttypes.Tx{checkStateTx("a", 0)})

	added := make(chan error, 1)
	go func() { added <- txmp.CheckTx(checkStateTx("a", 3), nil, mempool.TxInfo{}) }()
	select {
	case err := <-added:
		t.Fatalf("admission did not wait for the recheck pass: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(app.recheckGate)
	require.NoError(t, <-added)
	require.Equal(t, 3, txmp.Size())
	require.True(t, txmp.Has(checkStateTx("a", 3).Key()), "tx rejected: checked against a half-rebuilt state")
}

// The next commit waits for the running pass: no CheckTx races ABCI Commit.
func TestAsyncRecheck_NextCommitWaitsForPass(t *testing.T) {
	app := newCheckStateApp()
	txmp := setupCheckStatePool(t, app, nil)
	addSeqTxs(t, txmp, "a", 0, 4)

	app.recheckGate = make(chan struct{})
	commitBlock(t, txmp, app, 1, []cmttypes.Tx{checkStateTx("a", 0)})

	committed := make(chan struct{})
	go func() {
		commitBlock(t, txmp, app, 2, []cmttypes.Tx{checkStateTx("a", 1)})
		close(committed)
	}()
	// Release the first pass one tx at a time; the next commit must not run before it ends.
	for i := 0; i < 3; i++ {
		select {
		case <-committed:
			t.Fatalf("next commit ran while the pass had %d txs left", 3-i)
		case <-time.After(50 * time.Millisecond):
		}
		app.recheckGate <- struct{}{}
	}
	close(app.recheckGate)
	<-committed
	waitForPass(txmp)
	require.False(t, app.commitRaced.Load())
	require.EqualValues(t, 3+2, app.rechecks.Load())
	require.Equal(t, 2, txmp.Size())
}

// A commit that had to wait for the previous pass rechecks synchronously, so
// admission keeps the next interval; the following commit is async again.
func TestAsyncRecheck_LatePassFallsBackToSync(t *testing.T) {
	app := newCheckStateApp()
	txmp := setupCheckStatePool(t, app, nil)
	addSeqTxs(t, txmp, "a", 0, 6)

	app.recheckGate = make(chan struct{})
	commitBlock(t, txmp, app, 1, []cmttypes.Tx{checkStateTx("a", 0)}) // async pass over 5 txs
	committed := make(chan struct{})
	go func() {
		commitBlock(t, txmp, app, 2, []cmttypes.Tx{checkStateTx("a", 1)})
		close(committed)
	}()
	time.Sleep(50 * time.Millisecond) // let the commit queue on the lock
	for i := 0; i < 5; i++ {
		app.recheckGate <- struct{}{}
	}
	select {
	case <-committed:
		t.Fatal("late commit returned before rechecking synchronously")
	case <-time.After(100 * time.Millisecond):
	}
	for i := 0; i < 4; i++ {
		app.recheckGate <- struct{}{}
	}
	<-committed
	require.True(t, txmp.mtx.TryLock(), "late commit left a background pass holding the lock")
	txmp.mtx.Unlock()

	commitBlock(t, txmp, app, 3, []cmttypes.Tx{checkStateTx("a", 2)}) // async again: returns with the pass gated
	require.False(t, txmp.mtx.TryLock())
	close(app.recheckGate)
	waitForPass(txmp)
	require.EqualValues(t, 5+4+3, app.rechecks.Load())
	require.Equal(t, 3, txmp.Size())
}

// A panic in the synchronous part still releases the lock.
func TestAsyncRecheck_PanicReleasesLock(t *testing.T) {
	txmp := setupCheckStatePool(t, newCheckStateApp(), nil)
	txmp.Lock()
	require.Panics(t, func() {
		_ = txmp.UpdateAsyncRecheck(1, []*cmttypes.CachedTx{checkStateTx("a", 0).ToCachedTx()}, nil, nil, nil)
	})
	require.True(t, txmp.mtx.TryLock())
	txmp.mtx.Unlock()
}

// The height signal and txs-available notification fire after the pass.
func TestAsyncRecheck_SignalsAfterPass(t *testing.T) {
	app := newCheckStateApp()
	txmp := setupCheckStatePool(t, app, nil)
	txmp.EnableTxsAvailable()
	addSeqTxs(t, txmp, "a", 0, 3)
	drain(txmp.HeightSignal())
	drain(txmp.TxsAvailable())

	app.recheckGate = make(chan struct{})
	commitBlock(t, txmp, app, 1, []cmttypes.Tx{checkStateTx("a", 0)})
	select {
	case <-txmp.HeightSignal():
		t.Fatal("height signal fired before the pass")
	case <-txmp.TxsAvailable():
		t.Fatal("txs available fired before the pass")
	case <-time.After(100 * time.Millisecond):
	}
	close(app.recheckGate)
	select {
	case <-txmp.HeightSignal():
	case <-time.After(5 * time.Second):
		t.Fatal("no height signal after the pass")
	}
	<-txmp.TxsAvailable()
}

// Back-to-back commits (a pass longer than the interval): passes never overlap
// and each commit is followed by exactly one pass over the whole pool.
func TestAsyncRecheck_PassesNeverPileUp(t *testing.T) {
	app := newCheckStateApp()
	app.recheckDelay = time.Millisecond
	txmp := setupCheckStatePool(t, app, nil)
	const signers, perSigner, heights = 20, 10, 5
	for s := 0; s < signers; s++ {
		addSeqTxs(t, txmp, fmt.Sprintf("s%02d", s), 0, perSigner)
	}

	pool := signers * perSigner
	want := 0
	for h := 1; h <= heights; h++ {
		var block []cmttypes.Tx
		for s := 0; s < signers; s++ {
			block = append(block, checkStateTx(fmt.Sprintf("s%02d", s), uint64(h-1)))
		}
		commitBlock(t, txmp, app, int64(h), block)
		pool -= signers
		want += pool
	}
	waitForPass(txmp)
	// Overlapping or skipped passes would break the per-signer replay order.
	require.False(t, app.commitRaced.Load())
	require.EqualValues(t, want, app.rechecks.Load())
	require.Equal(t, pool, txmp.Size())
}

// With the default config, or recheck off, the recheck runs inside the call and
// the lock is released before return.
func TestAsyncRecheck_SwitchesOffStaySynchronous(t *testing.T) {
	for _, tc := range []struct {
		name         string
		async        bool
		recheck      bool
		wantRechecks int32
	}{
		{"default config", cmtcfg.DefaultMempoolConfig().AsyncRecheck, true, 2},
		{"recheck off", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newCheckStateApp()
			app.recheckDelay = 20 * time.Millisecond // a background pass would still hold the lock
			txmp := setupCheckStatePool(t, app, func(c *cmtcfg.MempoolConfig) {
				c.AsyncRecheck = tc.async
				c.Recheck = tc.recheck
			})
			txmp.EnableTxsAvailable()
			addSeqTxs(t, txmp, "a", 0, 3)
			drain(txmp.TxsAvailable())
			drain(txmp.HeightSignal())

			txmp.Lock()
			app.commit([]cmttypes.Tx{checkStateTx("a", 0)})
			require.NoError(t, txmp.UpdateAsyncRecheck(1, []*cmttypes.CachedTx{checkStateTx("a", 0).ToCachedTx()},
				abciResponses(1, abci.CodeTypeOK), nil, nil))
			require.True(t, txmp.mtx.TryLock())
			txmp.mtx.Unlock()
			require.Equal(t, tc.wantRechecks, app.rechecks.Load())
			require.Len(t, txmp.HeightSignal(), 1)
			require.Len(t, txmp.TxsAvailable(), 1)
		})
	}
}

// Race test: commits, recheck passes, admission, reaping and sequence queries
// run concurrently; every committed tx must be gone from the pool at once.
func TestAsyncRecheck_Race(t *testing.T) {
	app := newCheckStateApp()
	app.recheckDelay = 50 * time.Microsecond
	txmp := setupCheckStatePool(t, app, nil)
	query, err := proxy.NewConnSyncLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, query.Start())
	t.Cleanup(func() { require.NoError(t, query.Stop()) })

	const signers, heights = 8, 40
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for s := 0; s < signers; s++ {
		signer := fmt.Sprintf("s%d", s)
		wg.Add(1)
		go func() { // client: submit the next sequence per the app's check state
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := query.QuerySequence(context.Background(), &abci.RequestQuerySequence{Signer: []byte(signer)})
				require.NoError(t, err)
				_ = txmp.CheckTx(checkStateTx(signer, res.Sequence), nil, mempool.TxInfo{})
				time.Sleep(200 * time.Microsecond)
			}
		}()
	}
	wg.Add(1)
	go func() { // other proposers' reaps
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = txmp.ReapMaxBytesMaxGas(-1, -1)
				_ = txmp.Size()
			}
		}
	}()

	committed := map[cmttypes.TxKey]bool{}
	for h := int64(1); h <= heights; h++ {
		time.Sleep(2 * time.Millisecond)
		block := txmp.ReapMaxTxs(10)
		txs := make([]cmttypes.Tx, len(block))
		for i, tx := range block {
			txs[i] = tx.Tx
			committed[tx.Key()] = true
		}
		commitBlock(t, txmp, app, h, txs)
		for _, tx := range txmp.ReapMaxBytesMaxGas(-1, -1) {
			require.False(t, committed[tx.Key()], "committed tx reaped at height %d", h)
		}
	}
	close(stop)
	wg.Wait()
	waitForPass(txmp)
	require.False(t, app.commitRaced.Load())
	require.Positive(t, app.rechecks.Load())
}
