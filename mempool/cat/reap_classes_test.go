package cat

import (
	"context"
	"errors"
	"fmt"
	"math"
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

const sendURL = "/cosmos.bank.v1beta1.MsgSend"

// encodeClassTx encodes a real tx of class "sdk" ("sdkN": N messages), "pfb", "pff" or "unk".
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
	case class == "unk":
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

type classTxMeta struct {
	name, signer string
	seq, prio    int
}

// classApp serves registered txs; rechecks are logged, wait on gate, reject bad txs and
// return an error for failed ones.
type classApp struct {
	abci.BaseApplication
	mtx    sync.Mutex
	meta   map[string]classTxMeta
	bad    map[string]bool
	failed map[string]bool
	order  []string
	gate   chan struct{}
	at     atomic.Int32 // rechecks that reached the gate
}

func (app *classApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	app.mtx.Lock()
	m := app.meta[string(req.Tx)]
	bad, failed := app.bad[m.name], app.failed[m.name]
	if req.Type == abci.CheckTxType_Recheck {
		app.order = append(app.order, m.name)
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
	t    testing.TB
	app  *classApp
	txmp *TxPool
}

// newClassPool starts an async recheck pool at app v11; caps, if set, replace the app's caps.
func newClassPool(t testing.TB, caps *[3]int) *classPool {
	t.Helper()
	if caps != nil {
		orig := maxBlockMsgs
		maxBlockMsgs = func(uint64) (int, int, int, bool) { return caps[0], caps[1], caps[2], true }
		t.Cleanup(func() { maxBlockMsgs = orig })
	}
	app := &classApp{meta: map[string]classTxMeta{}, bad: map[string]bool{}, failed: map[string]bool{}}
	cfg := cmtcfg.TestMempoolConfig()
	cfg.AsyncRecheck = true
	cfg.Size = 5000
	conn, err := proxy.NewConnSyncLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, conn.Start())
	t.Cleanup(func() { require.NoError(t, conn.Stop()) })
	txmp := NewTxPool(log.NewNopLogger(), cfg, conn, 0)
	txmp.SetNextAppVersion(cmttypes.BlockTxLimitAppVersion)
	return &classPool{t: t, app: app, txmp: txmp}
}

// add admits signer/seq of class at prio; its name is "signer/seq/class".
func (p *classPool) add(signer string, seq, prio int, class string) string {
	name := fmt.Sprintf("%s/%d/%s", signer, seq, class)
	tx := encodeClassTx(p.t, name, class)
	p.app.mtx.Lock()
	p.app.meta[string(tx)] = classTxMeta{name: name, signer: signer, seq: seq, prio: prio}
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

// commit starts a gated async recheck pass at height.
func (p *classPool) commit(height int64, gate chan struct{}) {
	p.app.gate = gate
	p.txmp.Lock()
	require.NoError(p.t, p.txmp.UpdateAsyncRecheck(height, nil, nil, nil, nil))
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

// 1100 SDK txs priced above 300 PFFs: at app v11 the reap returns once the pass has kept
// 750 SDK txs and 250 PFFs (caps + 25%), which it rechecks first; at v10 nothing is capped,
// so it waits for the whole pass.
func TestReapClasses_SDKFloodKeepsPFFs(t *testing.T) {
	flood := func(t *testing.T) *classPool {
		p := newClassPool(t, nil)
		for i := 0; i < 1100; i++ {
			p.add(fmt.Sprintf("s%04d", i), 0, 100, "sdk")
		}
		for i := 0; i < 300; i++ {
			p.add(fmt.Sprintf("p%04d", i), 0, 1, "pff")
		}
		return p
	}
	t.Run("v11", func(t *testing.T) {
		p := flood(t)
		gate := make(chan struct{}, 2000)
		p.commit(1, gate)
		got := p.startReap(1 << 30)
		feed(gate, 750+249)
		requireNoReap(t, got, "below the PFF quota")
		feed(gate, 1)
		during := requireReap(t, got, "at the PFF quota")
		require.Equal(t, map[string]int{"sdk": 750, "pff": 250}, p.counts(during))
		close(gate)
		waitForPass(p.txmp)
		require.Equal(t, p.names(p.txmp.ReapMaxBytesMaxGas(1<<30, -1)), p.names(during))
		order := p.app.order
		require.Len(t, order, 1400)
		require.Equal(t, "s0749/0/sdk", order[749])
		require.Equal(t, "p0000/0/pff", order[750])
		require.Equal(t, "s0750/0/sdk", order[1000])
	})
	t.Run("v10", func(t *testing.T) {
		p := flood(t)
		p.txmp.SetNextAppVersion(cmttypes.BlockTxLimitAppVersion - 1)
		gate := make(chan struct{}, 2000)
		p.commit(1, gate)
		got := p.startReap(1 << 30)
		feed(gate, 1399)
		requireNoReap(t, got, "pass not over")
		feed(gate, 1)
		require.Equal(t, map[string]int{"sdk": 1100, "pff": 300}, p.counts(requireReap(t, got, "pass end")))
	})
}

// A class with fewer pending txs than its quota stops holding the wait once the pass has
// processed all of them, kept or evicted, while the pass still runs.
func TestReapClasses_PendingBelowQuota(t *testing.T) {
	for _, evict := range []bool{false, true} {
		t.Run(fmt.Sprintf("evicted=%v", evict), func(t *testing.T) {
			p := newClassPool(t, &[3]int{2, 2, 2})
			a, b := p.add("a", 0, 9, "sdk"), p.add("b", 0, 9, "sdk")
			p.add("c", 0, 9, "sdk")
			pff := p.add("p", 0, 5, "pff")
			p.app.bad[pff] = evict
			for i := 0; i < 5; i++ {
				p.add(fmt.Sprintf("z%d", i), 0, 1, "sdk")
			}
			gate := make(chan struct{})
			p.commit(1, gate)
			got := p.startReap(-1)
			feed(gate, 2)
			requireNoReap(t, got, "PFF not yet processed")
			feed(gate, 1)
			want := []string{a, b, pff}
			if evict {
				want = want[:2]
			}
			require.Equal(t, want, p.names(requireReap(t, got, "all pending PFFs processed")))
			require.NotNil(t, p.txmp.pass.Load(), "pass must still be running")
			close(gate)
			waitForPass(p.txmp)
		})
	}
}

// A tx core cannot classify is charged to no class and is always waited for: it only
// delays the reap, which still holds every classified tx it would hold otherwise.
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
			require.Equal(t, want, p.names(requireReap(t, got, "quotas met")))
			close(gate)
			waitForPass(p.txmp)
		})
	}
}

// Txs the pass evicts are not charged: the reap waits until kept txs fill the quota.
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

// Unknown txs are never capped; without async recheck nothing is classified or capped.
func TestReapClasses_Uncapped(t *testing.T) {
	p := newClassPool(t, &[3]int{1, 1, 1})
	for i := 0; i < 6; i++ {
		p.add(fmt.Sprintf("u%d", i), 0, 9-i, "unk")
	}
	require.Len(t, p.txmp.ReapMaxBytesMaxGas(-1, -1), 6)

	p = newClassPool(t, &[3]int{1, 1, 1})
	p.txmp.config.AsyncRecheck = false
	for i := 0; i < 3; i++ {
		p.add(fmt.Sprintf("s%d", i), 0, 9-i, "sdk")
		p.add(fmt.Sprintf("p%d", i), 0, 9-i, "pff")
	}
	require.Len(t, p.txmp.ReapMaxBytesMaxGas(-1, -1), 6)
}

// A class quota ends its sender's prefix: the reap never takes a later sequence after a tx
// it left out, and the pass does not count one either.
func TestReapClasses_SenderPrefix(t *testing.T) {
	p := newClassPool(t, &[3]int{2, 1, 1}) // quotas 2, 1, 1
	a0 := p.add("a", 0, 9, "sdk")
	m0 := p.add("m", 0, 8, "sdk")
	p.add("m", 1, 8, "sdk") // over the SDK quota: m's PFF behind it is not reapable
	p.add("m", 2, 8, "pff")
	p0 := p.add("p", 0, 1, "pff")
	gate := make(chan struct{})
	p.commit(1, gate)
	got := p.startReap(-1)
	feed(gate, 4)
	requireNoReap(t, got, "m's PFF must not count")
	feed(gate, 1)
	require.Equal(t, []string{a0, m0, p0}, p.names(requireReap(t, got, "p kept")))
	close(gate)
	waitForPass(p.txmp)
	require.Equal(t, []string{a0, m0, p0}, p.names(p.txmp.ReapMaxBytesMaxGas(-1, -1)))
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

// The pass rechecks whole signer sets, candidate sets first, each group in reap order, so
// every sender is rechecked contiguously and in sequence order.
func TestReapClasses_OrderKeepsSenderSequence(t *testing.T) {
	p := newClassPool(t, &[3]int{2, 2, 2})
	p.add("s0", 0, 9, "sdk")
	p.add("s0", 1, 9, "sdk")
	p.add("s1", 0, 8, "sdk")
	p.add("s1", 1, 8, "sdk")
	p.add("x", 0, 7, "sdk") // over the SDK quota, so x is not a candidate despite its PFF
	p.add("x", 1, 7, "pff")
	p.add("p0", 0, 5, "pff")
	p.add("p1", 0, 5, "pff")
	p.add("p2", 0, 4, "pff")
	p.commit(1, nil)
	waitForPass(p.txmp)
	require.Equal(t, []string{
		"s0/0/sdk", "s0/1/sdk", "p0/0/pff", "p1/0/pff",
		"s1/0/sdk", "s1/1/sdk", "x/0/sdk", "x/1/pff", "p2/0/pff",
	}, p.app.order)
}

// A reap returns once the pass has kept maxBytes, before the class quotas are met.
func TestReapClasses_StopsAtMaxBytes(t *testing.T) {
	p := newClassPool(t, nil)
	var want []string
	for s := 0; s < 4; s++ {
		want = append(want, p.add("a", s, 1, "sdk"))
	}
	gate := make(chan struct{})
	p.commit(1, gate)
	size := p.txmp.store.get(cmttypes.Tx(encodeClassTx(t, want[0], "sdk")).Key()).size()
	got := p.startReap(2 * size)
	feed(gate, 2)
	require.Equal(t, want[:2], p.names(requireReap(t, got, "maxBytes kept")))
	close(gate)
	waitForPass(p.txmp)
}

// Random pools: once its wait returns, the reap made during the pass equals the reap after
// the pass under the same caps. Exact with mixed-class senders when every candidate is kept,
// and with single-class senders of weight 1 when some are evicted.
func TestReapClasses_DuringPassEqualsAfter(t *testing.T) {
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	for _, tc := range []struct {
		name    string
		classes []string
		mixed   bool
		evict   int // in 1/5
	}{
		{"mixed senders, all kept", []string{"sdk", "sdk2", "sdk3", "pfb", "pff", "unk"}, true, 0},
		{"single-class senders, evictions", []string{"sdk", "pfb", "pff", "unk"}, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			early := 0
			const rounds = 100
			for round := 0; round < rounds; round++ {
				p := newClassPool(t, &[3]int{1 + rng.Intn(6), 1 + rng.Intn(4), 1 + rng.Intn(6)})
				total := 0
				for s := 0; s < 3+rng.Intn(15); s++ {
					prio, class := rng.Intn(10), tc.classes[rng.Intn(len(tc.classes))]
					for q := 0; q < 1+rng.Intn(4); q++ {
						if tc.mixed {
							class = tc.classes[rng.Intn(len(tc.classes))]
						}
						p.app.bad[p.add(fmt.Sprintf("r%d", s), q, prio, class)] = rng.Intn(5) < tc.evict
						total++
					}
				}
				gate := make(chan struct{})
				p.commit(1, gate)
				got := p.startReap(-1)
				var during []*cmttypes.CachedTx
				returned := false
				for k := 1; k <= total && !returned; k++ {
					gate <- struct{}{}
					if k < total { // tx k is processed once the pass reaches tx k+1
						require.Eventually(t, func() bool { return p.app.at.Load() == int32(k+1) }, 5*time.Second, time.Microsecond)
					}
					select {
					case during = <-got:
						returned = true
						if k < total {
							early++
						}
					case <-time.After(time.Millisecond):
					}
				}
				close(gate)
				if !returned {
					during = requireReap(t, got, "pass end")
				}
				waitForPass(p.txmp)
				require.Equal(t, p.names(p.txmp.ReapMaxBytesMaxGas(-1, -1)), p.names(during), "round %d", round)
			}
			t.Logf("%d of %d reaps returned before the pass end", early, rounds)
		})
	}
}

func TestReapClasses_Quotas(t *testing.T) {
	inf := int64(math.MaxInt64)
	require.Equal(t, [numClasses]int64{inf, inf, inf, inf}, classQuotas(cmttypes.BlockTxLimitAppVersion-1))
	require.Equal(t, [numClasses]int64{750, 250, 250, inf}, classQuotas(cmttypes.BlockTxLimitAppVersion))
}

func TestClassifyTx(t *testing.T) {
	send := anyMsg(sendURL, []byte{0x0a, 0x01, 'a'})
	pff := anyMsg(payForFibreURL, []byte{0x0a, 0x01, 'a'})
	exec := anyMsg(msgExecURL, pbBytes(pbBytes(pbBytes(pbBytes(nil, 1, []byte("grantee")), 2, send), 2, send), 2, send))
	b, err := squareshare.NewBlob(squareshare.MustNewV0Namespace([]byte("classes")), []byte("data"), 0, nil)
	require.NoError(t, err)
	blobTx, err := squaretx.MarshalBlobTx(txRaw(nil, anyMsg("/celestia.blob.v1.MsgPayForBlobs", nil)), b)
	require.NoError(t, err)

	type want struct {
		class  uint8
		weight int64
	}
	for name, c := range map[string]struct {
		tx   []byte
		want want
	}{
		"send":               {txRaw(nil, send), want{classSDK, 1}},
		"two sends":          {txRaw(nil, send, send), want{classSDK, 2}},
		"exec of 3":          {txRaw(nil, exec), want{classSDK, 4}},
		"pff":                {txRaw(nil, pff), want{classPFF, 1}},
		"pff mixed":          {txRaw(nil, send, pff), want{classUnknown, 1}},
		"two pffs":           {txRaw(nil, pff, pff), want{classUnknown, 1}},
		"blob tx":            {blobTx, want{classPFB, 1}},
		"blob tx, no blobs":  {pbBytes(pbBytes(nil, 1, txRaw(nil, send)), 3, []byte(squaretx.ProtoBlobTxTypeID)), want{classUnknown, 1}},
		"no messages":        {txRaw(nil), want{classUnknown, 1}},
		"garbage":            {[]byte{0xff, 0xff, 0xff}, want{classUnknown, 1}},
		"truncated":          {txRaw(nil, pff)[:10], want{classUnknown, 1}},
		"msg wrong wiretype": {pbBytes(nil, 1, protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 1)), want{classUnknown, 1}},
		// TxRaw body_bytes twice: protobuf keeps the last, and so does the classifier.
		"body twice": {append(txRaw(nil, send), pbBytes(nil, 1, pbBytes(nil, 1, pff))...), want{classPFF, 1}},
	} {
		class, weight := classifyTx(c.tx)
		require.Equal(t, c.want, want{class, weight}, name)
	}
}
