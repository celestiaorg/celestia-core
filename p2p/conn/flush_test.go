package conn

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
)

// TestMConnectionFlushesWhenSendQueuesDrain checks that a message reaches the
// socket as soon as nothing else is waiting to be sent, without waiting for
// the flush throttle timer.
func TestMConnectionFlushesWhenSendQueuesDrain(t *testing.T) {
	server, client := NetPipe()
	defer server.Close()
	defer client.Close()

	cfg := DefaultMConnConfig()
	cfg.FlushThrottle = 5 * time.Second

	chDescs := []*ChannelDescriptor{{ID: 0x01, Priority: 1, SendQueueCapacity: 1}}
	mconn := NewMConnectionWithConfig(client, chDescs, func(byte, []byte) {}, func(interface{}) {}, cfg)
	mconn.SetLogger(log.TestingLogger())
	require.NoError(t, mconn.Start())
	defer mconn.Stop() //nolint:errcheck // ignore for tests

	received := make(chan struct{}, 8)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				received <- struct{}{}
			}
		}
	}()

	for i := 0; i < 3; i++ {
		require.True(t, mconn.Send(0x01, []byte("a lone message")))
		select {
		case <-received:
		case <-time.After(time.Second):
			t.Fatalf("message %d waited for the throttle timer", i)
		}
	}
}
