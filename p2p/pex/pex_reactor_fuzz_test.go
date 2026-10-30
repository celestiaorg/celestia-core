package pex

import (
	"encoding/binary"
	"encoding/hex"
	"hash/fnv"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

// fuzzIDs are valid node IDs; a few fixed IDs make duplicates, MarkGood and
// MarkBad hit addresses that are actually in the book.
var fuzzIDs = func() []p2p.ID {
	ids := make([]p2p.ID, 8)
	for i := range ids {
		ids[i] = p2p.ID(strings.Repeat(hex.EncodeToString([]byte{byte(i + 1)}), p2p.IDByteLength))
	}
	return ids
}()

// fuzzIPs covers public, private, loopback, link-local, reserved and
// documentation ranges for both address families.
var fuzzIPs = []string{
	"8.8.8.8", "1.2.3.4", "127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1",
	"169.254.1.1", "0.0.0.0", "255.255.255.255", "100.64.0.1", "198.18.0.1",
	"::1", "::", "fc00::1", "fe80::1", "2001:db8::1", "2001:470::1", "2002:102:304::1",
	"2001::1", "64:ff9b::102:304", "fd87:d87e:eb43::1", "2606:4700::1111", "::ffff:8.8.8.8",
}

type fuzzInput struct{ b []byte }

func (r *fuzzInput) empty() bool { return len(r.b) == 0 }

func (r *fuzzInput) u8() byte {
	if len(r.b) == 0 {
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *fuzzInput) take(n int) []byte {
	if n > len(r.b) {
		n = len(r.b)
	}
	out := append([]byte{}, r.b[:n]...)
	r.b = r.b[n:]
	return out
}

func (r *fuzzInput) u32() uint32 {
	var buf [4]byte
	copy(buf[:], r.take(4))
	return binary.BigEndian.Uint32(buf[:])
}

func (r *fuzzInput) netAddress() tmp2p.NetAddress {
	var pb tmp2p.NetAddress
	switch b := r.u8(); b % 8 {
	case 0:
		pb.ID = string(r.take(int(r.u8() % 48))) // usually invalid
	case 1:
		pb.ID = string(fuzzPrivateID)
	default:
		pb.ID = string(fuzzIDs[int(b>>3)%len(fuzzIDs)])
	}
	switch b := r.u8(); b % 4 {
	case 0:
		pb.IP = net.IP(r.take(4)).String()
	case 1:
		pb.IP = net.IP(r.take(16)).String()
	case 2:
		pb.IP = string(r.take(int(r.u8() % 48))) // usually unparsable
	default:
		pb.IP = fuzzIPs[int(b>>2)%len(fuzzIPs)]
	}
	if b := r.u8(); b&0x80 != 0 {
		pb.Port = r.u32()
	} else {
		pb.Port = 26656 + uint32(b)
	}
	return pb
}

var (
	fuzzPrivateID = p2p.ID(strings.Repeat("ee", p2p.IDByteLength))
	// fuzzOurAddr is fuzzIDs[7] at fuzzIPs[0] with the default port, which
	// netAddress emits for the bytes {58, 3, 0}.
	fuzzOurAddr = func() *p2p.NetAddress {
		addr := p2p.NewNetAddressIPPort(net.ParseIP(fuzzIPs[0]), 26656)
		addr.ID = fuzzIDs[7]
		return addr
	}()
)

// fuzzPexPeer is a running mock peer with a fixed ID and node info that
// records what the reactor sends to it.
type fuzzPexPeer struct {
	*p2pmock.Peer
	addr *p2p.NetAddress
	sent []proto.Message
}

func newFuzzPexPeer(i int) *fuzzPexPeer {
	ip := net.IPv4(9, 9, 9, byte(i+1))
	addr := p2p.NewNetAddressIPPort(ip, 26656)
	addr.ID = p2p.ID(strings.Repeat(hex.EncodeToString([]byte{byte(0xa0 + i)}), p2p.IDByteLength))
	return &fuzzPexPeer{Peer: p2pmock.NewPeer(ip), addr: addr}
}

func (p *fuzzPexPeer) ID() p2p.ID                  { return p.addr.ID }
func (p *fuzzPexPeer) SocketAddr() *p2p.NetAddress { return p.addr }

func (p *fuzzPexPeer) NodeInfo() p2p.NodeInfo {
	return p2p.DefaultNodeInfo{DefaultNodeID: p.addr.ID, ListenAddr: p.addr.DialString()}
}

func (p *fuzzPexPeer) Send(e p2p.Envelope) bool {
	p.sent = append(p.sent, e.Message)
	return true
}

// newFuzzAddrBook returns an address book whose bucket placement does not
// depend on random keys, so inputs replay the same way.
func newFuzzAddrBook(dir string, strict bool) *addrBook {
	book := NewAddrBook(filepath.Join(dir, "addrbook.json"), strict).(*addrBook)
	book.key = strings.Repeat("ab", 12)
	book.hasher = fnv.New64a() // unkeyed, unlike the production hasher
	book.rand.Seed(1)
	book.SetLogger(log.NewNopLogger())
	book.AddOurAddress(fuzzOurAddr)
	book.AddPrivateIDs([]string{string(fuzzPrivateID)})
	return book
}

// checkAddrBook asserts the book's counters, indexes and buckets agree and
// that nothing invalid, banned, private, ours or (when strict) non-routable
// was stored.
func checkAddrBook(t *testing.T, book *addrBook) {
	require.Equal(t, len(book.addrLookup), book.size(), "size counters disagree with lookup")
	require.LessOrEqual(t, book.size(), newBucketCount*(newBucketSize+1)+oldBucketCount*(oldBucketSize+1))
	for id, ka := range book.addrLookup {
		require.Equal(t, id, ka.ID())
		require.NoError(t, ka.Addr.Valid())
		require.NotContains(t, book.badPeers, id)
		require.NotEqual(t, fuzzPrivateID, id)
		require.NotEqual(t, fuzzOurAddr.String(), ka.Addr.String())
		if book.routabilityStrict {
			require.True(t, ka.Addr.Routable(), "non-routable address %v stored", ka.Addr)
		}
		require.NotEmpty(t, ka.Buckets, "address %v is in no bucket", ka.Addr)
		if ka.isOld() {
			require.Len(t, ka.Buckets, 1)
		} else {
			require.LessOrEqual(t, len(ka.Buckets), maxNewBucketsPerAddress)
		}
		for _, idx := range ka.Buckets {
			require.Same(t, ka, book.getBucket(ka.BucketType, idx)[ka.Addr.String()])
		}
	}
	for _, buckets := range [][]map[string]*knownAddress{book.bucketsNew, book.bucketsOld} {
		for _, bucket := range buckets {
			require.LessOrEqual(t, len(bucket), newBucketSize+1)
			for _, ka := range bucket {
				require.Same(t, ka, book.addrLookup[ka.ID()])
			}
		}
	}
}

// FuzzPexReactor drives the PEX reactor and its address book with a sequence
// of peer messages (structured PexAddrs lists, PexRequests and raw envelope
// bytes) interleaved with the book operations the switch performs. Unsolicited
// or malformed address lists must stop the peer, request floods are rate
// limited, responses fit the message cap, and the book stays bounded and
// internally consistent with only valid (routable, if strict) addresses.
func FuzzPexReactor(f *testing.F) {
	// addr encodes a known ID and one of fuzzIPs with the default port.
	addr := func(id, ip int) []byte { return []byte{byte(2 + 8*id), byte(3 + 4*ip), 0} }
	list := func(entries ...[]byte) []byte {
		n := 3
		for _, e := range entries {
			n += len(e)
		}
		out := make([]byte, 0, n)
		out = append(out, 1, 0, byte(len(entries)))
		for _, e := range entries {
			out = append(out, e...)
		}
		return out
	}
	mixed := list(addr(0, 1), addr(1, 21), addr(2, 2), addr(3, 3), addr(4, 0), addr(0, 1), addr(5, 22), addr(6, 7))
	f.Add(byte(1), append([]byte{0, 0}, mixed...))                                            // solicited, strict book
	f.Add(byte(0), append([]byte{0, 0}, mixed...))                                            // non-strict book
	f.Add(byte(1), mixed)                                                                     // unsolicited
	f.Add(byte(1), append([]byte{0, 1}, list([]byte{0, 3, 1, 'z', 7, 0})...))                 // invalid ID
	f.Add(byte(1), append([]byte{0, 0}, list([]byte{2, 2, 3, 'x', 'y', 'z', 0})...))          // unparsable IP
	f.Add(byte(1), append([]byte{0, 0}, list([]byte{2, 7, 0x80, 0xff, 0xff, 0xff, 0xff})...)) // port overflow
	f.Add(byte(1), []byte{2, 0, 2, 0, 2, 0, 3, 0, 2, 0})                                      // request flood, then reset
	f.Add(byte(1), append([]byte{0, 0}, list(addr(7, 0), addr(4, 1))...))                     // our own address, solicited
	f.Add(byte(1), append(append([]byte{0, 0}, mixed...), 4, 0, 0, 2, 0, 5, 0, 10, 7, 0, 6, 0, 50))
	for _, msg := range []p2p.Wrapper{
		&tmp2p.PexRequest{},
		&tmp2p.PexAddrs{Addrs: []tmp2p.NetAddress{{ID: string(fuzzIDs[0]), IP: "1.2.3.4", Port: 26656}}},
	} {
		raw, err := proto.Marshal(msg.Wrap())
		require.NoError(f, err)
		f.Add(byte(1), append([]byte{0, 0, 7, 0, byte(len(raw))}, raw...))
	}

	cfg := config.DefaultP2PConfig()
	nodeKey := p2p.NodeKey{PrivKey: ed25519.GenPrivKeyFromSecret([]byte("pex-fuzz"))}
	sw := p2p.NewSwitch(cfg, p2p.NewMultiplexTransport(p2p.DefaultNodeInfo{}, nodeKey, p2p.MConnConfig(cfg), trace.NoOpTracer()))
	sw.SetLogger(log.NewNopLogger())
	dir := f.TempDir()

	f.Fuzz(func(t *testing.T, flags byte, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		in := &fuzzInput{b: data}
		book := newFuzzAddrBook(dir, flags&1 == 1)
		r := NewReactor(book, &ReactorConfig{})
		r.SetSwitch(sw)
		r.SetLogger(log.NewNopLogger())
		// Keep the rate limit far above the run time of one input so it is
		// independent of the machine's speed.
		r.SetEnsurePeersPeriod(24 * time.Hour)
		// The reactor is not started (that would dial and run timers), but
		// its processor goroutine must not leak; Reactor.OnStop does not
		// stop it.
		t.Cleanup(r.BaseReactor.OnStop)

		peers := []*fuzzPexPeer{newFuzzPexPeer(0), newFuzzPexPeer(1), newFuzzPexPeer(2)}
		requests := make([]int, len(peers))

		for ops := 0; !in.empty() && ops < 64; ops++ {
			op := in.u8()
			pi := int(in.u8()) % len(peers)
			peer := peers[pi]
			if !peer.IsRunning() {
				continue // the switch no longer delivers messages from a stopped peer
			}
			switch op % 8 {
			case 0:
				before := len(peer.sent)
				r.RequestAddrs(peer)
				require.True(t, r.requestsSent.Has(string(peer.ID())))
				require.LessOrEqual(t, len(peer.sent), before+1)
			case 1:
				n := int(in.u8())
				if n == 0xff {
					n = maxGetSelection + int(in.u8()) // above the selection size
				}
				msg := &tmp2p.PexAddrs{Addrs: make([]tmp2p.NetAddress, 0, n)}
				for i := 0; i < n && !in.empty(); i++ {
					msg.Addrs = append(msg.Addrs, in.netAddress())
				}
				receivePexAddrs(t, r, book, peer, msg)
			case 2:
				before := len(peer.sent)
				r.Receive(p2p.Envelope{ChannelID: PexChannel, Src: peer, Message: &tmp2p.PexRequest{}})
				requests[pi]++
				// The first two requests are free; the third within the
				// interval is a flood.
				require.Equal(t, requests[pi] <= 2, peer.IsRunning())
				if peer.IsRunning() {
					require.Len(t, peer.sent, before+1)
					resp, ok := peer.sent[before].(*tmp2p.PexAddrs)
					require.True(t, ok)
					require.LessOrEqual(t, resp.Size(), maxMsgSize, "PEX response exceeds the receive cap")
					_, err := p2p.NetAddressesFromProto(resp.Addrs)
					require.NoError(t, err)
				}
			case 3:
				r.RemovePeer(peer, nil)
				requests[pi] = 0
				require.False(t, r.requestsSent.Has(string(peer.ID())))
			case 4:
				book.MarkGood(fuzzIDs[int(in.u8())%len(fuzzIDs)])
			case 5:
				addr, err := p2p.NetAddressFromProto(in.netAddress())
				if err == nil {
					book.MarkBad(addr, time.Hour)
					require.False(t, book.HasAddress(addr))
				}
			case 6:
				bias := int(int8(in.u8()))
				selection := book.GetSelectionWithBias(bias)
				require.LessOrEqual(t, len(selection), maxGetSelection)
				if book.Size() > 0 {
					require.NotEmpty(t, selection)
				}
				for _, addr := range selection {
					require.True(t, book.HasAddress(addr))
				}
			case 7:
				var envelope tmp2p.Message
				if proto.Unmarshal(in.take(int(in.u8())), &envelope) != nil {
					continue
				}
				msg, err := envelope.Unwrap()
				if err != nil {
					continue
				}
				if addrsMsg, ok := msg.(*tmp2p.PexAddrs); ok {
					receivePexAddrs(t, r, book, peer, addrsMsg)
				} else {
					r.Receive(p2p.Envelope{ChannelID: PexChannel, Src: peer, Message: msg})
					requests[pi]++
					require.Equal(t, requests[pi] <= 2, peer.IsRunning())
				}
			}
			checkAddrBook(t, book)
		}
	})
}

// receivePexAddrs delivers msg and checks the peer is stopped iff the list
// was unsolicited or did not convert, and that the open request is consumed.
func receivePexAddrs(t *testing.T, r *Reactor, book *addrBook, peer *fuzzPexPeer, msg *tmp2p.PexAddrs) {
	id := string(peer.ID())
	solicited := r.requestsSent.Has(id)
	_, convErr := p2p.NetAddressesFromProto(msg.Addrs)
	sizeBefore := book.Size()
	r.Receive(p2p.Envelope{ChannelID: PexChannel, Src: peer, Message: msg})
	accepted := solicited && convErr == nil
	require.Equal(t, accepted, peer.IsRunning(), "solicited=%v convErr=%v", solicited, convErr)
	if accepted {
		require.False(t, r.requestsSent.Has(id), "the open request must be consumed")
	}
	if !accepted {
		require.LessOrEqual(t, book.Size(), sizeBefore, "rejected list must not add addresses")
	}
}
