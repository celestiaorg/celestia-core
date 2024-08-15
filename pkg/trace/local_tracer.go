package trace

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/libs/log"
)

// Event wraps some trace data with metadata that dictates the table and things
// like the chainID and nodeID.
type Event[T any] struct {
	ChainID   string    `json:"chain_id"`
	NodeID    string    `json:"node_id"`
	Table     string    `json:"table"`
	Timestamp time.Time `json:"timestamp"`
	Msg       T         `json:"msg"`
}

// NewEvent creates a new Event with the given chainID, nodeID, table, and msg.
// It adds the current time as the timestamp.
func NewEvent[T any](chainID, nodeID, table string, msg T) Event[T] {
	return Event[T]{
		ChainID:   chainID,
		NodeID:    nodeID,
		Table:     table,
		Msg:       msg,
		Timestamp: time.Now(),
	}
}

// LocalTracer saves all of the events passed to the retuen channel to files
// based on their "type" (a string field in the event). Each type gets its own
// file. The internals are purposefully not *explicitly* thread safe to avoid the
// overhead of locking with each event save. Only pass events to the returned
// channel. Call CloseAll to close all open files.
type LocalTracer struct {
	chainID, nodeID string
	logger          log.Logger
	cfg             *config.Config

	// fileMap maps tables to their open files files are threadsafe, but the map
	// is not. Therefore don't create new files after initialization to remain
	// threadsafe.
	fileMap map[string]*cachedFile
}

// NewLocalTracer creates a struct that will save all of the events passed to
// the retuen channel to files based on their "table" (a string field in the
// event). Each type gets its own file. The internal are purposefully not thread
// safe to avoid the overhead of locking with each event save. Only pass events
// to the returned channel. Call CloseAll to close all open files. Goroutine to
// save events is started in this function.
func NewLocalTracer(cfg *config.Config, logger log.Logger, chainID, nodeID string) (*LocalTracer, error) {
	fm := make(map[string]*cachedFile)
	p := path.Join(cfg.RootDir, "data", "traces")
	for _, table := range splitAndTrimEmpty(cfg.Instrumentation.TracingTables, ",", " ") {
		fileName := fmt.Sprintf("%s/%s.jsonl", p, table)
		err := os.MkdirAll(p, 0700)
		if err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", p, err)
		}
		file, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_SYNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open or create file %s: %w", fileName, err)
		}

		fm[table] = newCachedFile(file, logger, cfg.Instrumentation.TraceBufferSize, 10)
	}

	lt := &LocalTracer{
		fileMap: fm,
		cfg:     cfg,
		chainID: chainID,
		nodeID:  nodeID,
		logger:  logger,
	}

	return lt, nil
}

func (lt *LocalTracer) Write(e Entry) {
	cf, has := lt.getFile(e.Table())
	if !has {
		return
	}
	cf.Cache(NewEvent(lt.chainID, lt.nodeID, e.Table(), e))
}

// getFile gets a file for the given type. This method is purposely
// not thread-safe to avoid the overhead of locking with each event save.
func (lt *LocalTracer) getFile(table string) (*cachedFile, bool) {
	f, has := lt.fileMap[table]
	return f, has
}

// IsCollecting returns true if the table is being collected.
func (lt *LocalTracer) IsCollecting(table string) bool {
	_, has := lt.getFile(table)
	return has
}

// Stop optionally uploads and closes all open files.
func (lt *LocalTracer) Stop() {
	for _, file := range lt.fileMap {
		err := file.Close()
		if err != nil {
			lt.logger.Error("failed to close file", "error", err)
		}
	}
}

// splitAndTrimEmpty slices s into all subslices separated by sep and returns a
// slice of the string s with all leading and trailing Unicode code points
// contained in cutset removed. If sep is empty, SplitAndTrim splits after each
// UTF-8 sequence. First part is equivalent to strings.SplitN with a count of
// -1.  also filter out empty strings, only return non-empty strings.
//
// NOTE: this is copy pasted from the config package to avoid a circular
// dependency. See the function of the same name for tests.
func splitAndTrimEmpty(s, sep, cutset string) []string {
	if s == "" {
		return []string{}
	}

	spl := strings.Split(s, sep)
	nonEmptyStrings := make([]string, 0, len(spl))
	for i := 0; i < len(spl); i++ {
		element := strings.Trim(spl[i], cutset)
		if element != "" {
			nonEmptyStrings = append(nonEmptyStrings, element)
		}
	}
	return nonEmptyStrings
}
