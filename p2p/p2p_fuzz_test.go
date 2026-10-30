package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/protoio"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p/conn"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

const (
	fuzzMaxInput = 64 << 10
	fuzzWatchdog = 10 * time.Second
	fuzzRecvCap  = 4096
	fuzzSentinel = byte(0x7e)
)

var (
	fuzzVictimKey   = ed25519.GenPrivKeyFromSecret([]byte("p2p fuzz victim"))
	fuzzAttackerKey = ed25519.GenPrivKeyFromSecret([]byte("p2p fuzz attacker"))
	fuzzVictimID    = PubKeyToID(fuzzVictimKey.PubKey())
	fuzzAttackerID  = PubKeyToID(fuzzAttackerKey.PubKey())
	fuzzLocalInfo   = DefaultNodeInfo{
		ProtocolVersion: defaultProtocolVersion,
		DefaultNodeID:   fuzzVictimID,
		ListenAddr:      "127.0.0.1:26656",
		Network:         "fuzz-net",
		Version:         "1.0.0",
		Channels:        []byte{0x01, 0x02},
		Moniker:         "victim",
	}
)

// needsDNS reports whether DefaultNodeInfo.Validate would resolve listenAddr
// as a host name. Such inputs are skipped so fuzzing makes no network calls.
func needsDNS(id ID, listenAddr string) bool {
	spl := strings.Split(removeProtocolIfDefined(IDAddressString(id, listenAddr)), "@")
	if len(spl) != 2 || validateID(ID(spl[0])) != nil {
		return false
	}
	host, _, err := net.SplitHostPort(spl[1])
	return err == nil && host != "" && net.ParseIP(host) == nil
}

// halfPipe is a net.Conn made of two io.Pipes so each side can close its
// write direction independently, like a TCP half-close. net.Pipe cannot, and
// its SetDeadline fails once the other end is closed, which a socket does not.
// Deadlines are no-ops; the harness uses a watchdog instead.
type halfPipe struct {
	*io.PipeReader
	*io.PipeWriter
}

func newHalfPipe() (a, b halfPipe) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return halfPipe{ar, aw}, halfPipe{br, bw}
}

func (p halfPipe) Close() error {
	_ = p.PipeWriter.Close()
	return p.PipeReader.Close()
}
func (halfPipe) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }
func (halfPipe) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2} }
func (halfPipe) SetDeadline(time.Time) error      { return nil }
func (halfPipe) SetReadDeadline(time.Time) error  { return nil }
func (halfPipe) SetWriteDeadline(time.Time) error { return nil }

// FuzzTransportUpgrade runs MultiplexTransport.upgrade (SecretConnection
// handshake, node-info exchange, Validate, ID checks and CompatibleWith) on a
// connection whose remote side is a real SecretConnection that sends a
// fuzz-chosen DefaultNodeInfo (or raw bytes). In structured mode the result
// must match what Validate, the ID checks and CompatibleWith predict.
//
// flags bit 0: send raw instead of the structured node info.
// flags bit 1: use the attacker's real node ID instead of id.
// flags bit 2: use our network and block version so CompatibleWith can pass.
func FuzzTransportUpgrade(f *testing.F) {
	f.Add(uint8(6), "", "127.0.0.1:26656", "", "1.0.0", []byte{1}, "peer", "on", "", uint64(0), []byte{})
	f.Add(uint8(6), "", "tcp://[::1]:1", "", "", []byte{1, 1}, "peer", "", "", uint64(0), []byte{})
	f.Add(uint8(6), "", "1.2.3.4:5", "", "\t", []byte{2}, " ", "x", "\x00", uint64(0), []byte{})
	f.Add(uint8(2), "", "0.0.0.0:0", "other", "", make([]byte, 17), "peer", "off", "rpc", uint64(9), []byte{})
	f.Add(uint8(4), string(fuzzVictimID), "127.0.0.1:1", "", "", []byte{1}, "peer", "", "", uint64(0), []byte{})
	f.Add(uint8(1), "", "", "", "", []byte{}, "", "", "", uint64(0), []byte{0x05, 0x0a, 0x03, 0x08, 0x01, 0x10})
	f.Add(uint8(1), "", "", "", "", []byte{}, "", "", "", uint64(0), []byte{0xff, 0xff, 0x03})

	f.Fuzz(func(t *testing.T, flags uint8, id, listenAddr, network, version string, channels []byte,
		moniker, txIndex, rpcAddr string, blockVer uint64, raw []byte,
	) {
		if len(id)+len(listenAddr)+len(network)+len(version)+len(channels)+len(moniker)+
			len(txIndex)+len(rpcAddr)+len(raw) > fuzzMaxInput {
			return
		}
		info := DefaultNodeInfo{
			ProtocolVersion: ProtocolVersion{P2P: blockVer >> 8, Block: blockVer, App: blockVer << 8},
			DefaultNodeID:   ID(id),
			ListenAddr:      listenAddr,
			Network:         network,
			Version:         version,
			Channels:        channels,
			Moniker:         moniker,
			Other:           DefaultNodeInfoOther{TxIndex: txIndex, RPCAddress: rpcAddr},
		}
		if flags&2 != 0 {
			info.DefaultNodeID = fuzzAttackerID
		}
		if flags&4 != 0 {
			info.Network = fuzzLocalInfo.Network
			info.ProtocolVersion.Block = fuzzLocalInfo.ProtocolVersion.Block
		}
		payload := raw
		if flags&1 == 0 {
			bz, err := protoio.MarshalDelimited(info.ToProto())
			if err != nil {
				t.Fatal(err)
			}
			payload = bz
		}
		var decoded tmp2p.DefaultNodeInfo
		if protoio.UnmarshalDelimited(payload, &decoded) == nil &&
			needsDNS(ID(decoded.DefaultNodeID), decoded.ListenAddr) {
			return
		}

		mt := NewMultiplexTransport(fuzzLocalInfo, NodeKey{PrivKey: fuzzVictimKey},
			conn.DefaultMConnConfig(), trace.NoOpTracer())

		local, remote := newHalfPipe()
		var timedOut atomic.Bool
		watchdog := time.AfterFunc(fuzzWatchdog, func() {
			timedOut.Store(true)
			_ = local.Close()
			_ = remote.Close()
		})
		done := make(chan struct{})
		go func() { // the attacker
			defer close(done)
			defer remote.PipeWriter.Close() // EOF for the victim's reads
			sc, err := conn.MakeSecretConnection(remote, fuzzAttackerKey)
			if err != nil {
				return
			}
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				var theirs tmp2p.DefaultNodeInfo
				_, _ = protoio.NewDelimitedReader(sc, MaxNodeInfoSize()).ReadMsg(&theirs)
			}()
			if len(payload) > 0 {
				_, _ = sc.Write(payload)
			}
			<-readDone
		}()

		sc, ni, err := mt.upgrade(local, nil)
		watchdog.Stop()
		_ = local.Close()
		<-done
		_ = remote.Close()
		if timedOut.Load() {
			t.Fatalf("upgrade did not finish within %v", fuzzWatchdog)
		}

		if err != nil {
			var rej ErrRejected
			if !errors.As(err, &rej) {
				t.Fatalf("upgrade returned %T, want ErrRejected: %v", err, err)
			}
		} else {
			defer sc.Close()
			if verr := ni.Validate(); verr != nil {
				t.Fatalf("accepted node info fails Validate: %v", verr)
			}
			if ni.ID() != fuzzAttackerID {
				t.Fatalf("accepted node info ID %v, connection ID %v", ni.ID(), fuzzAttackerID)
			}
			if cerr := fuzzLocalInfo.CompatibleWith(ni); cerr != nil {
				t.Fatalf("accepted incompatible node info: %v", cerr)
			}
			dni := ni.(DefaultNodeInfo)
			if len(dni.Channels) > maxNumChannels || strings.TrimSpace(dni.Moniker) == "" {
				t.Fatalf("accepted node info with bad channels/moniker: %+v", dni)
			}
			if _, aerr := ni.NetAddress(); aerr != nil {
				t.Fatalf("accepted node info without a valid NetAddress: %v", aerr)
			}
		}
		if flags&1 != 0 {
			return
		}

		// Structured mode: the outcome must match the individual checks.
		want := info.ToProto().Size() <= MaxNodeInfoSize() &&
			info.Validate() == nil &&
			info.ID() == fuzzAttackerID &&
			fuzzLocalInfo.CompatibleWith(info) == nil
		if (err == nil) != want {
			t.Fatalf("upgrade err=%v, model expects success=%v", err, want)
		}
		if err == nil {
			gotBz, _ := ni.(DefaultNodeInfo).ToProto().Marshal()
			wantBz, _ := info.ToProto().Marshal()
			if !bytes.Equal(gotBz, wantBz) {
				t.Fatalf("node info changed in transit: got %+v, sent %+v", ni, info)
			}
		}
	})
}

// fuzzReactor records every envelope delivered through BaseReactor's default
// processor.
type fuzzReactor struct {
	BaseReactor
	mtx      sync.Mutex
	got      []Envelope
	sentinel chan struct{}
}

func fuzzPrecheck(bz []byte) error {
	if len(bz) > 32 {
		return errors.New("too long")
	}
	return nil
}

func (r *fuzzReactor) GetChannels() []*conn.ChannelDescriptor {
	return []*conn.ChannelDescriptor{
		{ID: 0x01, Priority: 1, RecvMessageCapacity: fuzzRecvCap, MessageType: &tmp2p.Message{}},
		{
			ID: 0x02, Priority: 1, RecvMessageCapacity: fuzzRecvCap, MessageType: &tmp2p.Message{},
			RecvMessagePrecheck: fuzzPrecheck,
		},
		{ID: fuzzSentinel, Priority: 1, MessageType: &tmp2p.Message{}},
	}
}

func (r *fuzzReactor) Receive(e Envelope) {
	if e.ChannelID == fuzzSentinel {
		close(r.sentinel)
		return
	}
	r.mtx.Lock()
	r.got = append(r.got, e)
	r.mtx.Unlock()
}

type fuzzEnvelope struct {
	ch  byte
	msg []byte
}

// expectedDelivery is the model of unmarshalEnvelope + ProcessorWithReactor.
func expectedDelivery(e fuzzEnvelope) proto.Message {
	if e.ch != 0x01 && e.ch != 0x02 {
		return nil
	}
	if e.ch == 0x02 && fuzzPrecheck(e.msg) != nil {
		return nil
	}
	var m tmp2p.Message
	if proto.Unmarshal(e.msg, &m) != nil {
		return nil
	}
	inner, err := m.Unwrap()
	if err != nil {
		return nil
	}
	return inner
}

// FuzzPeerReceive sends fuzz-chosen messages from an authenticated remote
// peer through a running peer's MConnection into a BaseReactor (onReceive ->
// QueueUnprocessedEnvelope -> unmarshalEnvelope, RecvMessagePrecheck ->
// ProcessorWithReactor -> Receive). A model predicts exactly which envelopes
// reach Receive. A final envelope with a fuzz-chosen channel ID is queued
// directly to cover channels the reactor did not register.
func FuzzPeerReceive(f *testing.F) {
	f.Add([]byte{0x80, 0, 0x81, 0, 0x82}, uint8(1), []byte{0x0a, 0x00})
	f.Add([]byte{0x01, 0x00, 0x02, 0x0a, 0x00}, uint8(2), []byte{0x12, 0x00})
	f.Add([]byte{0xc1, 0x81, 3, 0x40, 0x7f, 0, 0, 0, 0, 0x50, 0x50}, uint8(0x7e), []byte{})
	f.Add([]byte{0x02, 0x00, 0x01}, uint8(0x33), []byte{0x0a, 0x00})    // unknown channel
	f.Add([]byte{0x00, 0x20, 0x00, 0x80}, uint8(1), []byte{0x12, 0x02}) // over capacity

	f.Fuzz(func(t *testing.T, msgs []byte, directCh uint8, direct []byte) {
		if len(msgs)+len(direct) > fuzzMaxInput {
			return
		}
		take := func(n int) []byte {
			b := append([]byte{}, msgs[:min(n, len(msgs))]...)
			msgs = msgs[len(b):]
			return append(b, make([]byte, n-len(b))...)
		}

		// Build the messages and the expected deliveries.
		var (
			sent     []fuzzEnvelope
			expected []proto.Message
			stopped  bool
		)
		for len(msgs) > 0 && len(sent) < 64 {
			op := msgs[0]
			msgs = msgs[1:]
			e := fuzzEnvelope{ch: []byte{0x01, 0x02, 0x03}[int(op&0x3f)%3]}
			switch {
			case op&0x80 != 0 && op&0x40 != 0:
				e.msg, _ = proto.Marshal(&tmp2p.Message{Sum: &tmp2p.Message_PexRequest{PexRequest: &tmp2p.PexRequest{}}})
			case op&0x80 != 0:
				addrs := make([]tmp2p.NetAddress, int(take(1)[0])%8)
				for i := range addrs {
					b := take(7)
					addrs[i] = tmp2p.NetAddress{
						ID:   fmt.Sprintf("%x", bytes.Repeat(b[:1], int(b[0])%21)),
						IP:   net.IP(b[1:5]).String(),
						Port: uint32(binary.BigEndian.Uint16(b[5:7])),
					}
				}
				e.msg, _ = proto.Marshal(&tmp2p.Message{Sum: &tmp2p.Message_PexAddrs{PexAddrs: &tmp2p.PexAddrs{Addrs: addrs}}})
			default:
				n := int(binary.BigEndian.Uint16(take(2))) % (fuzzRecvCap + 1024)
				e.msg = take(n)
			}
			sent = append(sent, e)
			if stopped {
				continue
			}
			if e.ch == 0x03 || len(e.msg) > fuzzRecvCap {
				stopped = true // MConnection drops the peer
				continue
			}
			if m := expectedDelivery(e); m != nil {
				expected = append(expected, m)
			}
		}
		if directCh == fuzzSentinel {
			directCh--
		}
		directEnv := fuzzEnvelope{ch: directCh, msg: direct}
		if m := expectedDelivery(directEnv); m != nil {
			expected = append(expected, m)
		}

		// Set up a running peer with an authenticated remote side.
		local, remote := net.Pipe()
		var victimSC, attackerSC *conn.SecretConnection
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if attackerSC, err = conn.MakeSecretConnection(remote, fuzzAttackerKey); err != nil {
				panic(err)
			}
		}()
		victimSC, err := conn.MakeSecretConnection(local, fuzzVictimKey)
		if err != nil {
			t.Fatal(err)
		}
		wg.Wait()

		r := &fuzzReactor{sentinel: make(chan struct{})}
		r.BaseReactor = *NewBaseReactor("fuzz", r)
		defer r.OnStop()
		chDescs := r.GetChannels()[:2]
		reactorsByCh := map[byte]Reactor{0x01: r, 0x02: r}

		peerErr := make(chan interface{}, 1)
		cfg := conn.DefaultMConnConfig()
		cfg.SendRate, cfg.RecvRate = 0, 0
		p := newPeer(newPeerConn(false, false, victimSC, nil), cfg, fuzzLocalInfo, reactorsByCh, nil,
			chDescs, func(_ Peer, reason interface{}, _ string) {
				select {
				case peerErr <- reason:
				default:
				}
			}, newMetricsLabelCache())
		if err := p.Start(); err != nil {
			t.Fatal(err)
		}
		defer p.Stop() //nolint:errcheck

		wg.Add(1)
		go func() { // the attacker writes all messages, then hangs up
			defer wg.Done()
			defer remote.Close()
			w := protoio.NewDelimitedWriter(attackerSC)
			for _, e := range sent {
				for i := 0; ; i += 1024 {
					end := min(i+1024, len(e.msg))
					pkt := &tmp2p.PacketMsg{ChannelID: int32(e.ch), EOF: end == len(e.msg), Data: e.msg[i:end]}
					if _, err := w.WriteMsg(&tmp2p.Packet{Sum: &tmp2p.Packet_PacketMsg{PacketMsg: pkt}}); err != nil {
						return
					}
					if pkt.EOF {
						break
					}
				}
			}
		}()

		var errArg interface{}
		select {
		case errArg = <-peerErr:
		case <-time.After(fuzzWatchdog):
			t.Fatalf("peer connection did not end within %v", fuzzWatchdog)
		}
		if err, ok := errArg.(error); ok && strings.HasPrefix(err.Error(), "recovered from panic") {
			t.Fatalf("peer recv path panicked: %v", err)
		}
		_ = local.Close()
		wg.Wait()

		r.QueueUnprocessedEnvelope(UnprocessedEnvelope{ChannelID: directEnv.ch, Src: p, Message: directEnv.msg})
		sentinel, _ := proto.Marshal(&tmp2p.Message{Sum: &tmp2p.Message_PexRequest{PexRequest: &tmp2p.PexRequest{}}})
		r.QueueUnprocessedEnvelope(UnprocessedEnvelope{ChannelID: fuzzSentinel, Src: p, Message: sentinel})
		select {
		case <-r.sentinel:
		case <-time.After(fuzzWatchdog):
			t.Fatalf("reactor did not process the queue within %v", fuzzWatchdog)
		}

		r.mtx.Lock()
		defer r.mtx.Unlock()
		if len(r.got) != len(expected) {
			t.Fatalf("Receive got %d envelopes, expected %d (peer error: %v)", len(r.got), len(expected), errArg)
		}
		for i, e := range r.got {
			if e.Src != p || !proto.Equal(e.Message, expected[i]) {
				t.Fatalf("envelope %d: got %v from %v, expected %v", i, e.Message, e.Src, expected[i])
			}
		}
	})
}
