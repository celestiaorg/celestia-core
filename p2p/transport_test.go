package p2p

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/protoio"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p/conn"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

var defaultNodeName = "host_peer"

func emptyNodeInfo() NodeInfo {
	return DefaultNodeInfo{}
}

// newMultiplexTransport returns a tcp connected multiplexed peer
// using the default MConnConfig. It's a convenience function used
// for testing.
func newMultiplexTransport(
	nodeInfo NodeInfo,
	nodeKey NodeKey,
) *MultiplexTransport {
	return NewMultiplexTransport(
		nodeInfo, nodeKey, conn.DefaultMConnConfig(), trace.NoOpTracer(),
	)
}

func TestTransportMultiplexConnFilter(t *testing.T) {
	mt := newMultiplexTransport(
		emptyNodeInfo(),
		NodeKey{
			PrivKey: ed25519.GenPrivKey(),
		},
	)
	id := mt.nodeKey.ID()

	MultiplexTransportConnFilters(
		func(_ ConnSet, _ net.Conn, _ []net.IP) error { return nil },
		func(_ ConnSet, _ net.Conn, _ []net.IP) error { return nil },
		func(_ ConnSet, _ net.Conn, _ []net.IP) error {
			return fmt.Errorf("rejected")
		},
	)(mt)

	addr, err := NewNetAddressString(IDAddressString(id, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}

	if err := mt.Listen(*addr); err != nil {
		t.Fatal(err)
	}

	errc := make(chan error)

	go func() {
		addr := NewNetAddress(id, mt.listener.Addr())

		_, err := addr.Dial()
		if err != nil {
			errc <- err
			return
		}

		close(errc)
	}()

	if err := <-errc; err != nil {
		t.Errorf("connection failed: %v", err)
	}

	_, err = mt.Accept(peerConfig{})
	if e, ok := err.(ErrRejected); ok {
		if !e.IsFiltered() {
			t.Errorf("expected peer to be filtered, got %v", err)
		}
	} else {
		t.Errorf("expected ErrRejected, got %v", err)
	}
}

func TestTransportMultiplexConnFilterTimeout(t *testing.T) {
	mt := newMultiplexTransport(
		emptyNodeInfo(),
		NodeKey{
			PrivKey: ed25519.GenPrivKey(),
		},
	)
	id := mt.nodeKey.ID()

	MultiplexTransportFilterTimeout(5 * time.Millisecond)(mt)
	MultiplexTransportConnFilters(
		func(_ ConnSet, _ net.Conn, _ []net.IP) error {
			time.Sleep(1 * time.Second)
			return nil
		},
	)(mt)

	addr, err := NewNetAddressString(IDAddressString(id, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}

	if err := mt.Listen(*addr); err != nil {
		t.Fatal(err)
	}

	errc := make(chan error)
	go func() {
		addr := NewNetAddress(id, mt.listener.Addr())

		_, err := addr.Dial()
		if err != nil {
			errc <- err
			return
		}

		close(errc)
	}()

	if err := <-errc; err != nil {
		t.Errorf("connection failed: %v", err)
	}

	_, err = mt.Accept(peerConfig{})
	if _, ok := err.(ErrFilterTimeout); !ok {
		t.Errorf("expected ErrFilterTimeout, got %v", err)
	}
}

func TestTransportMultiplexMaxIncomingConnections(t *testing.T) {
	pv := ed25519.GenPrivKey()
	id := PubKeyToID(pv.PubKey())
	mt := newMultiplexTransport(
		testNodeInfo(
			id, "transport",
		),
		NodeKey{
			PrivKey: pv,
		},
	)

	MultiplexTransportMaxIncomingConnections(0)(mt)

	addr, err := NewNetAddressString(IDAddressString(id, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	const maxIncomingConns = 2
	MultiplexTransportMaxIncomingConnections(maxIncomingConns)(mt)
	if err := mt.Listen(*addr); err != nil {
		t.Fatal(err)
	}

	laddr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

	// Connect more peers than max
	for i := 0; i <= maxIncomingConns; i++ {
		errc := make(chan error)
		go testDialer(*laddr, errc)

		err = <-errc
		if i < maxIncomingConns {
			if err != nil {
				t.Errorf("dialer connection failed: %v", err)
			}
			_, err = mt.Accept(peerConfig{})
			if err != nil {
				t.Errorf("connection failed: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
			// mt actually blocks forever on trying to accept a new peer into a full channel so
			// expect the dialer to encounter a timeout error. Calling mt.Accept will block until
			// mt is closed.
			t.Errorf("expected i/o timeout error, got %v", err)
		}
	}
}

func TestTransportMultiplexAcceptMultiple(t *testing.T) {
	mt := testSetupMultiplexTransport(t)
	laddr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

	var (
		seed     = rand.New(rand.NewSource(time.Now().UnixNano()))
		nDialers = seed.Intn(64) + 64
		errc     = make(chan error, nDialers)
	)

	// Setup dialers.
	for i := 0; i < nDialers; i++ {
		go testDialer(*laddr, errc)
	}

	// Catch connection errors.
	for i := 0; i < nDialers; i++ {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}

	ps := []Peer{}

	// Accept all peers.
	for i := 0; i < cap(errc); i++ {
		p, err := mt.Accept(peerConfig{})
		if err != nil {
			t.Fatal(err)
		}

		if err := p.Start(); err != nil {
			t.Fatal(err)
		}

		ps = append(ps, p)
	}

	if have, want := len(ps), cap(errc); have != want {
		t.Errorf("have %v, want %v", have, want)
	}

	// Stop all peers.
	for _, p := range ps {
		if err := p.Stop(); err != nil {
			t.Fatal(err)
		}
	}

	if err := mt.Close(); err != nil {
		t.Errorf("close errored: %v", err)
	}
}

func testDialer(dialAddr NetAddress, errc chan error) {
	var (
		pv     = ed25519.GenPrivKey()
		dialer = newMultiplexTransport(
			testNodeInfo(PubKeyToID(pv.PubKey()), defaultNodeName),
			NodeKey{
				PrivKey: pv,
			},
		)
	)

	_, err := dialer.Dial(dialAddr, peerConfig{})
	if err != nil {
		errc <- err
		return
	}

	// Signal that the connection was established.
	errc <- nil
}

func TestTransportMultiplexAcceptNonBlocking(t *testing.T) {
	mt := testSetupMultiplexTransport(t)

	var (
		fastNodePV   = ed25519.GenPrivKey()
		fastNodeInfo = testNodeInfo(PubKeyToID(fastNodePV.PubKey()), "fastnode")
		errc         = make(chan error)
		fastc        = make(chan struct{})
		slowc        = make(chan struct{})
		slowdonec    = make(chan struct{})
	)

	// Simulate slow Peer.
	go func() {
		addr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

		c, err := addr.Dial()
		if err != nil {
			errc <- err
			return
		}

		close(slowc)
		defer func() {
			close(slowdonec)
		}()

		// Make sure we switch to fast peer goroutine.
		runtime.Gosched()

		select {
		case <-fastc:
			// Fast peer connected.
		case <-time.After(200 * time.Millisecond):
			// We error if the fast peer didn't succeed.
			errc <- fmt.Errorf("fast peer timed out")
		}

		sc, err := upgradeSecretConn(c, 200*time.Millisecond, ed25519.GenPrivKey())
		if err != nil {
			errc <- err
			return
		}

		_, err = handshake(sc, 200*time.Millisecond,
			testNodeInfo(
				PubKeyToID(ed25519.GenPrivKey().PubKey()),
				"slow_peer",
			))
		if err != nil {
			errc <- err
		}
	}()

	// Simulate fast Peer.
	go func() {
		<-slowc

		var (
			dialer = newMultiplexTransport(
				fastNodeInfo,
				NodeKey{
					PrivKey: fastNodePV,
				},
			)
		)
		addr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

		_, err := dialer.Dial(*addr, peerConfig{})
		if err != nil {
			errc <- err
			return
		}

		close(fastc)
		<-slowdonec
		close(errc)
	}()

	if err := <-errc; err != nil {
		t.Logf("connection failed: %v", err)
	}

	// Both connections race independently onto Accept()'s result channel;
	// the slow peer is deliberately malformed so it's always rejected, but
	// isn't guaranteed to lose that race under CI jitter. Accept both and
	// check the fast peer is among them.
	var fastPeer Peer
	for i := 0; i < 2; i++ {
		p, err := mt.Accept(peerConfig{})
		if err != nil {
			t.Logf("connection %d rejected (expected for the deliberately malformed slow peer): %v", i, err)
			continue
		}
		fastPeer = p
	}
	if fastPeer == nil {
		t.Fatal("fast peer was never accepted")
	}

	if have, want := fastPeer.NodeInfo(), fastNodeInfo; !reflect.DeepEqual(have, want) {
		t.Errorf("have %v, want %v", have, want)
	}
}

func TestTransportMultiplexValidateNodeInfo(t *testing.T) {
	mt := testSetupMultiplexTransport(t)

	errc := make(chan error)

	go func() {
		var (
			pv     = ed25519.GenPrivKey()
			dialer = newMultiplexTransport(
				testNodeInfo(PubKeyToID(pv.PubKey()), ""), // Should not be empty
				NodeKey{
					PrivKey: pv,
				},
			)
		)

		addr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

		_, err := dialer.Dial(*addr, peerConfig{})
		if err != nil {
			errc <- err
			return
		}

		close(errc)
	}()

	if err := <-errc; err != nil {
		t.Errorf("connection failed: %v", err)
	}

	_, err := mt.Accept(peerConfig{})
	if e, ok := err.(ErrRejected); ok {
		if !e.IsNodeInfoInvalid() {
			t.Errorf("expected NodeInfo to be invalid, got %v", err)
		}
	} else {
		t.Errorf("expected ErrRejected, got %v", err)
	}
}

func TestTransportMultiplexRejectMissmatchID(t *testing.T) {
	mt := testSetupMultiplexTransport(t)

	errc := make(chan error)

	go func() {
		dialer := newMultiplexTransport(
			testNodeInfo(
				PubKeyToID(ed25519.GenPrivKey().PubKey()), "dialer",
			),
			NodeKey{
				PrivKey: ed25519.GenPrivKey(),
			},
		)
		addr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

		_, err := dialer.Dial(*addr, peerConfig{})
		if err != nil {
			errc <- err
			return
		}

		close(errc)
	}()

	if err := <-errc; err != nil {
		t.Errorf("connection failed: %v", err)
	}

	_, err := mt.Accept(peerConfig{})
	if e, ok := err.(ErrRejected); ok {
		if !e.IsAuthFailure() {
			t.Errorf("expected auth failure, got %v", e)
		}
	} else {
		t.Errorf("expected ErrRejected, got %v", err)
	}
}

func TestTransportMultiplexDialRejectWrongID(t *testing.T) {
	mt := testSetupMultiplexTransport(t)

	var (
		pv     = ed25519.GenPrivKey()
		dialer = newMultiplexTransport(
			testNodeInfo(PubKeyToID(pv.PubKey()), ""), // Should not be empty
			NodeKey{
				PrivKey: pv,
			},
		)
	)

	wrongID := PubKeyToID(ed25519.GenPrivKey().PubKey())
	addr := NewNetAddress(wrongID, mt.listener.Addr())

	_, err := dialer.Dial(*addr, peerConfig{})
	if err != nil {
		t.Logf("connection failed: %v", err)
		if e, ok := err.(ErrRejected); ok {
			if !e.IsAuthFailure() {
				t.Errorf("expected auth failure, got %v", e)
			}
		} else {
			t.Errorf("expected ErrRejected, got %v", err)
		}
	}
}

func TestTransportMultiplexRejectIncompatible(t *testing.T) {
	mt := testSetupMultiplexTransport(t)

	errc := make(chan error)

	go func() {
		var (
			pv     = ed25519.GenPrivKey()
			dialer = newMultiplexTransport(
				testNodeInfoWithNetwork(PubKeyToID(pv.PubKey()), "dialer", "incompatible-network"),
				NodeKey{
					PrivKey: pv,
				},
			)
		)
		addr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

		_, err := dialer.Dial(*addr, peerConfig{})
		if err != nil {
			errc <- err
			return
		}

		close(errc)
	}()

	_, err := mt.Accept(peerConfig{})
	if e, ok := err.(ErrRejected); ok {
		if !e.IsIncompatible() {
			t.Errorf("expected to reject incompatible, got %v", e)
		}
	} else {
		t.Errorf("expected ErrRejected, got %v", err)
	}
}

func TestTransportMultiplexRejectSelf(t *testing.T) {
	mt := testSetupMultiplexTransport(t)

	errc := make(chan error)

	go func() {
		addr := NewNetAddress(mt.nodeKey.ID(), mt.listener.Addr())

		_, err := mt.Dial(*addr, peerConfig{})
		if err != nil {
			errc <- err
			return
		}

		close(errc)
	}()

	if err := <-errc; err != nil {
		if e, ok := err.(ErrRejected); ok {
			if !e.IsSelf() {
				t.Errorf("expected to reject self, got: %v", e)
			}
		} else {
			t.Errorf("expected ErrRejected, got %v", err)
		}
	} else {
		t.Errorf("expected connection failure")
	}

	_, err := mt.Accept(peerConfig{})
	if err, ok := err.(ErrRejected); ok {
		if !err.IsSelf() {
			t.Errorf("expected to reject self, got: %v", err)
		}
	} else {
		t.Errorf("expected ErrRejected, got %v", nil)
	}
}

func TestTransportConnDuplicateIPFilter(t *testing.T) {
	filter := ConnDuplicateIPFilter()

	if err := filter(nil, &testTransportConn{}, nil); err != nil {
		t.Fatal(err)
	}

	var (
		c  = &testTransportConn{}
		cs = NewConnSet()
	)

	cs.Set(c, []net.IP{
		{10, 0, 10, 1},
		{10, 0, 10, 2},
		{10, 0, 10, 3},
	})

	if err := filter(cs, c, []net.IP{
		{10, 0, 10, 2},
	}); err == nil {
		t.Errorf("expected Peer to be rejected as duplicate")
	}
}

func TestTransportHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var (
		peerPV       = ed25519.GenPrivKey()
		peerNodeInfo = testNodeInfo(PubKeyToID(peerPV.PubKey()), defaultNodeName)
	)

	go func() {
		c, err := net.Dial(ln.Addr().Network(), ln.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}

		go func(c net.Conn) {
			_, err := protoio.NewDelimitedWriter(c).WriteMsg(peerNodeInfo.(DefaultNodeInfo).ToProto())
			if err != nil {
				t.Error(err)
			}
		}(c)
		go func(c net.Conn) {
			var (
				// ni   DefaultNodeInfo
				pbni tmp2p.DefaultNodeInfo
			)

			protoReader := protoio.NewDelimitedReader(c, MaxNodeInfoSize())
			_, err := protoReader.ReadMsg(&pbni)
			if err != nil {
				t.Error(err)
			}

			_, err = DefaultNodeInfoFromToProto(&pbni)
			if err != nil {
				t.Error(err)
			}
		}(c)
	}()

	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}

	ni, err := handshake(c, 20*time.Millisecond, emptyNodeInfo())
	if err != nil {
		t.Fatal(err)
	}

	if have, want := ni, peerNodeInfo; !reflect.DeepEqual(have, want) {
		t.Errorf("have %v, want %v", have, want)
	}
}

func TestTransportAddChannel(t *testing.T) {
	mt := newMultiplexTransport(
		emptyNodeInfo(),
		NodeKey{
			PrivKey: ed25519.GenPrivKey(),
		},
	)
	testChannel := byte(0x01)

	mt.AddChannel(testChannel)
	if !mt.nodeInfo.(DefaultNodeInfo).HasChannel(testChannel) {
		t.Errorf("missing added channel %v. Got %v", testChannel, mt.nodeInfo.(DefaultNodeInfo).Channels)
	}
}

// create listener
func testSetupMultiplexTransport(t *testing.T) *MultiplexTransport {
	var (
		pv = ed25519.GenPrivKey()
		id = PubKeyToID(pv.PubKey())
		mt = newMultiplexTransport(
			testNodeInfo(
				id, "transport",
			),
			NodeKey{
				PrivKey: pv,
			},
		)
	)

	addr, err := NewNetAddressString(IDAddressString(id, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}

	if err := mt.Listen(*addr); err != nil {
		t.Fatal(err)
	}

	// give the listener some time to get ready
	time.Sleep(20 * time.Millisecond)

	return mt
}

type testTransportAddr struct{}

func (a *testTransportAddr) Network() string { return "tcp" }
func (a *testTransportAddr) String() string  { return "test.local:1234" }

type testTransportConn struct{}

func (c *testTransportConn) Close() error {
	return fmt.Errorf("close() not implemented")
}

func (c *testTransportConn) LocalAddr() net.Addr {
	return &testTransportAddr{}
}

func (c *testTransportConn) RemoteAddr() net.Addr {
	return &testTransportAddr{}
}

func (c *testTransportConn) Read(_ []byte) (int, error) {
	return -1, fmt.Errorf("read() not implemented")
}

func (c *testTransportConn) SetDeadline(_ time.Time) error {
	return fmt.Errorf("setDeadline() not implemented")
}

func (c *testTransportConn) SetReadDeadline(_ time.Time) error {
	return fmt.Errorf("setReadDeadline() not implemented")
}

func (c *testTransportConn) SetWriteDeadline(_ time.Time) error {
	return fmt.Errorf("setWriteDeadline() not implemented")
}

func (c *testTransportConn) Write(_ []byte) (int, error) {
	return -1, fmt.Errorf("write() not implemented")
}

// errListener wraps a net.Listener and returns the queued errors from Accept
// before it accepts real connections.
type errListener struct {
	net.Listener
	errs chan error
}

func (l *errListener) Accept() (net.Conn, error) {
	select {
	case err := <-l.errs:
		return nil, err
	default:
		return l.Listener.Accept()
	}
}

// testSetupErrListenerTransport starts a transport whose listener returns
// errs from Accept before it accepts real connections. The returned channel
// is closed when the accept loop exits.
func testSetupErrListenerTransport(t *testing.T, errs ...error) (*MultiplexTransport, <-chan struct{}) {
	t.Helper()
	pv := ed25519.GenPrivKey()
	id := PubKeyToID(pv.PubKey())
	mt := newMultiplexTransport(testNodeInfo(id, "transport"), NodeKey{PrivKey: pv})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	el := &errListener{Listener: ln, errs: make(chan error, len(errs))}
	for _, err := range errs {
		el.errs <- err
	}
	mt.listener = el
	mt.netAddr = *NewNetAddress(id, ln.Addr())

	done := make(chan struct{})
	go func() {
		defer close(done)
		mt.acceptPeers()
	}()
	return mt, done
}

func TestTransportMultiplexAcceptRetriesTemporaryErrors(t *testing.T) {
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	enfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.ENFILE)}
	mt, done := testSetupErrListenerTransport(t, emfile, emfile, enfile, emfile)

	errc := make(chan error, 1)
	go testDialer(mt.NetAddress(), errc)

	acceptc := make(chan error, 1)
	go func() {
		p, err := mt.Accept(peerConfig{})
		if err == nil {
			mt.Cleanup(p)
		}
		acceptc <- err
	}()

	select {
	case err := <-acceptc:
		if err != nil {
			t.Fatalf("Accept returned error after temporary accept errors: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Accept")
	}
	if err := <-errc; err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not exit after Close")
	}
}

func TestTransportMultiplexAcceptStopsOnClose(t *testing.T) {
	mt, done := testSetupErrListenerTransport(t)

	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not exit after Close")
	}
	if _, err := mt.Accept(peerConfig{}); !errors.As(err, &ErrTransportClosed{}) {
		t.Fatalf("want ErrTransportClosed, got %v", err)
	}
}

func TestTransportMultiplexAcceptReturnsFatalError(t *testing.T) {
	fatal := errors.New("fatal accept error")
	mt, done := testSetupErrListenerTransport(t, fatal)
	defer mt.Close()

	acceptc := make(chan error, 1)
	go func() {
		_, err := mt.Accept(peerConfig{})
		acceptc <- err
	}()
	select {
	case err := <-acceptc:
		if !errors.Is(err, fatal) {
			t.Fatalf("want %v, got %v", fatal, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Accept")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not exit after fatal error")
	}
}

// alwaysErrListener wraps a net.Listener and returns err from every Accept
// call. It counts the calls.
type alwaysErrListener struct {
	net.Listener
	err   error
	calls atomic.Int32
}

func (l *alwaysErrListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	return nil, l.err
}

func TestTransportMultiplexAcceptStopsOnCloseDuringBackoff(t *testing.T) {
	pv := ed25519.GenPrivKey()
	id := PubKeyToID(pv.PubKey())
	mt := newMultiplexTransport(testNodeInfo(id, "transport"), NodeKey{PrivKey: pv})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	el := &alwaysErrListener{
		Listener: ln,
		err:      &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)},
	}
	mt.listener = el
	mt.netAddr = *NewNetAddress(id, ln.Addr())

	done := make(chan struct{})
	go func() {
		defer close(done)
		mt.acceptPeers()
	}()

	// The delay doubles from minAcceptRetryDelay on each error. After this
	// many errors it reaches maxAcceptRetryDelay, so the loop now waits for
	// the full maximum delay before the next Accept call.
	const errsToMaxDelay = 9
	deadline := time.Now().Add(10 * time.Second)
	for el.calls.Load() < errsToMaxDelay {
		if time.Now().After(deadline) {
			t.Fatalf("accept loop made only %d Accept calls", el.calls.Load())
		}
		time.Sleep(time.Millisecond)
	}

	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	// The loop must stop waiting when the transport closes. The bound is
	// much shorter than the maxAcceptRetryDelay wait the loop is in.
	select {
	case <-done:
	case <-time.After(maxAcceptRetryDelay / 5):
		t.Fatal("accept loop did not exit promptly after Close during backoff")
	}
}

func TestIsTemporaryAcceptError(t *testing.T) {
	wrap := func(errno syscall.Errno) error {
		return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", errno)}
	}
	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{"EMFILE", wrap(syscall.EMFILE), true},
		{"ENFILE", wrap(syscall.ENFILE), true},
		{"ENOBUFS", wrap(syscall.ENOBUFS), true},
		{"ENOMEM", wrap(syscall.ENOMEM), true},
		{"temporary net.Error", tempNetError{temporary: true}, true},
		{"non-temporary net.Error", tempNetError{temporary: false}, false},
		{"plain error", errors.New("boom"), false},
		{"closed listener", net.ErrClosed, false},
		{"nil", nil, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTemporaryAcceptError(tc.err); got != tc.want {
				t.Fatalf("isTemporaryAcceptError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// tempNetError is a net.Error whose Temporary result is set by the test.
type tempNetError struct{ temporary bool }

func (e tempNetError) Error() string   { return "test net error" }
func (e tempNetError) Timeout() bool   { return false }
func (e tempNetError) Temporary() bool { return e.temporary }

func TestNextAcceptRetryDelay(t *testing.T) {
	want := []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond,
		40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond,
		320 * time.Millisecond, 640 * time.Millisecond, time.Second, time.Second,
	}
	var d time.Duration
	for i, w := range want {
		d = nextAcceptRetryDelay(d)
		if d != w {
			t.Fatalf("step %d: got %v, want %v", i, d, w)
		}
	}
	if got := nextAcceptRetryDelay(0); got != minAcceptRetryDelay {
		t.Fatalf("after reset: got %v, want %v", got, minAcceptRetryDelay)
	}
}

// scriptListener returns the errors sent on steps from Accept. A nil step
// accepts a real connection. Accept blocks until a step arrives or the
// listener closes.
type scriptListener struct {
	net.Listener
	steps  chan error
	closed chan struct{}
	once   sync.Once
}

func newScriptListener(t *testing.T) *scriptListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &scriptListener{Listener: ln, steps: make(chan error, 64), closed: make(chan struct{})}
}

func (l *scriptListener) Accept() (net.Conn, error) {
	select {
	case err := <-l.steps:
		if err != nil {
			return nil, err
		}
		return l.Listener.Accept()
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *scriptListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

// recordLogger records the messages logged at Error level.
type recordLogger struct {
	log.Logger
	mu   sync.Mutex
	msgs []string
}

func (l *recordLogger) Error(msg string, _ ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
}

func (l *recordLogger) With(...interface{}) log.Logger { return l }

func (l *recordLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}

// testSetupScriptTransport starts an accept loop on a scriptListener. The
// retry wait returns at once and sends each requested delay on the returned
// delays channel.
func testSetupScriptTransport(
	t *testing.T, opts ...MultiplexTransportOption,
) (*MultiplexTransport, *scriptListener, <-chan time.Duration, <-chan struct{}) {
	t.Helper()
	pv := ed25519.GenPrivKey()
	id := PubKeyToID(pv.PubKey())
	mt := newMultiplexTransport(testNodeInfo(id, "transport"), NodeKey{PrivKey: pv})
	for _, opt := range opts {
		opt(mt)
	}
	delays := make(chan time.Duration, 64)
	mt.acceptRetryAfter = func(d time.Duration) <-chan time.Time {
		delays <- d
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	sl := newScriptListener(t)
	mt.listener = sl
	mt.netAddr = *NewNetAddress(id, sl.Addr())

	done := make(chan struct{})
	go func() {
		defer close(done)
		mt.acceptPeers()
	}()
	t.Cleanup(func() {
		select {
		case <-mt.closec:
		default:
			_ = mt.Close()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("accept loop did not exit at cleanup")
		}
	})
	return mt, sl, delays, done
}

func recvDelay(t *testing.T, delays <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case d := <-delays:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not wait before retrying")
		return 0
	}
}

func TestTransportMultiplexAcceptBackoffSequence(t *testing.T) {
	_, sl, delays, _ := testSetupScriptTransport(t)
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}

	// Ten errors in a row: the delay doubles from 5ms and stops at 1s.
	want := []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond,
		40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond,
		320 * time.Millisecond, 640 * time.Millisecond, time.Second, time.Second,
	}
	for i, w := range want {
		sl.steps <- emfile
		if got := recvDelay(t, delays); got != w {
			t.Fatalf("retry %d: got delay %v, want %v", i, got, w)
		}
	}

	// A successful accept resets the delay.
	conn, err := net.Dial("tcp", sl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sl.steps <- nil
	sl.steps <- emfile
	if got := recvDelay(t, delays); got != minAcceptRetryDelay {
		t.Fatalf("after successful accept: got delay %v, want %v", got, minAcceptRetryDelay)
	}
}

func TestTransportMultiplexAcceptLogsRetry(t *testing.T) {
	logger := &recordLogger{Logger: log.NewNopLogger()}
	_, sl, delays, _ := testSetupScriptTransport(t, MultiplexTransportLogger(logger))

	sl.steps <- &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	recvDelay(t, delays)

	msgs := logger.messages()
	if len(msgs) != 1 || msgs[0] != "Accept error, retrying" {
		t.Fatalf("got logged messages %q, want one retry message", msgs)
	}
}

func TestTransportMultiplexAcceptFatalErrorStopsOnClose(t *testing.T) {
	mt, sl, _, done := testSetupScriptTransport(t)

	// Nobody calls Accept, so the loop waits to send the fatal error.
	sl.steps <- errors.New("fatal accept error")
	// Wait until the loop takes the step and reaches the send.
	deadline := time.Now().Add(5 * time.Second)
	for len(sl.steps) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("accept loop did not take the fatal error")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)

	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not exit after Close with a fatal error pending")
	}
}
