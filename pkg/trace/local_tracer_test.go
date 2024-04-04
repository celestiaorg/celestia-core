package trace

import (
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"testing"
	"time"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/stretchr/testify/require"
)

const (
	// CannalTable is the table name for the Cannal struct.
	CannalTable = "cannal"
)

type Cannal struct {
	Ville    string `json:"city"`
	Longueur int    `json:"length"`
}

func (c Cannal) Table() string {
	return CannalTable
}

// TestLocalTracerReadWrite tests the local client by writing some events,
// reading them back and comparing them, writing at the same time as reading.
func TestLocalTracerReadWrite(t *testing.T) {
	port, err := getFreePort()
	client := setupLocalTracer(t, port)

	annecy := Cannal{"Annecy", 420}
	paris := Cannal{"Paris", 420}
	client.Write(annecy)
	client.Write(paris)

	time.Sleep(100 * time.Millisecond)

	f, err := client.ReadTable(CannalTable)
	require.NoError(t, err)

	// write at the same time as reading to test thread safety this test will be
	// flakey if this is not being handled correctly
	migenees := Cannal{"Migennes", 620}
	pontivy := Cannal{"Pontivy", 720}
	client.Write(migenees)
	client.Write(pontivy)

	events, err := DecodeFile[Cannal](f)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 2)
	require.Equal(t, annecy, events[0].Msg)
	require.Equal(t, paris, events[1].Msg)
	f.Close()

	time.Sleep(100 * time.Millisecond)

	f, err = client.ReadTable(CannalTable)
	require.NoError(t, err)
	defer f.Close()
	events, err = DecodeFile[Cannal](f)
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, migenees, events[2].Msg)
	require.Equal(t, pontivy, events[3].Msg)
}

func setupLocalTracer(t *testing.T, port int) *LocalTracer {
	logger := log.NewNopLogger()
	cfg := config.DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.Instrumentation.TraceBufferSize = 100
	cfg.Instrumentation.TracingTables = CannalTable
	cfg.Instrumentation.TracePullAddress = fmt.Sprintf(":%d", port)

	client, err := NewLocalTracer(cfg, logger, "test_chain", "test_node")
	if err != nil {
		t.Fatalf("failed to create local client: %v", err)
	}

	return client
}

// TestLocalTracerServerPull tests the pull portion of the server.
func TestLocalTracerServerPull(t *testing.T) {
	port, err := getFreePort()
	require.NoError(t, err)
	client := setupLocalTracer(t, port)

	for i := 0; i < 5; i++ {
		client.Write(Cannal{"Annecy", i})
	}

	// Wait for the server to start
	time.Sleep(100 * time.Millisecond)

	// Test the server
	newDir := t.TempDir()

	url := fmt.Sprintf("http://localhost:%d", port)

	// try to read a table that is not being collected. error expected.
	err = GetTable(url, "canal", newDir)
	require.Error(t, err)

	err = GetTable(url, CannalTable, newDir)
	require.NoError(t, err)

	originalFile, err := client.ReadTable(CannalTable)
	require.NoError(t, err)
	defer originalFile.Close()
	originalBz, err := io.ReadAll(originalFile)
	require.NoError(t, err)

	path := path.Join(newDir, CannalTable+".jsonl")
	downloadedFile, err := os.Open(path)
	require.NoError(t, err)
	defer downloadedFile.Close()

	downloadedBz, err := io.ReadAll(downloadedFile)
	require.NoError(t, err)
	require.Equal(t, originalBz, downloadedBz)

	_, err = downloadedFile.Seek(0, 0) // reset the seek on the file to read it again
	require.NoError(t, err)
	events, err := DecodeFile[Cannal](downloadedFile)
	require.NoError(t, err)
	require.Len(t, events, 5)
	for i := 0; i < 5; i++ {
		require.Equal(t, i, events[i].Msg.Longueur)
	}
}

// getFreePort returns a free port and optionally an error.
func getFreePort() (int, error) {
	a, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", a)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
