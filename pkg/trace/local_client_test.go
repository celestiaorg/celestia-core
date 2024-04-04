package trace

import (
	"io"
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

// TestLocalClientReadWrite tests the local client by writing some events,
// reading them back and comparing them, writing at the same time as reading.
func TestLocalClientReadWrite(t *testing.T) {
	// Setup
	client := setupLocalClient(t)

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

func setupLocalClient(t *testing.T) *LocalClient {
	logger := log.NewNopLogger()
	cfg := config.DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.Instrumentation.TraceBufferSize = 100
	cfg.Instrumentation.TracingTables = CannalTable
	cfg.Instrumentation.TracePushURL = ":42042"

	client, err := NewLocalClient(cfg, logger, "test_chain", "test_node")
	if err != nil {
		t.Fatalf("failed to create local client: %v", err)
	}

	return client
}

// TestLocalClientServerPull tests the pull portion of the server.
func TestLocalClientServerPull(t *testing.T) {
	// Setup
	client := setupLocalClient(t)

	for i := 0; i < 5; i++ {
		client.Write(Cannal{"Annecy", i})
	}

	go client.servePullData()

	// Wait for the server to start
	time.Sleep(100 * time.Millisecond)

	// Test the server
	newDir := t.TempDir()

	// try to read a table that is not being collected. error expected.
	err := GetTable("http://localhost:42042/get_table", "canal", newDir)
	require.Error(t, err)

	err = GetTable("http://localhost:42042/get_table", CannalTable, newDir)
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

	downloadedFile.Seek(0, 0) // reset the seek on the file to read it again
	events, err := DecodeFile[Cannal](downloadedFile)
	require.NoError(t, err)
	require.Len(t, events, 5)
	for i := 0; i < 5; i++ {
		require.Equal(t, i, events[i].Msg.Longueur)
	}
}
