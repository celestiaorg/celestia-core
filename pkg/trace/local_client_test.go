package trace

import (
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

func (c Cannal) InfluxRepr() (map[string]interface{}, error) {
	return map[string]interface{}{
		"city":   c.Ville,
		"length": c.Longueur,
	}, nil
}

// TestLocalClientIntegrationTest tests the local client by writing some events,
// reading them back and comparing them, writing at the same time as reading,
// then uploads the files to a server and compares those.
func TestLocalClientIntegrationTest(t *testing.T) {
	// Setup
	client := setupLocalClient(t)

	annecy := Cannal{"Annecy", 420}
	paris := Cannal{"Paris", 420}
	client.Write(annecy)
	client.Write(paris)

	// Wait for the write to complete.
	time.Sleep(100 * time.Millisecond)

	f, err := client.ReadTable(CannalTable)
	require.NoError(t, err)

	events, err := ScanDecodeFile[Cannal](f)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 2)
	require.Equal(t, annecy, events[0].Msg)
	require.Equal(t, paris, events[1].Msg)

	migenees := Cannal{"Migennes", 620}
	pontivy := Cannal{"Pontivy", 720}
	client.Write(migenees)
	client.Write(pontivy)
	time.Sleep(100 * time.Millisecond)
	events, err = ScanDecodeFile[Cannal](f)
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, migenees, events[2].Msg)
	require.Equal(t, pontivy, events[3].Msg)
}

func setupLocalClient(t *testing.T) *LocalClient {
	logger := log.NewNopLogger()
	cfg := config.DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.Instrumentation.TraceDB = "test"
	cfg.Instrumentation.TraceBufferSize = 100
	cfg.Instrumentation.TracingTables = CannalTable
	cfg.Instrumentation.TracePullAddress = "http://localhost:42042/upload"

	client, err := NewLocalClient(cfg, logger, "test_chain", "test_node")
	if err != nil {
		t.Fatalf("failed to create local client: %v", err)
	}

	return client
}
