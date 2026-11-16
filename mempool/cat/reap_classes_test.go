package cat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	squareshare "github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/proxy"
	cmttypes "github.com/cometbft/cometbft/types"
)

func pbBytes(b []byte, num protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), v)
}

func anyMsg(url string, val []byte) []byte {
	return pbBytes(pbBytes(nil, 1, []byte(url)), 2, val)
}

// txRaw encodes a cosmos-sdk TxRaw with the given messages and auth_info bytes.
func txRaw(authInfo []byte, msgs ...[]byte) []byte {
	var body []byte
	for _, m := range msgs {
		body = pbBytes(body, 1, m)
	}
	return pbBytes(pbBytes(nil, 1, body), 2, authInfo)
}

const (
	sendURL    = "/cosmos.bank.v1beta1.MsgSend"
	msgExecURL = "/cosmos.authz.v1beta1.MsgExec"
)

// encodeClassTx encodes a real tx of class "sdk" ("sdkN": N messages), "pfb", "pff" or
// "unk..." (no messages).
func encodeClassTx(t testing.TB, id, class string) []byte {
	t.Helper()
	switch {
	case class == "pff":
		return txRaw([]byte(id), anyMsg(payForFibreURL, []byte(id)))
	case class == "pfb":
		b, err := squareshare.NewBlob(squareshare.MustNewV0Namespace([]byte("classes")), []byte(id), 0, nil)
		require.NoError(t, err)
		tx, err := squaretx.MarshalBlobTx(txRaw([]byte(id), anyMsg("/celestia.blob.v1.MsgPayForBlobs", []byte(id))), b)
		require.NoError(t, err)
		return tx
	case strings.HasPrefix(class, "unk"):
		return txRaw([]byte(id)) // no messages
	case strings.HasPrefix(class, "sdk"):
		n := 1
		if class != "sdk" {
			n, _ = strconv.Atoi(class[3:])
		}
		msgs := make([][]byte, n)
		for i := range msgs {
			msgs[i] = anyMsg(sendURL, []byte(id))
		}
		return txRaw([]byte(id), msgs...)
	}
	t.Fatalf("unknown class %q", class)
	return nil
}

// classTxMeta is a registered tx; bucket and weight are how the modeled app fills it.
type classTxMeta struct {
	name, signer   string
	seq, prio      int
	bucket, weight int
}

// classApp serves registered txs; rechecks are logged, wait on gate, reject bad txs and
// return an error for failed ones. Like a real app, it then rejects the sender's later
// sequences in the same pass.
type classApp struct {
	abci.BaseApplication
	mtx    sync.Mutex
	meta   map[string]classTxMeta
	bad    map[string]bool
	failed map[string]bool
	drop   map[string]bool // txs appFill drops, e.g. a PayForFibre that cannot settle
	dead   map[string]bool // senders with a rejected sequence in this pass
	order  []string
	gate   chan struct{}
	at     atomic.Int32 // rechecks that reached the gate
}

func (app *classApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	app.mtx.Lock()
	m := app.meta[string(req.Tx)]
	bad, failed := app.bad[m.name] || app.dead[m.signer], app.failed[m.name]
	if req.Type == abci.CheckTxType_Recheck {
		app.order = append(app.order, m.name)
		if bad || failed {
			app.dead[m.signer] = true
		}
	}
	app.mtx.Unlock()
	if req.Type == abci.CheckTxType_Recheck {
		app.at.Add(1)
		if app.gate != nil {
			<-app.gate
		}
		if failed {
			return nil, errors.New("recheck failed")
		}
		if bad {
			return &abci.ResponseCheckTx{Code: 1}, nil
		}
	}
	return &abci.ResponseCheckTx{Code: abci.CodeTypeOK, GasWanted: 1, Priority: int64(m.prio),
		Address: []byte(m.signer), Sequence: uint64(m.seq)}, nil
}

type classPool struct {
	t       testing.TB
	app     *classApp
	txmp    *TxPool
	version uint64 // the app version commit hands the pool
}

// newClassPool starts an async recheck pool at app v11; caps, if set, replace the app's caps.
func newClassPool(t testing.TB, caps *[3]int) *classPool {
	t.Helper()
	if caps != nil {
		orig := maxBlockMsgs
		maxBlockMsgs = func(uint64) (int, int, int, bool) { return caps[0], caps[1], caps[2], true }
		t.Cleanup(func() { maxBlockMsgs = orig })
	}
	return newPool(t, true)
}

func newPool(t testing.TB, async bool) *classPool {
	app := &classApp{meta: map[string]classTxMeta{}, bad: map[string]bool{}, failed: map[string]bool{},
		drop: map[string]bool{}, dead: map[string]bool{}}
	cfg := cmtcfg.TestMempoolConfig()
	cfg.AsyncRecheck = async
	cfg.Size = 5000
	conn, err := proxy.NewConnSyncLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, conn.Start())
	t.Cleanup(func() { require.NoError(t, conn.Stop()) })
	txmp := NewTxPool(log.NewNopLogger(), cfg, conn, 0)
	return &classPool{t: t, app: app, txmp: txmp, version: cmttypes.BlockTxLimitAppVersion}
}

// add admits signer/seq of class at prio; its name is "signer/seq/class". An "unkN" tx is
// unknown to core and filled by the modeled app as class N.
func (p *classPool) add(signer string, seq, prio int, class string) string {
	name := fmt.Sprintf("%s/%d/%s", signer, seq, class)
	tx := encodeClassTx(p.t, name, class)
	m := classTxMeta{name: name, signer: signer, seq: seq, prio: prio, weight: 1}
	switch {
	case class == "pfb":
		m.bucket = classPFB
	case class == "pff":
		m.bucket = classPFF
	case strings.HasPrefix(class, "unk") && len(class) > 3:
		m.bucket, _ = strconv.Atoi(class[3:])
	case strings.HasPrefix(class, "sdk") && class != "sdk":
		m.weight, _ = strconv.Atoi(class[3:])
	}
	p.app.mtx.Lock()
	p.app.meta[string(tx)] = m
	p.app.mtx.Unlock()
	require.NoError(p.t, p.txmp.CheckTx(tx, nil, mempool.TxInfo{}))
	return name
}

func (p *classPool) names(txs []*cmttypes.CachedTx) []string {
	p.app.mtx.Lock()
	defer p.app.mtx.Unlock()
	out := make([]string, len(txs))
	for i, tx := range txs {
		out[i] = p.app.meta[string(tx.Tx)].name
	}
	return out
}

func (p *classPool) counts(txs []*cmttypes.CachedTx) map[string]int {
	m := map[string]int{}
	for _, n := range p.names(txs) {
		m[strings.TrimRight(n[strings.LastIndex(n, "/")+1:], "0123456789")]++
	}
	return m
}

// appFill models PrepareProposal's fill of txs: SDK, then PFB, then PFF txs, each class in
// reap order up to its cap. It skips a tx that does not fit, one in app.drop, and one whose
// sender's previous sequence it did not take (the app checks sequences).
func (p *classPool) appFill(txs []*cmttypes.CachedTx, caps [3]int) (fill [3]int) {
	p.app.mtx.Lock()
	defer p.app.mtx.Unlock()
	next := map[string]int{}
	for b := range fill {
		for _, tx := range txs {
			m := p.app.meta[string(tx.Tx)]
			if m.bucket != b || next[m.signer] != m.seq || p.app.drop[m.name] || fill[b]+m.weight > caps[b] {
				continue
			}
			fill[b] += m.weight
			next[m.signer]++
		}
	}
	return fill
}

// commit starts a gated async recheck pass at height, or rechecks synchronously.
func (p *classPool) commit(height int64, gate chan struct{}) {
	p.app.mtx.Lock()
	p.app.gate, p.app.dead = gate, map[string]bool{}
	p.app.mtx.Unlock()
	p.txmp.Lock()
	require.NoError(p.t, p.txmp.UpdateAsyncRecheck(height, p.version, nil, nil, nil, nil))
}

func (p *classPool) startReap(maxBytes int64) chan []*cmttypes.CachedTx {
	got := make(chan []*cmttypes.CachedTx, 1)
	go func() { got <- p.txmp.ReapMaxBytesMaxGas(maxBytes, -1) }()
	return got
}

func requireNoReap(t *testing.T, got chan []*cmttypes.CachedTx, why string) {
	t.Helper()
	select {
	case <-got:
		t.Fatalf("%s: reap returned", why)
	case <-time.After(200 * time.Millisecond):
	}
}

func requireReap(t *testing.T, got chan []*cmttypes.CachedTx, why string) []*cmttypes.CachedTx {
	t.Helper()
	select {
	case txs := <-got:
		return txs
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: reap did not return", why)
		return nil
	}
}

func feed(gate chan struct{}, n int) {
	for i := 0; i < n; i++ {
		gate <- struct{}{}
	}
}

// releasesAt starts a pass and a reap, and requires the reap to return exactly once the
// pass has processed n txs; it returns that reap.
func (p *classPool) releasesAt(t *testing.T, n int) []*cmttypes.CachedTx {
	t.Helper()
	gate := make(chan struct{}, 5000)
	p.commit(1, gate)
	got := p.startReap(-1)
	feed(gate, n-1)
	requireNoReap(t, got, fmt.Sprintf("%d txs processed", n-1))
	feed(gate, 1)
	during := requireReap(t, got, fmt.Sprintf("%d txs processed", n))
	close(gate)
	waitForPass(p.txmp)
	return during
}

// 1100 SDK txs priced above 700 PFFs, app v11: the reap returns once the pass has kept 750
// SDK txs (600 + 150) and 600 PFFs (200 + 400), and takes every tx kept so far.
func TestReapClasses_SDKFloodKeepsPFFs(t *testing.T) {
	p := newClassPool(t, nil)
	for i := 0; i < 1100; i++ {
		p.add(fmt.Sprintf("s%04d", i), 0, 100, "sdk")
	}
	for i := 0; i < 700; i++ {
		p.add(fmt.Sprintf("p%04d", i), 0, 1, "pff")
	}
	during := p.releasesAt(t, 1100+600)
	require.Equal(t, map[string]int{"sdk": 1100, "pff": 600}, p.counts(during))
	require.Equal(t, map[string]int{"sdk": 1100, "pff": 700}, p.counts(p.txmp.ReapMaxBytesMaxGas(-1, -1)))
}

// For an app version whose caps core does not know, a reap waits for the pass to end; at
// v11 it returns once the last tx the app could take is rechecked.
func TestReapClasses_UnknownVersionWaitsForPassEnd(t *testing.T) {
	for v, at := range map[uint64]int{
		cmttypes.BlockTxLimitAppVersion - 1: 12, cmttypes.BlockTxLimitAppVersion: 11, cmttypes.BlockTxLimitAppVersion + 1: 12,
	} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			p := newClassPool(t, nil)
			p.version = v
			for i := 0; i < 10; i++ {
				p.add(fmt.Sprintf("s%d", i), 0, 9, "sdk")
			}
			p.add("t", 0, 1, "pff")
			p.add("t", 1, 1, "sdk") // the app fills it before t/0: its sequence check fails
			require.Len(t, p.releasesAt(t, at), at)
		})
	}
}

// A class that runs out of txs stops holding the wait once the pass has processed all of
// them, kept or evicted, while the pass still runs.
func TestReapClasses_ClassRunsOut(t *testing.T) {
	for _, evict := range []bool{false, true} {
		t.Run(fmt.Sprintf("evicted=%v", evict), func(t *testing.T) {
			p := newClassPool(t, &[3]int{2, 2, 2}) // needs 2, 3, 6
			want := []string{p.add("a", 0, 9, "sdk"), p.add("b", 0, 9, "sdk"), p.add("c", 0, 9, "sdk")}
			pff := p.add("p", 0, 5, "pff")
			p.app.bad[pff] = evict
			for i := 0; i < 5; i++ {
				p.add(fmt.Sprintf("z%d", i), 0, 1, "sdk")
			}
			if !evict {
				want = append(want, pff)
			}
			gate := make(chan struct{})
			p.commit(1, gate)
			got := p.startReap(-1)
			feed(gate, 3)
			requireNoReap(t, got, "PFF not yet processed")
			feed(gate, 1)
			require.Equal(t, want, p.names(requireReap(t, got, "all PFFs processed")))
			require.NotNil(t, p.txmp.pass.Load(), "pass must still be running")
			close(gate)
			waitForPass(p.txmp)
		})
	}
}

// While a tx core cannot classify may still be rechecked, no class that ran out of txs
// is safe: the unknown tx could be of that class.
func TestReapClasses_UnknownOnlyOverWaits(t *testing.T) {
	for _, class := range []string{"sdk", "unk"} {
		t.Run(class, func(t *testing.T) {
			p := newClassPool(t, &[3]int{2, 2, 2})
			want := []string{p.add("a", 0, 9, "sdk"), p.add("b", 0, 9, "sdk"),
				p.add("p", 0, 5, "pff"), p.add("q", 0, 5, "pff")}
			u := p.add("u", 0, 3, class)
			for i := 0; i < 5; i++ {
				p.add(fmt.Sprintf("z%d", i), 0, 1, "sdk")
			}
			gate := make(chan struct{})
			p.commit(1, gate)
			got := p.startReap(-1)
			feed(gate, 4)
			if class == "unk" {
				requireNoReap(t, got, "unknown tx not yet processed")
				feed(gate, 1)
				want = append(want, u)
			}
			require.Equal(t, want, p.names(requireReap(t, got, "classes safe")))
			close(gate)
			waitForPass(p.txmp)
		})
	}
}

// Txs the pass evicts are not counted: the reap waits until enough kept txs.
func TestReapClasses_EvictedNotCounted(t *testing.T) {
	p := newClassPool(t, &[3]int{2, 2, 2})
	p.app.bad[p.add("a", 0, 9, "sdk")] = true
	p.app.bad[p.add("b", 0, 9, "sdk")] = true
	c, d := p.add("c", 0, 5, "sdk"), p.add("d", 0, 5, "sdk")
	p.add("e", 0, 5, "sdk")
	gate := make(chan struct{})
	p.commit(1, gate)
	got := p.startReap(-1)
	feed(gate, 3)
	requireNoReap(t, got, "one kept SDK tx")
	feed(gate, 1)
	require.Equal(t, []string{c, d}, p.names(requireReap(t, got, "two kept")))
	close(gate)
	waitForPass(p.txmp)
}

// A reap is never capped by class.
func TestReapClasses_Uncapped(t *testing.T) {
	p := newClassPool(t, &[3]int{1, 1, 1})
	for i := 0; i < 3; i++ {
		p.add(fmt.Sprintf("u%d", i), 0, 9-i, "unk")
		p.add(fmt.Sprintf("s%d", i), 0, 9-i, "sdk")
		p.add(fmt.Sprintf("p%d", i), 0, 9-i, "pff")
	}
	require.Len(t, p.txmp.ReapMaxBytesMaxGas(-1, -1), 9)
	p.commit(1, nil)
	waitForPass(p.txmp)
	require.Len(t, p.txmp.ReapMaxBytesMaxGas(-1, -1), 9)
}

// A recheck that errors leaves its tx unstamped, so the reap stops its sender there: the
// pass does not count the sender's later txs.
func TestReapClasses_RecheckErrorEndsPrefix(t *testing.T) {
	p := newClassPool(t, &[3]int{2, 1, 1})
	p.app.failed[p.add("a", 0, 9, "sdk")] = true
	p.add("a", 1, 9, "pff")
	b0, b1 := p.add("b", 0, 9, "sdk"), p.add("b", 1, 9, "sdk")
	p0 := p.add("p", 0, 1, "pff")
	p.add("z", 0, 1, "sdk") // keeps the pass running
	gate := make(chan struct{})
	p.commit(1, gate)
	got := p.startReap(-1)
	feed(gate, 4)
	requireNoReap(t, got, "a's PFF must not count")
	feed(gate, 1)
	require.Equal(t, []string{b0, b1, p0}, p.names(requireReap(t, got, "p kept")))
	close(gate)
	waitForPass(p.txmp)
}

// A tx removed since the pass took its snapshot is neither rechecked nor counted.
func TestReapClasses_RemovedDuringPass(t *testing.T) {
	p := newClassPool(t, &[3]int{2, 1, 1})
	z, b := p.add("z", 0, 9, "sdk"), p.add("b", 0, 5, "sdk")
	a := p.add("a", 0, 7, "sdk")
	gate := make(chan struct{})
	p.commit(1, gate)
	got := p.startReap(-1)
	require.Eventually(t, func() bool { return p.app.at.Load() == 1 }, 5*time.Second, time.Millisecond)
	require.NoError(t, p.txmp.RemoveTxByKey(cmttypes.Tx(encodeClassTx(t, a, "sdk")).Key()))
	feed(gate, 1)
	requireNoReap(t, got, "only z counted")
	feed(gate, 1)
	require.Equal(t, []string{z, b}, p.names(requireReap(t, got, "z and b kept")))
	close(gate)
	waitForPass(p.txmp)
	require.Equal(t, []string{"z/0/sdk", "b/0/sdk"}, p.app.order)
}

// Once every class is safe, each waiting reap still waits for the pass end if the kept txs
// are over half its maxBytes: the app's byte limit could bind.
func TestReapClasses_ByteGuard(t *testing.T) {
	p := newClassPool(t, &[3]int{1, 1, 1}) // needs 1, 1, 3
	s0 := p.add("s0", 0, 9, "sdk")
	for i := 1; i < 5; i++ {
		p.add(fmt.Sprintf("s%d", i), 0, 1, "sdk")
	}
	size := p.txmp.store.get(cmttypes.Tx(encodeClassTx(t, s0, "sdk")).Key()).size()
	gate := make(chan struct{})
	p.commit(1, gate)
	small, big := p.startReap(size), p.startReap(2*size)
	feed(gate, 1)
	require.Equal(t, []string{s0}, p.names(requireReap(t, big, "s0 is half of maxBytes")))
	requireNoReap(t, small, "s0 is all of maxBytes")
	close(gate)
	require.Equal(t, []string{s0}, p.names(requireReap(t, small, "pass end")))
	waitForPass(p.txmp)
}

// A heavy SDK tx the app skips does not count toward the SDK class (real v11 caps): the
// reap waits for 750 simple SDK txs, from which the app fills 600, as from a sync reap.
func TestReapClasses_HeavySDKTxNotCounted(t *testing.T) {
	caps := [3]int{600, 200, 200}
	p := newClassPool(t, nil)
	p.add("a", 0, 100, "sdk")
	p.add("b", 0, 99, "sdk600")
	for i := 0; i < 800; i++ {
		p.add(fmt.Sprintf("h%03d", i), 0, 50, "sdk")
	}
	during := p.releasesAt(t, 2+749)
	require.Equal(t, [3]int{600, 0, 0}, p.appFill(during, caps))
	require.Equal(t, [3]int{600, 0, 0}, p.appFill(p.txmp.ReapMaxBytesMaxGas(-1, -1), caps))
}

// PFFs that cannot settle ahead of honest ones (real v11 caps): with 399 of them, the
// reap waits for 600 PFFs, which still hold the 200 honest ones the app fills from a sync reap.
func TestReapClasses_UnsettleablePFFs(t *testing.T) {
	caps := [3]int{600, 200, 200}
	p := newClassPool(t, nil)
	for i := 0; i < 399; i++ {
		p.app.drop[p.add(fmt.Sprintf("w%03d", i), 0, 100, "pff")] = true
	}
	for i := 0; i < 300; i++ {
		p.add(fmt.Sprintf("h%03d", i), 0, 50, "pff")
	}
	during := p.releasesAt(t, 600)
	require.Equal(t, [3]int{0, 0, 200}, p.appFill(during, caps))
	require.Equal(t, [3]int{0, 0, 200}, p.appFill(p.txmp.ReapMaxBytesMaxGas(-1, -1), caps))
}

// A tx the app cannot take does not hold the wait: one behind its sender's
// tx of a class the app fills later, or of a class already safe, cannot make the block.
func TestReapClasses_UnreachableTxsDoNotHold(t *testing.T) {
	t.Run("behind a later class", func(t *testing.T) {
		p := newClassPool(t, &[3]int{4, 4, 4}) // needs 5, 6, 12
		for i := 0; i < 3; i++ {
			p.add(fmt.Sprintf("s%d", i), 0, 60, "sdk")
		}
		for i := 0; i < 40; i++ {
			p.add(fmt.Sprintf("p%02d", i), 0, 50, "pff")
		}
		p.add("t", 0, 1, "pff")
		p.add("t", 1, 1, "sdk") // the app fills it before t/0: its sequence check fails
		p.releasesAt(t, 3+12)
	})
	t.Run("behind a safe class", func(t *testing.T) {
		p := newClassPool(t, &[3]int{4, 4, 4})
		for i := 0; i < 10; i++ {
			p.add(fmt.Sprintf("s%d", i), 0, 60, "sdk")
		}
		for i := 0; i < 40; i++ {
			p.add(fmt.Sprintf("p%02d", i), 0, 50, "pff")
		}
		p.add("t", 0, 1, "sdk") // the app's SDK class is full before it
		p.add("t", 1, 1, "pfb")
		p.releasesAt(t, 10+12)
	})
}

// A simple tx behind its sender's tx of another class does not count: here the app's SDK
// class is full (h), so it skips each m/0 and fails m/1; only the 4 PFFs of p count.
func TestReapClasses_MixedSenderNotCounted(t *testing.T) {
	caps := [3]int{8, 2, 2} // needs 10, 3, 6
	p := newClassPool(t, &caps)
	p.add("h", 0, 9, "sdk8")
	for i := 0; i < 9; i++ {
		p.add(fmt.Sprintf("m%d", i), 0, 8, "sdk")
		p.add(fmt.Sprintf("m%d", i), 1, 8, "pff")
	}
	for i := 0; i < 4; i++ {
		p.add(fmt.Sprintf("p%d", i), 0, 1, "pff")
	}
	during := p.releasesAt(t, 1+18+4)
	require.Equal(t, [3]int{8, 0, 2}, p.appFill(during, caps))
}

// A tx the app reports no sender for does not count: core cannot see what it depends on.
func TestReapClasses_NoSenderNotCounted(t *testing.T) {
	p := newClassPool(t, &[3]int{1, 1, 1}) // needs 1, 1, 3
	p.add("", 0, 9, "sdk")
	p.add("a", 0, 5, "sdk")
	p.add("z", 0, 1, "sdk")
	p.releasesAt(t, 2)
}

// Random pools: the in-pass reap is the post-pass reap cut where the pass stood, and the
// modeled app fills each class from it at least as much as from a sync recheck's reap.
// The app skips over-cap txs and txs behind an untaken sequence, and drops up to each margin.
func TestReapClasses_NoSmallerThanSync(t *testing.T) {
	for _, tc := range []struct {
		name    string
		unknown bool
		mixed   int // senders with mixed classes, in 1/3
	}{{"all classes", true, 1}, {"known classes, mostly mixed senders", false, 2}} {
		t.Run(tc.name, func(t *testing.T) { noSmallerThanSync(t, tc.unknown, tc.mixed) })
	}
}

func noSmallerThanSync(t *testing.T, unknown bool, mixedIn3 int) {
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	early := 0
	const rounds = 150
	for round := 0; round < rounds; round++ {
		caps := [3]int{2 + rng.Intn(5), 2 + rng.Intn(4), 2 + rng.Intn(4)}
		classes := []string{"sdk", "sdk", "sdk2", fmt.Sprintf("sdk%d", caps[0]), "pfb", "pff", "pff"}
		if unknown {
			classes = append(classes, "unk0", "unk1", "unk2")
		}
		p, s := newClassPool(t, &caps), newPool(t, false)
		need, _ := classNeeds(cmttypes.BlockTxLimitAppVersion)
		var drops [3]int64
		total := 0
		for snd := 0; snd < 5+rng.Intn(40); snd++ {
			prio, mixed := rng.Intn(10), rng.Intn(3) < mixedIn3
			class := classes[rng.Intn(len(classes))]
			n := 1 + rng.Intn(4)
			for q := 0; q < n; q++ {
				if mixed {
					class = classes[rng.Intn(len(classes))]
				}
				bad := rng.Intn(10) == 0
				name := p.add(fmt.Sprintf("r%d", snd), q, prio, class)
				s.add(fmt.Sprintf("r%d", snd), q, prio, class)
				p.app.bad[name], s.app.bad[name] = bad, bad
				// A dropped last tx loses no later ones: drops stay within the margins.
				if b := p.app.meta[string(encodeClassTx(t, name, class))].bucket; q == n-1 && rng.Intn(3) == 0 &&
					drops[b] < need[b]-int64(caps[b]) {
					drops[b]++
					p.app.drop[name], s.app.drop[name] = true, true
				}
				total++
			}
		}
		s.commit(1, nil)
		syncReap := s.txmp.ReapMaxBytesMaxGas(-1, -1)

		gate := make(chan struct{})
		p.commit(1, gate)
		got := p.startReap(-1)
		var during []*cmttypes.CachedTx
		for k := 1; k <= total && during == nil; k++ {
			gate <- struct{}{}
			if k < total { // tx k is processed once the pass reaches tx k+1
				require.Eventually(t, func() bool { return p.app.at.Load() == int32(k+1) }, 5*time.Second, time.Microsecond)
			}
			select {
			case during = <-got:
				if k < total {
					early++
				}
			case <-time.After(time.Millisecond):
			}
		}
		close(gate)
		if during == nil {
			during = requireReap(t, got, "pass end")
		}
		waitForPass(p.txmp)
		after := p.names(p.txmp.ReapMaxBytesMaxGas(-1, -1))
		require.Equal(t, after[:len(during)], p.names(during), "round %d", round)
		inPass, synced := p.appFill(during, caps), s.appFill(syncReap, caps)
		for c := range inPass {
			require.GreaterOrEqual(t, inPass[c], synced[c], "round %d class %d caps %v", round, c, caps)
		}
	}
	t.Logf("%d of %d reaps returned before the pass end", early, rounds)
}

func TestReapClasses_Needs(t *testing.T) {
	need, ok := classNeeds(cmttypes.BlockTxLimitAppVersion)
	require.True(t, ok)
	require.Equal(t, [classUnknown]int64{750, 300, 600}, need)
	for _, v := range []uint64{cmttypes.BlockTxLimitAppVersion - 1, cmttypes.BlockTxLimitAppVersion + 1} {
		_, ok := classNeeds(v)
		require.False(t, ok)
	}
}

func TestClassifyTx(t *testing.T) {
	send := anyMsg(sendURL, []byte{0x0a, 0x01, 'a'})
	pff := anyMsg(payForFibreURL, []byte{0x0a, 0x01, 'a'})
	exec := anyMsg(msgExecURL, pbBytes(pbBytes(nil, 1, []byte("grantee")), 2, send))
	zk := anyMsg("/celestia.zkism.v1.MsgSubmitMessages", nil)
	b, err := squareshare.NewBlob(squareshare.MustNewV0Namespace([]byte("classes")), []byte("data"), 0, nil)
	require.NoError(t, err)
	pfb := anyMsg("/celestia.blob.v1.MsgPayForBlobs", nil)
	blobTx, err := squaretx.MarshalBlobTx(txRaw(nil, pfb), b)
	require.NoError(t, err)
	withBody := func(body []byte) []byte { return pbBytes(nil, 1, body) }
	timeoutHeight := append(pbBytes(nil, 1, send), protowire.AppendVarint(protowire.AppendTag(nil, 3, protowire.VarintType), 9)...)
	blobTimeout, err := squaretx.MarshalBlobTx(withBody(append(pbBytes(nil, 1, pfb), protowire.AppendVarint(protowire.AppendTag(nil, 3, protowire.VarintType), 9)...)), b)
	require.NoError(t, err)

	type want struct {
		class  uint8
		simple bool
	}
	for name, c := range map[string]struct {
		tx   []byte
		want want
	}{
		"send":               {txRaw(nil, send), want{classSDK, true}},
		"delegate":           {txRaw(nil, anyMsg("/cosmos.staking.v1beta1.MsgDelegate", nil)), want{classSDK, true}},
		"other sdk msg":      {txRaw(nil, anyMsg("/cosmos.gov.v1.MsgVote", nil)), want{classSDK, false}},
		"two sends":          {txRaw(nil, send, send), want{classSDK, false}},
		"exec":               {txRaw(nil, exec), want{classSDK, false}},
		"timeout height":     {withBody(timeoutHeight), want{classSDK, false}},
		"unordered":          {withBody(append(pbBytes(nil, 1, send), protowire.AppendVarint(protowire.AppendTag(nil, 4, protowire.VarintType), 1)...)), want{classSDK, false}},
		"timeout timestamp":  {withBody(append(pbBytes(nil, 1, send), pbBytes(nil, 5, []byte{0x08, 0x01})...)), want{classSDK, false}},
		"pff":                {txRaw(nil, pff), want{classPFF, true}},
		"pff mixed":          {txRaw(nil, send, pff), want{classUnknown, false}},
		"two pffs":           {txRaw(nil, pff, pff), want{classUnknown, false}},
		"zk proof":           {txRaw(nil, zk), want{classUnknown, false}},
		"zk proof and send":  {txRaw(nil, send, zk), want{classUnknown, false}},
		"ism":                {txRaw(nil, anyMsg("/hyperlane.core.v1.MsgProcessMessage", nil)), want{classUnknown, false}},
		"escrow deposit":     {txRaw(nil, anyMsg("/celestia.fibre.v1.MsgDepositToEscrow", nil)), want{classUnknown, false}}, //nolint:misspell // proto package name
		"blob tx":            {blobTx, want{classPFB, true}},
		"blob tx, timeout":   {blobTimeout, want{classPFB, false}},
		"blob tx, no blobs":  {pbBytes(pbBytes(nil, 1, txRaw(nil, pfb)), 3, []byte(squaretx.ProtoBlobTxTypeID)), want{classPFB, true}}, // CheckTx rejects it
		"no messages":        {txRaw(nil), want{classUnknown, false}},
		"garbage":            {[]byte{0xff, 0xff, 0xff}, want{classUnknown, false}},
		"truncated":          {txRaw(nil, pff)[:10], want{classUnknown, false}},
		"msg wrong wiretype": {pbBytes(nil, 1, protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 1)), want{classUnknown, false}},
		// protobuf keeps the last of a repeated scalar field, and so does the classifier.
		"type_url twice": {txRaw(nil, pbBytes(pbBytes(nil, 1, []byte(sendURL)), 1, []byte(payForFibreURL))), want{classPFF, true}},
		"body twice":     {append(txRaw(nil, send), pbBytes(nil, 1, pbBytes(nil, 1, pff))...), want{classPFF, true}},
	} {
		class, simple := classifyTx(c.tx)
		require.Equal(t, c.want, want{class, simple}, name)
	}
}

// classifyTx runs on peer bytes outside the pool lock: it never allocates, whatever the input.
func TestClassifyTx_NoAllocs(t *testing.T) {
	const size = 2 << 20
	var anys, nested []byte
	for one := pbBytes(nil, 1, anyMsg("ab", nil)); len(anys) < size-16; {
		anys = append(anys, one...)
	}
	for i := 0; i < 9000; i++ {
		nested = protowire.AppendTag(nested, 5, protowire.StartGroupType)
	}
	for _, tx := range [][]byte{
		bytes.Repeat([]byte{0x12, 0x00}, size/2), // empty BlobTx.blobs entries
		append(pbBytes(nil, 1, pbBytes(nil, 1, anyMsg(sendURL, nil))), bytes.Repeat([]byte{0x12, 0x00}, size/2)...),
		append(bytes.Repeat([]byte{0x12, 0x00}, size/2-4), 0x1a, 0x04, 'B', 'L', 'O', 'B'),
		pbBytes(nil, 1, anys),
		bytes.Repeat([]byte{0x0a, 0x00}, size/2),
		pbBytes(nil, 1, nested),
		encodeClassTx(t, "a", "pfb"),
	} {
		require.Zero(t, testing.AllocsPerRun(3, func() { classifyTx(tx) }))
	}
}

// classifyTx reads peer bytes before CheckTx: it must not panic, and only a known class
// can be simple.
func FuzzClassifyTx(f *testing.F) {
	for _, c := range []string{"sdk", "sdk3", "pff", "pfb", "unk"} {
		f.Add(encodeClassTx(f, "a", c))
	}
	f.Add(txRaw(nil, anyMsg(msgExecURL, pbBytes(pbBytes(nil, 1, []byte("g")), 2, anyMsg(sendURL, nil)))))
	f.Fuzz(func(t *testing.T, tx []byte) {
		class, simple := classifyTx(tx)
		require.Less(t, class, uint8(numClasses))
		require.True(t, class != classUnknown || !simple)
	})
}
