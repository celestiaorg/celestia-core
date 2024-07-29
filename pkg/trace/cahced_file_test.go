package trace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	cmtlog "github.com/tendermint/tendermint/libs/log"
)

func Test_cachedFile(t *testing.T) {
	type test struct {
		name           string
		cacheSize      int
		events         int
		close          bool
		readEventsFunc func([]Event[TestEntry]) bool
	}
	tests := []test{
		{"don't exceed write threshold", 10, 5, false, func(e []Event[TestEntry]) bool { return len(e) == 0 }},
		{"close writes all cached events", 10, 5, true, func(e []Event[TestEntry]) bool { return len(e) == 5 }},
		{"exceed write threshold", 10, 10, true, func(e []Event[TestEntry]) bool { return len(e) == 10 }},
		{"doesn't block when buffer is full", 10, 100, true, func(e []Event[TestEntry]) bool {
			return len(e) < 1000 && len(e) > 0
		},
		},
	}

	logger := cmtlog.TestingLogger()
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				tmp := t.TempDir()
				fdir := filepath.Join(tmp, "test.jsonl")
				f, err := os.OpenFile(fdir, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0777)
				require.NoError(t, err)

				cf := newCachedFile(f, logger, tt.cacheSize, 10)

				events := generateEvents(tt.events)

				for _, event := range events {
					cf.Cache(event)
				}

				if tt.close {
					err = cf.Close()
					require.NoError(t, err)
				}

				file, err := os.OpenFile(fdir, os.O_RDONLY, 0777)
				require.NoError(t, err)

				entries, err := DecodeFile[TestEntry](file)
				require.NoError(t, err)

				require.True(t, tt.readEventsFunc(entries))
			},
		)
	}
}

var _ Entry = &TestEntry{}

type TestEntry struct {
	table string
}

func (te *TestEntry) Table() string {
	return te.table
}

func generateEvents(count int) []Event[Entry] {
	events := make([]Event[Entry], 0, count)
	for i := 0; i < count; i++ {
		entry := Entry(&TestEntry{"test"})
		events = append(events, NewEvent("test", "test", "test", entry))
	}
	return events
}
