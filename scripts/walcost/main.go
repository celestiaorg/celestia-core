// walcost measures what the consensus goroutine waits on when writing the WAL in the worst case
// (32 MiB blocks, 100 validators), framing records like consensus.WALEncoder on an autofile.Group.
// Usage: go run ./scripts/walcost -arm B0,B1,B2 -heights 500 -dir /data/walcost (-h lists the arms).
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/cometbft/cometbft/consensus"
	auto "github.com/cometbft/cometbft/libs/autofile"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtcrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

const (
	partSize = 64 << 10 // types.BlockPartSizeBytes
	nParts   = 512      // 32 MiB block
	nVals    = 100

	walHeight = 1 << 22
)

type kind int

const (
	nonProposer kind = iota
	proposer
	cachedParts
	fullWAL
	contention
	rawDisk
)

// spec is one arm; lock is where the lock record goes: 1 before signVote's flush, 2 after signing.
type spec struct {
	kind                         kind
	lock                         int
	peerProposal, direct, second bool
	bulkSync, plain              bool
	desc                         string
}

var arms = map[string]spec{
	"B0":  {kind: nonProposer, desc: "non-proposer height today: own prevote, own precommit, EndHeight"},
	"B1":  {kind: nonProposer, lock: 1, desc: "B0 + lock record written before signVote's FlushAndSync"},
	"B1b": {kind: nonProposer, lock: 2, desc: "B0 + lock record written after signing, before the precommit WriteSync (enterPrecommit today)"},
	"B2":  {kind: nonProposer, lock: 1, peerProposal: true, desc: "B1 + peer proposal record (buffered) before the prevote"},
	"B2b": {kind: nonProposer, lock: 2, peerProposal: true, desc: "B1b + peer proposal record (buffered) before the prevote"},
	"B3":  {kind: proposer, desc: "proposer height: proposal WriteSync, 512 buffered part writes, prevote flushes them"},
	"B4":  {kind: cachedParts, desc: "non-proposer with 512 cached parts written before the prevote"},
	"B5":  {kind: contention, desc: "8 vote WriteSyncs 5 ms apart while 32 MiB streams unsynced to a separate Group"},
	"B5b": {kind: contention, direct: true, desc: "B5 with the bulk file opened O_DIRECT"},
	"B5p": {kind: contention, plain: true, desc: "B5 with a plain bulk file (autofile rotation fsyncs the Group head)"},
	"B5d": {kind: contention, second: true, desc: "B5 with the bulk Group under -bulkdir (second disk)"},
	"B5s": {kind: contention, bulkSync: true, desc: "B5 with the bulk Group fsynced at the end (kernel writes back at once)"},
	"B6":  {kind: fullWAL, desc: "full WAL non-proposer height: peer proposal, parts and 200 votes"},
	"RAW": {kind: rawDisk, desc: "1 GiB sequential write + fsync"},
}

var (
	crc32c   = crc32.MakeTable(crc32.Castagnoli)
	heights  = flag.Int("heights", 500, "measured heights per arm")
	warmup   = flag.Int("warmup", 5, "unmeasured heights per arm")
	interval = flag.Duration("interval", 100*time.Millisecond, "time slot per height")
	block    = flag.Int("block", 1, "heights per arm before switching (ABAB); >1 also syncs the disks between blocks")
	dir      = flag.String("dir", "", "WAL directory (required); ARM@dir overrides it per arm")
	bulkDir  = flag.String("bulkdir", "", "bulk Group directory for B5d")
	oDirect  = flag.Bool("odirect", false, "open every B5 bulk file O_DIRECT")
	samples  = flag.String("samples", "", "write every sample as a JSON line to this file")
)

// rec builds the proto records the consensus WAL holds, encoded exactly like WALEncoder.
type rec struct{ buf []byte }

func (r *rec) encode(m *cmtcons.WALMessage) []byte {
	pv := cmtcons.TimedWALMessage{Time: time.Now(), Msg: m}
	n := pv.Size()
	if cap(r.buf) < 8+n {
		r.buf = make([]byte, 8+n)
	}
	r.buf = r.buf[:8+n]
	if _, err := pv.MarshalTo(r.buf[8:]); err != nil {
		panic(err)
	}
	binary.BigEndian.PutUint32(r.buf[0:4], crc32.Checksum(r.buf[8:], crc32c))
	binary.BigEndian.PutUint32(r.buf[4:8], uint32(n))
	return r.buf
}

var (
	blockID = cmtproto.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: cmtproto.PartSetHeader{Total: nParts, Hash: cmtrand.Bytes(32)}}
	partBuf = cmtrand.Bytes(partSize)
	aunts   = [][]byte{cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32)}
	valAddr = cmtrand.Bytes(20)
	sig     = cmtrand.Bytes(64)
	peerID  = strings.Repeat("ab", 20)
)

func msgInfo(m cmtcons.Message, peer string) *cmtcons.WALMessage {
	return &cmtcons.WALMessage{Sum: &cmtcons.WALMessage_MsgInfo{MsgInfo: &cmtcons.MsgInfo{Msg: m, PeerID: peer}}}
}

func step(name string) *cmtcons.WALMessage {
	return &cmtcons.WALMessage{Sum: &cmtcons.WALMessage_EventDataRoundState{
		EventDataRoundState: &cmtproto.EventDataRoundState{Height: walHeight, Step: name}}}
}

func timeout() *cmtcons.WALMessage {
	return &cmtcons.WALMessage{Sum: &cmtcons.WALMessage_TimeoutInfo{
		TimeoutInfo: &cmtcons.TimeoutInfo{Duration: time.Second, Height: walHeight, Step: 1}}}
}

func endHeight() *cmtcons.WALMessage {
	return &cmtcons.WALMessage{Sum: &cmtcons.WALMessage_EndHeight{EndHeight: &cmtcons.EndHeight{Height: walHeight}}}
}

func vote(t cmtproto.SignedMsgType, peer string) *cmtcons.WALMessage {
	v := &cmtproto.Vote{Type: t, Height: walHeight, BlockID: blockID, Timestamp: time.Now(),
		ValidatorAddress: valAddr, ValidatorIndex: 42, Signature: sig}
	return msgInfo(cmtcons.Message{Sum: &cmtcons.Message_Vote{Vote: &cmtcons.Vote{Vote: v}}}, peer)
}

func proposal(peer string) *cmtcons.WALMessage {
	p := cmtproto.Proposal{Type: cmtproto.ProposalType, Height: walHeight, PolRound: -1, BlockID: blockID,
		Timestamp: time.Now(), Signature: sig}
	return msgInfo(cmtcons.Message{Sum: &cmtcons.Message_Proposal{Proposal: &cmtcons.Proposal{Proposal: p}}}, peer)
}

func lockInfo() *cmtcons.WALMessage {
	id, err := types.BlockIDFromProto(&blockID)
	if err != nil {
		panic(err)
	}
	m, err := consensus.WALToProto(consensus.LockInfo{Height: walHeight, BlockID: *id, BlockTime: time.Now()})
	if err != nil {
		panic(err)
	}
	return m
}

func part(i int, peer string) *cmtcons.WALMessage {
	p := cmtproto.Part{Index: uint32(i), Bytes: partBuf,
		Proof: cmtcrypto.Proof{Total: nParts, Index: int64(i), LeafHash: aunts[0], Aunts: aunts}}
	return msgInfo(cmtcons.Message{Sum: &cmtcons.Message_BlockPart{BlockPart: &cmtcons.BlockPart{Height: walHeight, Part: p}}}, peer)
}

// wal is a consensus WAL: an autofile.Group with BaseWAL's 2 s flush ticker.
type wal struct {
	g    *auto.Group
	enc  rec
	quit chan struct{}
	wg   sync.WaitGroup
}

func openGroup(d string) *auto.Group {
	if err := os.MkdirAll(d, 0o700); err != nil {
		panic(err)
	}
	g, err := auto.OpenGroup(filepath.Join(d, "wal"))
	if err != nil {
		panic(err)
	}
	if err := g.Start(); err != nil {
		panic(err)
	}
	return g
}

func openWAL(d string) *wal {
	w := &wal{g: openGroup(d), quit: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				w.sync()
			case <-w.quit:
				return
			}
		}
	}()
	return w
}

func (w *wal) write(m *cmtcons.WALMessage) {
	if _, err := w.g.Write(w.enc.encode(m)); err != nil {
		panic(err)
	}
}

func (w *wal) sync() {
	if err := w.g.FlushAndSync(); err != nil {
		panic(err)
	}
}

func (w *wal) close() {
	close(w.quit)
	w.wg.Wait()
	_ = w.g.Stop()
	w.g.Close()
}

type sample map[string]time.Duration

func timed(s sample, metric string, f func()) {
	t0 := time.Now()
	f()
	s[metric] += time.Since(t0)
}

// arm runs one height of a scenario on its own WAL.
type arm struct {
	label string
	spec
	dir, bulkDir string
	w            *wal
	bulk         *auto.Group // bulk block Group (B5, B5d, B5s)
	file         *bulkFile   // bulk block store as a plain file (B5p) or O_DIRECT (B5b, -odirect)
	bulkEnc      rec
	votes        []time.Duration // per-vote samples of the last B5 height
}

func newArm(token string) *arm {
	name, d, _ := strings.Cut(token, "@")
	sp, ok := arms[name]
	if !ok {
		fatalf("unknown arm %q", name)
	}
	if d == "" {
		d = *dir
	}
	d = filepath.Join(d, "walcost-"+strings.NewReplacer("/", "_", "@", "_").Replace(token))
	_ = os.RemoveAll(d)
	a := &arm{label: token, spec: sp, dir: d}
	if sp.kind == rawDisk {
		if err := os.MkdirAll(d, 0o700); err != nil {
			panic(err)
		}
		return a
	}
	a.w = openWAL(d)
	if sp.kind == contention {
		bd := filepath.Join(d, "bulk")
		if sp.second {
			if *bulkDir == "" {
				fatalf("B5d needs -bulkdir")
			}
			bd = filepath.Join(*bulkDir, filepath.Base(d)+"-bulk")
			_ = os.RemoveAll(bd)
		}
		a.bulkDir = bd
		if sp.direct || sp.plain || (*oDirect && !sp.bulkSync) {
			a.file = openBulkFile(bd, !sp.plain)
		} else {
			a.bulk = openGroup(bd)
		}
	}
	return a
}

func (a *arm) close() {
	defer os.RemoveAll(a.dir)
	defer os.RemoveAll(a.bulkDir)
	if a.kind == rawDisk {
		return
	}
	a.w.close()
	if a.bulk != nil {
		_ = a.bulk.Stop()
		a.bulk.Close()
	}
	if a.file != nil {
		a.file.f.Close()
	}
}

func (a *arm) height() sample {
	s := sample{}
	if a.kind == contention {
		a.contend(s)
		return s
	}
	w := a.w
	w.write(step("RoundStepNewHeight"))
	w.write(timeout())
	w.write(step("RoundStepNewRound"))
	switch a.kind {
	case nonProposer:
		w.write(step("RoundStepPropose"))
		if a.peerProposal {
			w.write(proposal(peerID))
		}
		a.prevote(s)
		a.precommit(s)
		a.endHeight(s)
	case proposer:
		timed(s, "propose", func() {
			w.sync() // decideProposal flushes before signing
			w.write(step("RoundStepPropose"))
			w.write(proposal(""))
			w.sync()
		})
		timed(s, "parts", func() {
			for i := 0; i < nParts; i++ {
				w.write(part(i, ""))
			}
		})
		a.prevote(s)
		a.precommit(s)
		a.endHeight(s)
	case cachedParts:
		w.write(step("RoundStepPropose"))
		timed(s, "parts", func() {
			for i := 0; i < nParts; i++ {
				w.write(part(i, ""))
			}
		})
		a.prevote(s)
		a.precommit(s)
		a.endHeight(s)
	case fullWAL:
		w.write(step("RoundStepPropose"))
		timed(s, "parts", func() {
			w.write(proposal(peerID))
			for i := 0; i < nParts; i++ {
				w.write(part(i, peerID))
			}
		})
		a.prevote(s)
		timed(s, "peervotes", func() {
			for i := 0; i < nVals; i++ {
				w.write(vote(cmtproto.PrevoteType, peerID))
			}
		})
		a.precommit(s)
		timed(s, "peervotes", func() {
			for i := 0; i < nVals; i++ {
				w.write(vote(cmtproto.PrecommitType, peerID))
			}
		})
		a.endHeight(s)
	}
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	s["height"] = sum
	return s
}

// prevote: signVote flushes the WAL before signing, then the vote is a WriteSync.
func (a *arm) prevote(s sample) {
	timed(s, "prevote", func() {
		a.w.sync()
		a.w.write(step("RoundStepPrevote"))
		a.w.write(vote(cmtproto.PrevoteType, ""))
		a.w.sync()
	})
}

func (a *arm) precommit(s sample) {
	timed(s, "precommit", func() {
		if a.lock == 1 {
			a.w.write(lockInfo())
		}
		a.w.sync()
		if a.lock == 2 { // enterPrecommit writes the record after signAddVote
			a.w.write(lockInfo())
		}
		a.w.write(step("RoundStepPrecommit"))
		a.w.write(vote(cmtproto.PrecommitType, ""))
		a.w.sync()
	})
	a.w.write(step("RoundStepCommit"))
}

func (a *arm) endHeight(s sample) {
	timed(s, "endheight", func() {
		a.w.write(endHeight())
		a.w.sync()
	})
}

// contend streams one block to the bulk block file in the background while the
// consensus goroutine does 8 vote WriteSyncs 5 ms apart (the "vote" samples).
func (a *arm) contend(s sample) {
	done := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		for i := 0; i < nParts; i++ {
			b := a.bulkEnc.encode(part(i, ""))
			if a.file != nil {
				a.file.write(b)
			} else if _, err := a.bulk.Write(b); err != nil {
				panic(err)
			}
		}
		if a.bulkSync {
			if err := a.bulk.FlushAndSync(); err != nil {
				panic(err)
			}
		}
		done <- time.Since(t0)
	}()
	var votes []time.Duration
	for i := 0; i < 8; i++ {
		t0 := time.Now()
		a.w.write(vote(cmtproto.PrevoteType, ""))
		a.w.sync()
		votes = append(votes, time.Since(t0))
		time.Sleep(5 * time.Millisecond)
	}
	s["bulk"] = <-done
	a.votes = votes
}

// bulkFile appends records, never fsynced, wrapping at 1 GiB; with O_DIRECT they are padded to 4 KiB.
type bulkFile struct {
	f      *os.File
	off    int64
	buf    []byte
	direct bool
}

func openBulkFile(d string, direct bool) *bulkFile {
	odirect, ok := map[string]int{"amd64": 0x4000, "386": 0x4000, "arm64": 0x10000, "arm": 0x10000}[runtime.GOARCH]
	if !direct {
		odirect = 0
	} else if runtime.GOOS != "linux" || !ok {
		fatalf("O_DIRECT needs linux on amd64/arm64")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		panic(err)
	}
	f, err := os.OpenFile(filepath.Join(d, "blocks"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC|odirect, 0o600)
	if err != nil {
		panic(err)
	}
	raw := make([]byte, 2*partSize+8192)
	pad := int(4096 - uintptr(unsafe.Pointer(&raw[0]))%4096)
	return &bulkFile{f: f, buf: raw[pad%4096:], direct: direct}
}

func (d *bulkFile) write(rec []byte) {
	b := rec
	if d.direct {
		n := (len(rec) + 4095) &^ 4095
		copy(d.buf, rec)
		clear(d.buf[len(rec):n])
		b = d.buf[:n]
	}
	if d.off+int64(len(b)) > 1<<30 {
		d.off = 0
	}
	if _, err := d.f.WriteAt(b, d.off); err != nil {
		panic(err)
	}
	d.off += int64(len(b))
}

func raw(d string) map[string]any {
	f, err := os.Create(filepath.Join(d, "raw"))
	if err != nil {
		panic(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	buf := cmtrand.Bytes(1 << 20)
	t0 := time.Now()
	for i := 0; i < 1024; i++ {
		if _, err := f.Write(buf); err != nil {
			panic(err)
		}
	}
	if err := f.Sync(); err != nil {
		panic(err)
	}
	el := time.Since(t0)
	return map[string]any{"arm": "RAW", "metric": "seq_write_fsync_1GiB", "ms": ms(el), "mib_s": math.Round(1024 / el.Seconds())}
}

func ms(d time.Duration) float64 { return math.Round(float64(d.Microseconds())) / 1000 }

func pct(s []time.Duration, p float64) time.Duration {
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(i, 0)]
}

func fatalf(f string, args ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", args...)
	os.Exit(2)
}

func main() {
	armList := flag.String("arm", "B0", "comma-separated arms, interleaved; ARM@dir runs an arm in another directory")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: walcost -arm B0,B1 -dir DIR [flags]\narms:")
		names := make([]string, 0, len(arms))
		for n := range arms {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(os.Stderr, "  %-4s %s\n", n, arms[n].desc)
		}
		flag.PrintDefaults()
	}
	flag.Parse()
	if *dir == "" {
		flag.Usage()
		os.Exit(2)
	}
	host, _ := os.Hostname()
	out := json.NewEncoder(os.Stdout)
	var sf *json.Encoder
	if *samples != "" {
		f, err := os.OpenFile(*samples, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		sf = json.NewEncoder(f)
	}

	var as []*arm
	for _, t := range strings.Split(*armList, ",") {
		a := newArm(t)
		if a.kind == rawDisk {
			r := raw(a.dir)
			r["host"], r["label"] = host, t
			_ = out.Encode(r)
			a.close()
			continue
		}
		as = append(as, a)
	}
	if len(as) == 0 {
		return
	}
	got := map[string]map[string][]time.Duration{}
	for _, a := range as {
		got[a.label] = map[string][]time.Duration{}
	}
	total := *warmup + *heights
	start := time.Now()
	slot := 0
	for h := 0; h < total; h += *block {
		if *block > 1 && h > 0 {
			_ = exec.Command("sync").Run() // portable sync(2)
		}
		for k := range as {
			a := as[(k + h / *block)%len(as)] // rotate the order so no arm always runs first
			for j := h; j < min(h+*block, total); j++ {
				if *interval > 0 {
					time.Sleep(time.Until(start.Add(time.Duration(slot) * *interval)))
				}
				slot++
				s := a.height()
				if j < *warmup {
					continue
				}
				for m, d := range s {
					got[a.label][m] = append(got[a.label][m], d)
					if sf != nil {
						_ = sf.Encode(map[string]any{"host": host, "arm": a.label, "metric": m, "h": j, "us": d.Microseconds()})
					}
				}
				if len(a.votes) > 0 {
					got[a.label]["vote"] = append(got[a.label]["vote"], a.votes...)
				}
				if sf != nil {
					for _, d := range a.votes {
						_ = sf.Encode(map[string]any{"host": host, "arm": a.label, "metric": "vote", "h": j, "us": d.Microseconds()})
					}
				}
			}
		}
	}
	for _, a := range as {
		a.close()
		metrics := make([]string, 0, len(got[a.label]))
		for m := range got[a.label] {
			metrics = append(metrics, m)
		}
		sort.Strings(metrics)
		for _, m := range metrics {
			d := got[a.label][m]
			sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
			var sum time.Duration
			for _, x := range d {
				sum += x
			}
			_ = out.Encode(map[string]any{"host": host, "arm": a.label, "metric": m, "n": len(d),
				"p50_ms": ms(pct(d, .50)), "p90_ms": ms(pct(d, .90)), "p99_ms": ms(pct(d, .99)),
				"max_ms": ms(d[len(d)-1]), "mean_ms": ms(sum / time.Duration(len(d))),
				"interval": interval.String(), "block": *block, "odirect": *oDirect})
		}
	}
}
