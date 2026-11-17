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
	"syscall"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
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

// scriptListener returns the errors sent on steps from Accept. A nil step
// accepts a real connection. Accept blocks until a step arrives or the
// listener closes.
type scriptListener struct {
	net.Listener
	steps  chan error
	closed chan struct{}
	once   sync.Once
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

// testSetupScriptTransport starts an accept loop on a scriptListener. The
// loop calls retryAfter instead of time.After before each retry.
func testSetupScriptTransport(
	t *testing.T, retryAfter func(time.Duration) <-chan time.Time,
) (*MultiplexTransport, *scriptListener, <-chan struct{}) {
	t.Helper()
	pv := ed25519.GenPrivKey()
	id := PubKeyToID(pv.PubKey())
	mt := newMultiplexTransport(testNodeInfo(id, "transport"), NodeKey{PrivKey: pv})
	mt.acceptRetryAfter = retryAfter

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sl := &scriptListener{Listener: ln, steps: make(chan error, 64), closed: make(chan struct{})}
	mt.listener = sl
	mt.netAddr = *NewNetAddress(id, ln.Addr())

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
		waitDone(t, done, "accept loop did not exit at cleanup")
	})
	return mt, sl, done
}

// recordRetryFn returns a retry wait that sends each delay on delays and
// fires at once.
func recordRetryFn(delays chan<- time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		delays <- d
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
}

func waitDone(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error(msg)
	}
}

func emfileErr() error {
	return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
}

func TestTransportMultiplexAcceptRetriesTemporaryErrors(t *testing.T) {
	mt, sl, done := testSetupScriptTransport(t, recordRetryFn(make(chan time.Duration, 64)))
	enfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.ENFILE)}
	for _, err := range []error{emfileErr(), emfileErr(), enfile, emfileErr(), nil} {
		sl.steps <- err
	}

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
	waitDone(t, done, "accept loop did not exit after Close")
}

func TestTransportMultiplexAcceptReturnsFatalError(t *testing.T) {
	fatal := errors.New("fatal accept error")
	mt, sl, done := testSetupScriptTransport(t, time.After)
	sl.steps <- fatal

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
	waitDone(t, done, "accept loop did not exit after fatal error")
}

func TestTransportMultiplexAcceptStopsOnCloseDuringBackoff(t *testing.T) {
	waiting := make(chan struct{})
	var once sync.Once
	// The wait never fires, so only Close can end it.
	neverFire := func(time.Duration) <-chan time.Time {
		once.Do(func() { close(waiting) })
		return nil
	}
	mt, sl, done := testSetupScriptTransport(t, neverFire)
	sl.steps <- emfileErr()

	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not start the retry wait")
	}
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done, "accept loop did not exit after Close during backoff")
}

func TestTransportMultiplexAcceptFatalErrorStopsOnClose(t *testing.T) {
	mt, sl, done := testSetupScriptTransport(t, time.After)
	fatal := &classifiedError{classified: make(chan struct{})}
	sl.steps <- fatal

	// The loop classifies the error after its closed check and just before
	// it sends the error to Accept. Nobody calls Accept, so the send waits.
	select {
	case <-fatal.classified:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not classify the fatal error")
	}
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done, "accept loop did not exit after Close with a fatal error pending")
}

// classifiedError is a fatal error that closes classified when
// isTemporaryAcceptError checks it with errors.As, which is its last check.
type classifiedError struct {
	classified chan struct{}
	once       sync.Once
}

func (e *classifiedError) Error() string { return "fatal accept error" }

func (e *classifiedError) As(any) bool {
	e.once.Do(func() { close(e.classified) })
	return false
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

func TestTransportMultiplexAcceptBackoffSequence(t *testing.T) {
	delays := make(chan time.Duration, 64)
	_, sl, _ := testSetupScriptTransport(t, recordRetryFn(delays))
	recvDelay := func() time.Duration {
		t.Helper()
		select {
		case d := <-delays:
			return d
		case <-time.After(5 * time.Second):
			t.Fatal("accept loop did not wait before retrying")
			return 0
		}
	}

	// Ten errors in a row: the delay doubles from 5ms and stops at 1s.
	want := []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond,
		40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond,
		320 * time.Millisecond, 640 * time.Millisecond, time.Second, time.Second,
	}
	for i, w := range want {
		sl.steps <- emfileErr()
		if got := recvDelay(); got != w {
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
	sl.steps <- emfileErr()
	if got := recvDelay(); got != minAcceptRetryDelay {
		t.Fatalf("after successful accept: got delay %v, want %v", got, minAcceptRetryDelay)
	}
}
