package conn

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
)

const testByteLimitChannelID = byte(0x01)

// newByteLimitedChannel returns a channel of a connection that is not started,
// so nothing leaves the send queue unless the test sends it.
func newByteLimitedChannel(t *testing.T, messageCapacity int, byteCapacity int) *Channel {
	t.Helper()
	server, client := NetPipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})

	descriptor := &ChannelDescriptor{
		ID:                     testByteLimitChannelID,
		Priority:               1,
		SendQueueCapacity:      messageCapacity,
		SendQueueCapacityBytes: byteCapacity,
	}
	noReceive := func(byte, []byte) {}
	noError := func(interface{}) {}
	connection := NewMConnectionWithConfig(client, []*ChannelDescriptor{descriptor}, noReceive, noError, DefaultMConnConfig())
	return connection.channelsIdx[testByteLimitChannelID]
}

func queuedBytes(channel *Channel) int64 {
	return atomic.LoadInt64(&channel.sendQueueBytes)
}

// sendNextMessage takes the next message from the send queue and cuts all of
// it into packets, like the send routine does when it writes a message.
func sendNextMessage(t *testing.T, channel *Channel) {
	t.Helper()
	require.True(t, channel.isSendPending())
	for {
		packet := channel.nextPacketMsg()
		if packet.EOF {
			return
		}
	}
}

func TestSendQueueBytesRefusesMessageOverLimit(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 1000)
	message := make([]byte, 400)

	require.True(t, channel.trySendBytes(message, false))
	require.True(t, channel.trySendBytes(message, false))
	require.False(t, channel.trySendBytes(message, false), "a third message would put 1200 bytes in a 1000 byte queue")

	require.Equal(t, int64(800), queuedBytes(channel), "a refused message must not be counted")
	require.Equal(t, 2, channel.loadSendQueueSize())

	smallMessage := make([]byte, 200)
	require.True(t, channel.trySendBytes(smallMessage, false), "a message that fits exactly is accepted")
	require.Equal(t, int64(1000), queuedBytes(channel))
}

func TestSendQueueBytesReleasedWhenMessageIsSent(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 1000)
	message := make([]byte, 600)

	require.True(t, channel.trySendBytes(message, false))
	require.False(t, channel.trySendBytes(message, false))

	sendNextMessage(t, channel)
	require.Equal(t, int64(0), queuedBytes(channel))
	require.True(t, channel.trySendBytes(message, false), "the queue has room again once the message is sent")
}

func TestSendQueueBytesCountsMessageUntilLastPacket(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 1000)
	channel.maxPacketMsgPayloadSize = 100
	message := make([]byte, 250)
	require.True(t, channel.trySendBytes(message, false))

	require.True(t, channel.isSendPending())
	firstPacket := channel.nextPacketMsg()
	require.False(t, firstPacket.EOF)
	require.Equal(t, int64(250), queuedBytes(channel), "a message that is partly sent still counts in full")

	secondPacket := channel.nextPacketMsg()
	require.False(t, secondPacket.EOF)
	lastPacket := channel.nextPacketMsg()
	require.True(t, lastPacket.EOF)
	require.Equal(t, int64(0), queuedBytes(channel))
}

func TestSendQueueBytesZeroMeansNoByteLimit(t *testing.T) {
	channel := newByteLimitedChannel(t, 3, 0)
	message := make([]byte, 1<<20)

	require.True(t, channel.trySendBytes(message, false))
	require.True(t, channel.trySendBytes(message, false))
	require.True(t, channel.trySendBytes(message, false))
	require.Equal(t, int64(3<<20), queuedBytes(channel), "bytes are counted even without a byte limit")

	require.False(t, channel.trySendBytes(message, false), "the message count limit still applies")
	require.Equal(t, int64(3<<20), queuedBytes(channel), "a message refused by the count limit must not be counted")
}

func TestSendQueueBytesAcceptsOversizedMessageInEmptyQueue(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 1000)
	oversizedMessage := make([]byte, 5000)
	smallMessage := make([]byte, 1)

	require.True(t, channel.trySendBytes(oversizedMessage, false), "an empty queue takes one message of any size")
	require.False(t, channel.trySendBytes(smallMessage, false))
	require.False(t, channel.trySendBytes(oversizedMessage, false))

	sendNextMessage(t, channel)
	require.True(t, channel.trySendBytes(smallMessage, false))
}

func TestSendQueueBytesBulkMessagesCannotUseReservedBytes(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 1000)
	channel.desc.SendQueueReservedBytes = 300
	message := make([]byte, 350)

	require.True(t, channel.trySendBulkBytes(message))
	require.True(t, channel.trySendBulkBytes(message))
	require.False(t, channel.trySendBulkBytes(message), "bulk messages can fill 700 of the 1000 bytes")
	require.Equal(t, int64(700), queuedBytes(channel))

	smallMessage := make([]byte, 300)
	require.True(t, channel.trySendBytes(smallMessage, false), "other messages can use the reserved bytes")
	require.False(t, channel.trySendBytes(smallMessage, false), "the whole queue is full now")
	require.Equal(t, int64(1000), queuedBytes(channel))
}

func TestSendQueueBytesBulkLimitIgnoredWithoutByteLimit(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 0)
	channel.desc.SendQueueReservedBytes = 300
	message := make([]byte, 1<<20)

	require.True(t, channel.trySendBulkBytes(message))
	require.True(t, channel.trySendBulkBytes(message), "a channel without a byte limit has no bulk limit either")
}

// The peer never reads, so the queue only fills up.
func TestMConnectionTrySendBulk(t *testing.T) {
	server, client := NetPipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})

	const mebibyte = 1 << 20
	descriptor := &ChannelDescriptor{
		ID:                     testByteLimitChannelID,
		Priority:               1,
		SendQueueCapacity:      100,
		SendQueueCapacityBytes: 10 * mebibyte,
		SendQueueReservedBytes: 4 * mebibyte,
	}
	noReceive := func(byte, []byte) {}
	noError := func(interface{}) {}
	connection := NewMConnectionWithConfig(client, []*ChannelDescriptor{descriptor}, noReceive, noError, DefaultMConnConfig())
	connection.SetLogger(log.TestingLogger())
	require.NoError(t, connection.Start())
	t.Cleanup(func() { _ = connection.Stop() })

	message := make([]byte, mebibyte)
	bulkAccepted := 0
	for connection.TrySendBulk(testByteLimitChannelID, message) {
		bulkAccepted++
		require.Less(t, bulkAccepted, 100, "bulk messages must be refused at some point")
	}
	require.GreaterOrEqual(t, bulkAccepted, 6, "bulk messages can fill the queue up to the reserved bytes")

	require.True(t, connection.TrySend(testByteLimitChannelID, message), "other messages can use the reserved bytes")
	require.False(t, connection.TrySendBulk(testByteLimitChannelID, message))

	status := connection.Status().Channels[0]
	require.LessOrEqual(t, status.SendQueueBytes, int64(10*mebibyte))
}

func TestSendQueueBytesConcurrentSendersStayUnderLimit(t *testing.T) {
	const (
		byteCapacity = 64_000
		messageSize  = 1000
		senders      = 16
		sendsEach    = 200
	)
	channel := newByteLimitedChannel(t, senders*sendsEach, byteCapacity)

	var accepted atomic.Int64
	var waitGroup sync.WaitGroup
	for sender := 0; sender < senders; sender++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			message := make([]byte, messageSize)
			for send := 0; send < sendsEach; send++ {
				if channel.trySendBytes(message, false) {
					accepted.Add(1)
				}
			}
		}()
	}
	waitGroup.Wait()

	acceptedBytes := accepted.Load() * messageSize
	require.Positive(t, acceptedBytes)
	require.LessOrEqual(t, acceptedBytes, int64(byteCapacity))
	require.Equal(t, acceptedBytes, queuedBytes(channel))
	require.Equal(t, int(accepted.Load()), channel.loadSendQueueSize())
}

func TestSendQueueBytesBlockingSendWaitsForRoom(t *testing.T) {
	channel := newByteLimitedChannel(t, 100, 1000)
	message := make([]byte, 600)
	require.True(t, channel.trySendBytes(message, false))

	sent := make(chan bool, 1)
	go func() {
		sent <- channel.sendBytes(message, false)
	}()

	select {
	case <-sent:
		t.Fatal("the blocking send must wait while the queue has no room")
	case <-time.After(5 * sendQueueBytesPollInterval):
	}

	sendNextMessage(t, channel)

	select {
	case ok := <-sent:
		require.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("the blocking send did not go through after the queue got room")
	}
	require.Equal(t, int64(600), queuedBytes(channel))
}

func TestMConnectionStatusReportsSendQueueBytes(t *testing.T) {
	server, client := NetPipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})

	const byteCapacity = 1 << 20
	descriptor := &ChannelDescriptor{
		ID:                     testByteLimitChannelID,
		Priority:               1,
		SendQueueCapacity:      100,
		SendQueueCapacityBytes: byteCapacity,
	}
	noReceive := func(byte, []byte) {}
	noError := func(interface{}) {}
	connection := NewMConnectionWithConfig(client, []*ChannelDescriptor{descriptor}, noReceive, noError, DefaultMConnConfig())
	channel := connection.channelsIdx[testByteLimitChannelID]

	require.True(t, channel.trySendBytes(make([]byte, 300), false))
	require.True(t, channel.trySendBytes(make([]byte, 200), false))

	status := connection.Status().Channels[0]
	require.Equal(t, byteCapacity, status.SendQueueCapacityBytes)
	require.Equal(t, int64(500), status.SendQueueBytes)
	require.Equal(t, 2, status.SendQueueSize)
}
