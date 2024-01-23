package conn

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/protoio"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
	"github.com/cometbft/cometbft/proto/tendermint/types"
)

const maxPingPongPacketSize = 1024 // bytes

func createTestMConnection(conn net.Conn) *MConnection {
	onReceive := func(chID byte, msgBytes []byte) {
	}
	onError := func(r interface{}) {
	}
	c := createMConnectionWithCallbacks(conn, onReceive, onError)
	c.SetLogger(log.TestingLogger())
	return c
}

func createMConnectionWithCallbacks(
	conn net.Conn,
	onReceive func(chID byte, msgBytes []byte),
	onError func(r interface{}),
) *MConnection {
	cfg := DefaultMConnConfig()
	cfg.PingInterval = 90 * time.Millisecond
	cfg.PongTimeout = 45 * time.Millisecond
	chDescs := []*ChannelDescriptor{{ID: 0x01, Priority: 1, SendQueueCapacity: 1}}
	c := NewMConnectionWithConfig(conn, chDescs, onReceive, onError, cfg)
	c.SetLogger(log.TestingLogger())
	return c
}

func createMConnectionWithCallbacksConfigs(
	conn net.Conn,
	onReceive func(chID byte, msgBytes []byte),
	onError func(r interface{}),
	cfg MConnConfig,
) *MConnection {
	chDescs := []*ChannelDescriptor{{ID: 0x01, Priority: 1, SendQueueCapacity: 1}}
	c := NewMConnectionWithConfig(conn, chDescs, onReceive, onError, cfg)
	c.SetLogger(log.TestingLogger())
	return c
}

func TestMConnectionSendFlushStop(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	clientConn := createTestMConnection(client)
	err := clientConn.Start()
	require.Nil(t, err)
	defer clientConn.Stop() //nolint:errcheck // ignore for tests

	msg := []byte("abc")
	assert.True(t, clientConn.Send(0x01, msg))

	msgLength := 14

	// start the reader in a new routine, so we can flush
	errCh := make(chan error)
	go func() {
		msgB := make([]byte, msgLength)
		_, err := server.Read(msgB)
		if err != nil {
			t.Error(err)
			return
		}
		errCh <- err
	}()

	// stop the conn - it should flush all conns
	clientConn.FlushStop()

	timer := time.NewTimer(3 * time.Second)
	select {
	case <-errCh:
	case <-timer.C:
		t.Error("timed out waiting for msgs to be read")
	}
}

func TestMConnectionSend(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	mconn := createTestMConnection(client)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	msg := []byte("Ant-Man")
	assert.True(t, mconn.Send(0x01, msg))
	// Note: subsequent Send/TrySend calls could pass because we are reading from
	// the send queue in a separate goroutine.
	_, err = server.Read(make([]byte, len(msg)))
	if err != nil {
		t.Error(err)
	}
	assert.True(t, mconn.CanSend(0x01))

	msg = []byte("Spider-Man")
	assert.True(t, mconn.TrySend(0x01, msg))
	_, err = server.Read(make([]byte, len(msg)))
	if err != nil {
		t.Error(err)
	}

	assert.False(t, mconn.CanSend(0x05), "CanSend should return false because channel is unknown")
	assert.False(t, mconn.Send(0x05, []byte("Absorbing Man")), "Send should return false because channel is unknown")
}

func TestMConnectionSendRate(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	clientConn := createTestMConnection(client)
	err := clientConn.Start()
	require.Nil(t, err)
	defer clientConn.Stop() //nolint:errcheck // ignore for tests

	// prepare a message to send from client to the server
	msg := bytes.Repeat([]byte{1}, 1000*1024)

	// send the message and check if it was sent successfully
	done := clientConn.Send(0x01, msg)
	assert.True(t, done)

	// read the message from the server
	_, err = server.Read(make([]byte, len(msg)))
	if err != nil {
		t.Error(err)
	}

	// check if the peak send rate is within the expected range
	peakSendRate := clientConn.Status().SendMonitor.PeakRate
	// the peak send rate should be less than or equal to the max send rate
	// the max send rate is calculated based on the configured SendRate and other configs
	maxSendRate := clientConn.maxSendRate()
	assert.True(t, peakSendRate <= clientConn.maxSendRate(), fmt.Sprintf("peakSendRate %d > maxSendRate %d", peakSendRate, maxSendRate))
}

// maxSendRate returns the maximum send rate in bytes per second based on the MConnection's SendRate and other configs. It is used to calculate the highest expected value for the peak send rate.
// The returned value is slightly higher than the configured SendRate.
func (c *MConnection) maxSendRate() int64 {
	sampleRate := c.sendMonitor.GetSampleRate().Seconds()
	numberOfSamplePerSecond := 1 / sampleRate
	sendRate := float64(round(float64(c.config.SendRate) * sampleRate))
	batchSizeBytes := float64(numBatchPacketMsgs * c._maxPacketMsgSize)
	effectiveRatePerSample := math.Ceil(sendRate/batchSizeBytes) * batchSizeBytes
	effectiveSendRate := round(numberOfSamplePerSecond * effectiveRatePerSample)

	return effectiveSendRate
}

// round returns x rounded to the nearest int64 (non-negative values only).
func round(x float64) int64 {
	if _, frac := math.Modf(x); frac >= 0.5 {
		return int64(math.Ceil(x))
	}
	return int64(math.Floor(x))
}

func TestMConnectionReceiveRate(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	// prepare a client connection with callbacks to receive messages
	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}

	cnfg := DefaultMConnConfig()
	cnfg.SendRate = 500_000 // 500 KB/s
	cnfg.RecvRate = 500_000 // 500 KB/s

	clientConn := createMConnectionWithCallbacksConfigs(client, onReceive, onError, cnfg)
	err := clientConn.Start()
	require.Nil(t, err)
	defer clientConn.Stop() //nolint:errcheck // ignore for tests

	serverConn := createMConnectionWithCallbacksConfigs(server, func(chID byte, msgBytes []byte) {}, func(r interface{}) {}, cnfg)
	err = serverConn.Start()
	require.Nil(t, err)
	defer serverConn.Stop() //nolint:errcheck // ignore for tests

	msgSize := int(2 * cnfg.RecvRate)
	msg := bytes.Repeat([]byte{1}, msgSize)
	assert.True(t, serverConn.Send(0x01, msg))

	// approximate the time it takes to receive the message given the configured RecvRate
	approxDelay := time.Duration(int64(math.Ceil(float64(msgSize)/float64(cnfg.RecvRate))) * int64(time.Second) * 2)

	select {
	case receivedBytes := <-receivedCh:
		assert.Equal(t, msg, receivedBytes)
	case err := <-errorsCh:
		t.Fatalf("Expected %s, got %+v", msg, err)
	case <-time.After(approxDelay):
		t.Fatalf("Did not receive the message in %fs", approxDelay.Seconds())
	}

	peakRecvRate := clientConn.recvMonitor.Status().PeakRate
	maxRecvRate := clientConn.maxRecvRate()

	assert.True(t, peakRecvRate <= maxRecvRate, fmt.Sprintf("peakRecvRate %d > maxRecvRate %d", peakRecvRate, maxRecvRate))

	peakSendRate := clientConn.sendMonitor.Status().PeakRate
	maxSendRate := clientConn.maxSendRate()

	assert.True(t, peakSendRate <= maxSendRate, fmt.Sprintf("peakSendRate %d > maxSendRate %d", peakSendRate, maxSendRate))
}

// maxRecvRate returns the maximum receive rate in bytes per second based on
// the MConnection's RecvRate and other configs.
// It is used to calculate the highest expected value for the peak receive rate.
// Note that the returned value is slightly higher than the configured RecvRate.
func (c *MConnection) maxRecvRate() int64 {
	sampleRate := c.recvMonitor.GetSampleRate().Seconds()
	numberOfSamplePerSeccond := 1 / sampleRate
	recvRate := float64(round(float64(c.config.RecvRate) * sampleRate))
	batchSizeBytes := float64(c._maxPacketMsgSize)
	effectiveRecvRatePerSample := math.Ceil(recvRate/batchSizeBytes) * batchSizeBytes
	effectiveRecvRate := round(numberOfSamplePerSeccond * effectiveRecvRatePerSample)

	return effectiveRecvRate
}

func TestMConnectionReceive(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}
	mconn1 := createMConnectionWithCallbacks(client, onReceive, onError)
	err := mconn1.Start()
	require.Nil(t, err)
	defer mconn1.Stop() //nolint:errcheck // ignore for tests

	mconn2 := createTestMConnection(server)
	err = mconn2.Start()
	require.Nil(t, err)
	defer mconn2.Stop() //nolint:errcheck // ignore for tests

	msg := []byte("Cyclops")
	assert.True(t, mconn2.Send(0x01, msg))

	select {
	case receivedBytes := <-receivedCh:
		assert.Equal(t, msg, receivedBytes)
	case err := <-errorsCh:
		t.Fatalf("Expected %s, got %+v", msg, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Did not receive %s message in 500ms", msg)
	}
}

func TestMConnectionStatus(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	mconn := createTestMConnection(client)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	status := mconn.Status()
	assert.NotNil(t, status)
	assert.Zero(t, status.Channels[0].SendQueueSize)
}

func TestMConnectionPongTimeoutResultsInError(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}
	mconn := createMConnectionWithCallbacks(client, onReceive, onError)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	serverGotPing := make(chan struct{})
	go func() {
		// read ping
		var pkt tmp2p.Packet
		_, err := protoio.NewDelimitedReader(server, maxPingPongPacketSize).ReadMsg(&pkt)
		require.NoError(t, err)
		serverGotPing <- struct{}{}
	}()
	<-serverGotPing

	pongTimerExpired := mconn.config.PongTimeout + 200*time.Millisecond
	select {
	case msgBytes := <-receivedCh:
		t.Fatalf("Expected error, but got %v", msgBytes)
	case err := <-errorsCh:
		assert.NotNil(t, err)
	case <-time.After(pongTimerExpired):
		t.Fatalf("Expected to receive error after %v", pongTimerExpired)
	}
}

func TestMConnectionMultiplePongsInTheBeginning(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}
	mconn := createMConnectionWithCallbacks(client, onReceive, onError)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	// sending 3 pongs in a row (abuse)
	protoWriter := protoio.NewDelimitedWriter(server)

	_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPong{}))
	require.NoError(t, err)

	_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPong{}))
	require.NoError(t, err)

	_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPong{}))
	require.NoError(t, err)

	serverGotPing := make(chan struct{})
	go func() {
		// read ping (one byte)
		var packet tmp2p.Packet
		_, err := protoio.NewDelimitedReader(server, maxPingPongPacketSize).ReadMsg(&packet)
		require.NoError(t, err)
		serverGotPing <- struct{}{}

		// respond with pong
		_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPong{}))
		require.NoError(t, err)
	}()
	<-serverGotPing

	pongTimerExpired := mconn.config.PongTimeout + 20*time.Millisecond
	select {
	case msgBytes := <-receivedCh:
		t.Fatalf("Expected no data, but got %v", msgBytes)
	case err := <-errorsCh:
		t.Fatalf("Expected no error, but got %v", err)
	case <-time.After(pongTimerExpired):
		assert.True(t, mconn.IsRunning())
	}
}

func TestMConnectionMultiplePings(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}
	mconn := createMConnectionWithCallbacks(client, onReceive, onError)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	// sending 3 pings in a row (abuse)
	// see https://github.com/cometbft/cometbft/issues/1190
	protoReader := protoio.NewDelimitedReader(server, maxPingPongPacketSize)
	protoWriter := protoio.NewDelimitedWriter(server)
	var pkt tmp2p.Packet

	_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPing{}))
	require.NoError(t, err)

	_, err = protoReader.ReadMsg(&pkt)
	require.NoError(t, err)

	_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPing{}))
	require.NoError(t, err)

	_, err = protoReader.ReadMsg(&pkt)
	require.NoError(t, err)

	_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPing{}))
	require.NoError(t, err)

	_, err = protoReader.ReadMsg(&pkt)
	require.NoError(t, err)

	assert.True(t, mconn.IsRunning())
}

func TestMConnectionPingPongs(t *testing.T) {
	// check that we are not leaking any go-routines
	defer leaktest.CheckTimeout(t, 10*time.Second)()

	server, client := net.Pipe()

	defer server.Close()
	defer client.Close()

	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}
	mconn := createMConnectionWithCallbacks(client, onReceive, onError)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	serverGotPing := make(chan struct{})
	go func() {
		protoReader := protoio.NewDelimitedReader(server, maxPingPongPacketSize)
		protoWriter := protoio.NewDelimitedWriter(server)
		var pkt tmp2p.PacketPing

		// read ping
		_, err = protoReader.ReadMsg(&pkt)
		require.NoError(t, err)
		serverGotPing <- struct{}{}

		// respond with pong
		_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPong{}))
		require.NoError(t, err)

		time.Sleep(mconn.config.PingInterval)

		// read ping
		_, err = protoReader.ReadMsg(&pkt)
		require.NoError(t, err)
		serverGotPing <- struct{}{}

		// respond with pong
		_, err = protoWriter.WriteMsg(mustWrapPacket(&tmp2p.PacketPong{}))
		require.NoError(t, err)
	}()
	<-serverGotPing
	<-serverGotPing

	pongTimerExpired := (mconn.config.PongTimeout + 20*time.Millisecond) * 2
	select {
	case msgBytes := <-receivedCh:
		t.Fatalf("Expected no data, but got %v", msgBytes)
	case err := <-errorsCh:
		t.Fatalf("Expected no error, but got %v", err)
	case <-time.After(2 * pongTimerExpired):
		assert.True(t, mconn.IsRunning())
	}
}

func TestMConnectionStopsAndReturnsError(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	receivedCh := make(chan []byte)
	errorsCh := make(chan interface{})
	onReceive := func(chID byte, msgBytes []byte) {
		receivedCh <- msgBytes
	}
	onError := func(r interface{}) {
		errorsCh <- r
	}
	mconn := createMConnectionWithCallbacks(client, onReceive, onError)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	if err := client.Close(); err != nil {
		t.Error(err)
	}

	select {
	case receivedBytes := <-receivedCh:
		t.Fatalf("Expected error, got %v", receivedBytes)
	case err := <-errorsCh:
		assert.NotNil(t, err)
		assert.False(t, mconn.IsRunning())
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Did not receive error in 500ms")
	}
}

func newClientAndServerConnsForReadErrors(t *testing.T, chOnErr chan struct{}) (*MConnection, *MConnection) {
	server, client := NetPipe()

	onReceive := func(chID byte, msgBytes []byte) {}
	onError := func(r interface{}) {}

	// create client conn with two channels
	chDescs := []*ChannelDescriptor{
		{ID: 0x01, Priority: 1, SendQueueCapacity: 1},
		{ID: 0x02, Priority: 1, SendQueueCapacity: 1},
	}
	mconnClient := NewMConnection(client, chDescs, onReceive, onError)
	mconnClient.SetLogger(log.TestingLogger().With("module", "client"))
	err := mconnClient.Start()
	require.Nil(t, err)

	// create server conn with 1 channel
	// it fires on chOnErr when there's an error
	serverLogger := log.TestingLogger().With("module", "server")
	onError = func(r interface{}) {
		chOnErr <- struct{}{}
	}
	mconnServer := createMConnectionWithCallbacks(server, onReceive, onError)
	mconnServer.SetLogger(serverLogger)
	err = mconnServer.Start()
	require.Nil(t, err)
	return mconnClient, mconnServer
}

func expectSend(ch chan struct{}) bool {
	after := time.After(time.Second * 5)
	select {
	case <-ch:
		return true
	case <-after:
		return false
	}
}

func TestMConnectionReadErrorBadEncoding(t *testing.T) {
	chOnErr := make(chan struct{})
	mconnClient, mconnServer := newClientAndServerConnsForReadErrors(t, chOnErr)

	client := mconnClient.conn

	// Write it.
	_, err := client.Write([]byte{1, 2, 3, 4, 5})
	require.NoError(t, err)
	assert.True(t, expectSend(chOnErr), "badly encoded msgPacket")

	t.Cleanup(func() {
		if err := mconnClient.Stop(); err != nil {
			t.Log(err)
		}
	})

	t.Cleanup(func() {
		if err := mconnServer.Stop(); err != nil {
			t.Log(err)
		}
	})
}

func TestMConnectionReadErrorUnknownChannel(t *testing.T) {
	chOnErr := make(chan struct{})
	mconnClient, mconnServer := newClientAndServerConnsForReadErrors(t, chOnErr)

	msg := []byte("Ant-Man")

	// fail to send msg on channel unknown by client
	assert.False(t, mconnClient.Send(0x03, msg))

	// send msg on channel unknown by the server.
	// should cause an error
	assert.True(t, mconnClient.Send(0x02, msg))
	assert.True(t, expectSend(chOnErr), "unknown channel")

	t.Cleanup(func() {
		if err := mconnClient.Stop(); err != nil {
			t.Log(err)
		}
	})

	t.Cleanup(func() {
		if err := mconnServer.Stop(); err != nil {
			t.Log(err)
		}
	})
}

func TestMConnectionReadErrorLongMessage(t *testing.T) {
	chOnErr := make(chan struct{})
	chOnRcv := make(chan struct{})

	mconnClient, mconnServer := newClientAndServerConnsForReadErrors(t, chOnErr)
	defer mconnClient.Stop() //nolint:errcheck // ignore for tests
	defer mconnServer.Stop() //nolint:errcheck // ignore for tests

	mconnServer.onReceive = func(chID byte, msgBytes []byte) {
		chOnRcv <- struct{}{}
	}

	client := mconnClient.conn
	protoWriter := protoio.NewDelimitedWriter(client)

	// send msg that's just right
	var packet = tmp2p.PacketMsg{
		ChannelID: 0x01,
		EOF:       true,
		Data:      make([]byte, mconnClient.config.MaxPacketMsgPayloadSize),
	}

	_, err := protoWriter.WriteMsg(mustWrapPacket(&packet))
	require.NoError(t, err)
	assert.True(t, expectSend(chOnRcv), "msg just right")

	// send msg that's too long
	packet = tmp2p.PacketMsg{
		ChannelID: 0x01,
		EOF:       true,
		Data:      make([]byte, mconnClient.config.MaxPacketMsgPayloadSize+100),
	}

	_, err = protoWriter.WriteMsg(mustWrapPacket(&packet))
	require.Error(t, err)
	assert.True(t, expectSend(chOnErr), "msg too long")
}

func TestMConnectionReadErrorUnknownMsgType(t *testing.T) {
	chOnErr := make(chan struct{})
	mconnClient, mconnServer := newClientAndServerConnsForReadErrors(t, chOnErr)
	defer mconnClient.Stop() //nolint:errcheck // ignore for tests
	defer mconnServer.Stop() //nolint:errcheck // ignore for tests

	// send msg with unknown msg type
	_, err := protoio.NewDelimitedWriter(mconnClient.conn).WriteMsg(&types.Header{ChainID: "x"})
	require.NoError(t, err)
	assert.True(t, expectSend(chOnErr), "unknown msg type")
}

func TestMConnectionTrySend(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	mconn := createTestMConnection(client)
	err := mconn.Start()
	require.Nil(t, err)
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	msg := []byte("Semicolon-Woman")
	resultCh := make(chan string, 2)
	assert.True(t, mconn.TrySend(0x01, msg))
	_, err = server.Read(make([]byte, len(msg)))
	require.NoError(t, err)
	assert.True(t, mconn.CanSend(0x01))
	assert.True(t, mconn.TrySend(0x01, msg))
	assert.False(t, mconn.CanSend(0x01))
	go func() {
		mconn.TrySend(0x01, msg)
		resultCh <- "TrySend"
	}()
	assert.False(t, mconn.CanSend(0x01))
	assert.False(t, mconn.TrySend(0x01, msg))
	assert.Equal(t, "TrySend", <-resultCh)
}

//nolint:lll //ignore line length for tests
func TestConnVectors(t *testing.T) {

	testCases := []struct {
		testName string
		msg      proto.Message
		expBytes string
	}{
		{"PacketPing", &tmp2p.PacketPing{}, "0a00"},
		{"PacketPong", &tmp2p.PacketPong{}, "1200"},
		{"PacketMsg", &tmp2p.PacketMsg{ChannelID: 1, EOF: false, Data: []byte("data transmitted over the wire")}, "1a2208011a1e64617461207472616e736d6974746564206f766572207468652077697265"},
	}

	for _, tc := range testCases {
		tc := tc

		pm := mustWrapPacket(tc.msg)
		bz, err := pm.Marshal()
		require.NoError(t, err, tc.testName)

		require.Equal(t, tc.expBytes, hex.EncodeToString(bz), tc.testName)
	}
}

func TestMConnectionChannelOverflow(t *testing.T) {
	chOnErr := make(chan struct{})
	chOnRcv := make(chan struct{})

	mconnClient, mconnServer := newClientAndServerConnsForReadErrors(t, chOnErr)
	t.Cleanup(stopAll(t, mconnClient, mconnServer))

	mconnServer.onReceive = func(chID byte, msgBytes []byte) {
		chOnRcv <- struct{}{}
	}

	client := mconnClient.conn
	protoWriter := protoio.NewDelimitedWriter(client)

	var packet = tmp2p.PacketMsg{
		ChannelID: 0x01,
		EOF:       true,
		Data:      []byte(`42`),
	}
	_, err := protoWriter.WriteMsg(mustWrapPacket(&packet))
	require.NoError(t, err)
	assert.True(t, expectSend(chOnRcv))

	packet.ChannelID = int32(1025)
	_, err = protoWriter.WriteMsg(mustWrapPacket(&packet))
	require.NoError(t, err)
	assert.False(t, expectSend(chOnRcv))

}

type stopper interface {
	Stop() error
}

func stopAll(t *testing.T, stoppers ...stopper) func() {
	return func() {
		for _, s := range stoppers {
			if err := s.Stop(); err != nil {
				t.Log(err)
			}
		}
	}
}

// generateAndSendMessages sends a sequence of messages to the specified multiplex connection `mc`.
// Each message has the given size and is sent at the specified rate
// `messagingRate`. This process continues for the duration `totalDuration` or
// until `totalNum` messages are sent. If `totalNum` is negative,
// messaging persists for the entire `totalDuration`.
func generateAndSendMessages(mc *MConnection,
	messagingRate time.Duration,
	totalDuration time.Duration, totalNum int, msgSize int,
	msgContnet []byte, chID byte) {
	var msg []byte
	// all messages have an identical content
	if msgContnet == nil {
		msg = bytes.Repeat([]byte{'x'}, msgSize)
	} else {
		msg = msgContnet
	}

	// message generation interval ticker
	ticker := time.NewTicker(messagingRate)
	defer ticker.Stop()

	// timer for the total duration
	timer := time.NewTimer(totalDuration)
	defer timer.Stop()

	sentNum := 0
	// generating messages
	for {
		select {
		case <-ticker.C:
			// generate message
			if mc.Send(chID, msg) {
				sentNum++
				if totalNum > 0 && sentNum >= totalNum {
					log.TestingLogger().Info("Completed the message generation as the" +
						" total number of messages is reached")
					return
				}
			}
		case <-timer.C:
			// time's up
			log.TestingLogger().Info("Completed the message generation as the total " +
				"duration is reached")
			return
		}
	}
}

func BenchmarkMConnection(b *testing.B) {
	chID := byte(0x01)

	// Testcases 1-3 evaluate the effect of send queue capacity on message transmission delay.

	// Testcases 3-5 assess the full utilization of maximum bandwidth by
	// incrementally increasing the total load while keeping send and
	// receive rates constant.
	// The transmission time should be ~ totalMsg*msgSize/sendRate,
	// indicating that the actual sendRate is in effect and has been
	// utilized.

	// Testcases 5-7 assess if surpassing available bandwidth maintains
	// consistent transmission delay without congestion or performance
	// degradation. A uniform delay across these testcases is expected.

	tests := []struct {
		name              string
		msgSize           int           // size of each message in bytes
		totalMsg          int           // total number of messages to be sent
		messagingRate     time.Duration // rate at which messages are sent
		totalDuration     time.Duration // total duration for which messages are sent
		sendQueueCapacity int           // send queue capacity i.e., the number of messages that can be buffered
		sendRate          int64         // send rate in bytes per second
		recRate           int64         // receive rate in bytes per second
	}{
		{
			// testcase 1
			// Sends one 1KB message every 20ms, totaling 50 messages (50KB/s) per second.
			// Ideal transmission time for 50 messages is ~1 second.
			// SendQueueCapacity should not affect transmission delay.
			name: "queue capacity = 1, " +
				"total load = 50 KB, " +
				"msg rate = send rate",
			msgSize:           1 * 1024,
			totalMsg:          1 * 50,
			sendQueueCapacity: 1,
			messagingRate:     20 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
		{
			// testcase 2
			// Sends one 1KB message every 20ms, totaling 50 messages (50KB/s) per second.
			// Ideal transmission time for 50 messages is ~1 second.
			// Increasing SendQueueCapacity should not affect transmission
			// delay.
			name: "queue capacity = 50, " +
				"total load = 50 KB, " +
				"traffic rate = send rate",
			msgSize:           1 * 1024,
			totalMsg:          1 * 50,
			sendQueueCapacity: 50,
			messagingRate:     20 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
		{
			// testcase 3
			// Sends one 1KB message every 20ms, totaling 50 messages (50KB/s) per second.
			// Ideal transmission time for 50 messages is ~1 second.
			// Increasing SendQueueCapacity should not affect transmission
			// delay.
			name: "queue capacity = 100, " +
				"total load = 50 KB, " +
				"traffic rate = send rate",
			msgSize:           1 * 1024,
			totalMsg:          1 * 50,
			sendQueueCapacity: 100,
			messagingRate:     20 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
		{
			// testcase 4
			// Sends one 1KB message every 20ms, totaling 50 messages (50KB/s) per second.
			// The test runs for 100 messages, expecting a total transmission time of ~2 seconds.
			name: "queue capacity = 100, " +
				"total load = 2 * 50 KB, " +
				"traffic rate = send rate",
			msgSize:           1 * 1024,
			totalMsg:          2 * 50,
			sendQueueCapacity: 100,
			messagingRate:     20 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
		{
			// testcase 5
			// Sends one 1KB message every 20ms, totaling 50 messages (50KB/s) per second.
			// The test runs for 400 messages,
			// expecting a total transmission time of ~8 seconds.
			name: "queue capacity = 100, " +
				"total load = 8 * 50 KB, " +
				"traffic rate = send rate",
			msgSize:           1 * 1024,
			totalMsg:          8 * 50,
			sendQueueCapacity: 100,
			messagingRate:     20 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
		{
			// testcase 6
			// Sends one 1KB message every 10ms, totaling 100 messages (100KB/s) per second.
			// The test runs for 400 messages,
			// expecting a total transmission time of ~8 seconds.
			name: "queue capacity = 100, " +
				"total load = 8 * 50 KB, " +
				"traffic rate = 2 * send rate",
			msgSize:           1 * 1024,
			totalMsg:          8 * 50,
			sendQueueCapacity: 100,
			messagingRate:     10 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
		{
			// testcase 7
			// Sends one 1KB message every 2ms, totaling 500 messages (500KB/s) per second.
			// The test runs for 400 messages,
			// expecting a total transmission time of ~8 seconds.
			name: "queue capacity = 100, " +
				"total load = 8 * 50 KB, " +
				"traffic rate = 10 * send rate",
			msgSize:           1 * 1024,
			totalMsg:          8 * 50,
			sendQueueCapacity: 100,
			messagingRate:     2 * time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendRate:          50 * 1024,
			recRate:           50 * 1024,
		},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				// set up two networked connections
				// server, client := NetPipe() // can alternatively use this and comment out the line below
				server, client := tcpNetPipe()
				defer server.Close()
				defer client.Close()

				// prepare callback to receive messages
				allReceived := make(chan bool)
				receivedLoad := 0 // number of messages received
				onReceive := func(chID byte, msgBytes []byte) {
					receivedLoad++
					if receivedLoad >= tt.totalMsg && tt.totalMsg > 0 {
						allReceived <- true
					}
				}

				cnfg := DefaultMConnConfig()
				cnfg.SendRate = tt.sendRate
				cnfg.RecvRate = tt.recRate
				chDescs := []*ChannelDescriptor{{ID: chID, Priority: 1,
					SendQueueCapacity: tt.sendQueueCapacity}}
				clientMconn := NewMConnectionWithConfig(client, chDescs,
					func(chID byte, msgBytes []byte) {},
					func(r interface{}) {},
					cnfg)
				serverChDescs := []*ChannelDescriptor{{ID: chID, Priority: 1,
					SendQueueCapacity: tt.sendQueueCapacity}}
				serverMconn := NewMConnectionWithConfig(server, serverChDescs,
					onReceive,
					func(r interface{}) {},
					cnfg)
				clientMconn.SetLogger(log.TestingLogger())
				serverMconn.SetLogger(log.TestingLogger())

				err := clientMconn.Start()
				require.Nil(b, err)
				defer func() {
					_ = clientMconn.Stop()
				}()
				err = serverMconn.Start()
				require.Nil(b, err)
				defer func() {
					_ = serverMconn.Stop()
				}()

				// start measuring the time from here to exclude the time
				// taken to set up the connections
				b.StartTimer()
				// start generating messages, it is a blocking call
				generateAndSendMessages(clientMconn,
					tt.messagingRate,
					tt.totalDuration,
					tt.totalMsg,
					tt.msgSize,
					nil, chID)

				// wait for all messages to be received
				<-allReceived
				b.StopTimer()
			}
		})

	}

}

// tcpNetPipe creates a pair of connected net.Conn objects that can be used in tests.
func tcpNetPipe() (net.Conn, net.Conn) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	var conn1 net.Conn
	var wg sync.WaitGroup
	wg.Add(1)
	go func(c *net.Conn) {
		*c, _ = ln.Accept()
		wg.Done()
	}(&conn1)

	addr := ln.Addr().String()
	conn2, _ := net.Dial("tcp", addr)

	wg.Wait()

	return conn2, conn1
}

// generateExponentialSizedMessages creates and returns a series of messages
// with sizes (in the specified unit) increasing exponentially.
// The size of each message doubles, starting from 1 up to maxSizeBytes.
// unit is expected to be a power of 2.
func generateExponentialSizedMessages(maxSizeBytes int, unit int) [][]byte {
	maxSizeToUnit := maxSizeBytes / unit
	msgs := make([][]byte, 0)

	for size := 1; size <= maxSizeToUnit; size *= 2 {
		msgs = append(msgs, bytes.Repeat([]byte{'x'}, size*unit)) // create a message of the calculated size
	}
	return msgs
}

type testCase struct {
	name              string
	msgSize           int           // size of each message in bytes
	msg               []byte        // message to be sent
	totalMsg          int           // total number of messages to be sent
	messagingRate     time.Duration // rate at which messages are sent
	totalDuration     time.Duration // total duration for which messages are sent
	sendQueueCapacity int           // send queue capacity i.e., the number of messages that can be buffered
	sendRate          int64         // send rate in bytes per second
	recRate           int64         // receive rate in bytes per second
	chID              byte          // channel ID
}

func runBenchmarkTest(b *testing.B, tt testCase) {
	b.Run(tt.name, func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			// set up two networked connections
			// server, client := NetPipe() // can alternatively use this and comment out the line below
			server, client := tcpNetPipe()
			defer server.Close()
			defer client.Close()

			// prepare callback to receive messages
			allReceived := make(chan bool)
			receivedLoad := 0 // number of messages received
			onReceive := func(chID byte, msgBytes []byte) {
				receivedLoad++
				if receivedLoad >= tt.totalMsg && tt.totalMsg > 0 {
					allReceived <- true
				}
			}

			cnfg := DefaultMConnConfig()
			cnfg.SendRate = tt.sendRate
			cnfg.RecvRate = tt.recRate
			chDescs := []*ChannelDescriptor{{ID: tt.chID, Priority: 1,
				SendQueueCapacity: tt.sendQueueCapacity}}
			clientMconn := NewMConnectionWithConfig(client, chDescs,
				func(chID byte, msgBytes []byte) {},
				func(r interface{}) {},
				cnfg)
			serverChDescs := []*ChannelDescriptor{{ID: tt.chID, Priority: 1,
				SendQueueCapacity: tt.sendQueueCapacity}}
			serverMconn := NewMConnectionWithConfig(server, serverChDescs,
				onReceive,
				func(r interface{}) {},
				cnfg)
			clientMconn.SetLogger(log.TestingLogger())
			serverMconn.SetLogger(log.TestingLogger())

			err := clientMconn.Start()
			require.Nil(b, err)
			defer func() {
				_ = clientMconn.Stop()
			}()
			err = serverMconn.Start()
			require.Nil(b, err)
			defer func() {
				_ = serverMconn.Stop()
			}()

			// start measuring the time from here to exclude the time
			// taken to set up the connections
			b.StartTimer()
			// start generating messages, it is a blocking call
			generateAndSendMessages(clientMconn,
				tt.messagingRate,
				tt.totalDuration,
				tt.totalMsg,
				tt.msgSize,
				tt.msg,
				tt.chID)

			// wait for all messages to be received
			<-allReceived
			b.StopTimer()
		}
	})
}

func BenchmarkMConnection_ScalingPayloadSizes_LowSendRate(b *testing.B) {
	// This benchmark test builds upon the previous one i.e.,
	// BenchmarkMConnection_ScalingPayloadSizes_HighSendRate
	// by setting the send/and receive rates lower than the message load.
	// Test cases involve sending the same load of messages but with different message sizes.
	// Since the message load and bandwidth are consistent across all test cases,
	// they are expected to complete in the same amount of time. i.e.,
	//totalLoad/sendRate.

	maxSize := 32 * 1024 // 32KB
	msgs := generateExponentialSizedMessages(maxSize, 1024)
	totalLoad := float64(maxSize)
	chID := byte(0x01)
	// create test cases for each message size
	var testCases = make([]testCase, len(msgs))
	for i, msg := range msgs {
		msgSize := len(msg)
		totalMsg := int(math.Ceil(totalLoad / float64(msgSize)))
		testCases[i] = testCase{
			name:              fmt.Sprintf("msgSize = %d KB", msgSize/1024),
			msgSize:           msgSize,
			msg:               msg,
			totalMsg:          totalMsg,
			messagingRate:     time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendQueueCapacity: 100,
			sendRate:          4 * 1024,
			recRate:           4 * 1024,
			chID:              chID,
		}
	}

	for _, tt := range testCases {
		runBenchmarkTest(b, tt)
	}
}

func BenchmarkMConnection_ScalingPayloadSizes_HighSendRate(b *testing.B) {
	// One aspect that could impact the performance of MConnection and the
	// transmission rate is the size of the messages sent over the network,
	// especially when they exceed the MConnection.MaxPacketMsgPayloadSize (
	// messages are sent in packets of maximum size MConnection.
	// MaxPacketMsgPayloadSize).
	// The test cases in this benchmark involve sending messages with sizes
	// ranging exponentially from 1KB to 4096KB (
	// the max value of 4096KB is inspired by the largest possible PFB in a
	// Celestia block with 128*18 number of 512-byte shares)
	// The bandwidth is set significantly higher than the message load to ensure
	// it does not become a limiting factor.
	// All test cases are expected to complete in less than one second,
	// indicating a healthy performance.

	squareSize := 128                              // number of shares in a row/column
	shareSize := 512                               // bytes
	maxSize := squareSize * squareSize * shareSize // bytes
	msgs := generateExponentialSizedMessages(maxSize, 1024)
	chID := byte(0x01)

	// create test cases for each message size
	var testCases = make([]testCase, len(msgs))
	for i, msg := range msgs {
		testCases[i] = testCase{
			name:              fmt.Sprintf("msgSize = %d KB", len(msg)/1024),
			msgSize:           len(msg),
			msg:               msg,
			totalMsg:          10,
			messagingRate:     time.Millisecond,
			totalDuration:     1 * time.Minute,
			sendQueueCapacity: 100,
			sendRate:          512 * 1024 * 1024,
			recRate:           512 * 1024 * 1024,
			chID:              chID,
		}
	}

	for _, tt := range testCases {
		runBenchmarkTest(b, tt)
	}
}
