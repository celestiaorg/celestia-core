package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
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

// LocalClient saves all of the events passed to the retuen channel to files
// based on their "type" (a string field in the event). Each type gets its own
// file. The internals are purposefully not *explicitly* thread safe to avoid the
// overhead of locking with each event save. Only pass events to the returned
// channel. Call CloseAll to close all open files.
type LocalClient struct {
	chainID, nodeID string
	logger          log.Logger
	cfg             *config.Config

	// fileMap maps tables to their open files files are threadsafe, but the map
	// is not. Therefore don't create new files after initialization to remain
	// threadsafe.
	fileMap map[string]*bufferedFile
	// canal is a channel for all events that are being written. It acts as an
	// extra buffer to avoid blocking the caller when writing to files.
	canal chan Event[Entry]
}

// NewLocalClient creates a struct that will save all of the events passed to
// the retuen channel to files based on their "table" (a string field in the
// event). Each type gets its own file. The internal are purposefully not thread
// safe to avoid the overhead of locking with each event save. Only pass events
// to the returned channel. Call CloseAll to close all open files. Goroutine to
// save events is started in this function.
func NewLocalClient(cfg *config.Config, logger log.Logger, chainID, nodeID string) (*LocalClient, error) {
	fm := make(map[string]*bufferedFile)
	path := path.Join(cfg.RootDir, "data", "traces")
	for _, table := range splitAndTrimEmpty(cfg.Instrumentation.TracingTables, ",", " ") {
		fileName := fmt.Sprintf("%s/%s.jsonl", path, table)
		err := os.MkdirAll(path, 0700)
		if err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", path, err)
		}
		file, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open or create file %s: %w", fileName, err)
		}
		bf := newbufferedFile(file)
		fm[table] = bf
	}

	lc := &LocalClient{
		fileMap: fm,
		cfg:     cfg,
		canal:   make(chan Event[Entry], cfg.Instrumentation.TraceBufferSize),
		chainID: chainID,
		nodeID:  nodeID,
		logger:  logger,
	}

	go lc.draincanal()
	if cfg.Instrumentation.TracePushURL != "" {
		go lc.servePullData()
	}

	return lc, nil
}

func (lc *LocalClient) Write(e Entry) {
	if !lc.IsCollecting(e.Table()) {
		return
	}
	lc.canal <- NewEvent(lc.chainID, lc.nodeID, e.Table(), e)
}

// ReadTable returns a file for the given table. If the table is not being
// collected, an error is returned. This method is not thread-safe.
func (lc *LocalClient) ReadTable(table string) (*os.File, error) {
	bf, has := lc.getFile(table)
	if !has {
		return nil, fmt.Errorf("table %s not found", table)
	}

	return bf.File()
}

func (lc *LocalClient) IsCollecting(table string) bool {
	if _, has := lc.getFile(table); has {
		return true
	}
	return false
}

// getFile gets a file for the given type. This method is purposely
// not thread-safe to avoid the overhead of locking with each event save.
func (lc *LocalClient) getFile(table string) (*bufferedFile, bool) {
	f, has := lc.fileMap[table]
	return f, has
}

// saveEventToFile marshals an Event into JSON and appends it to a file named after the event's Type.
func (lc *LocalClient) saveEventToFile(event Event[Entry]) error {
	file, has := lc.getFile(event.Table)
	if !has {
		return fmt.Errorf("table %s not found", event.Table)
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %v", err)
	}

	if _, err := file.Write(append(eventJSON, '\n')); err != nil {
		return fmt.Errorf("failed to write event to file: %v", err)
	}

	return nil
}

// draincanal takes a variadic number of channels of Event pointers and drains them into files.
func (lc *LocalClient) draincanal() {
	// purposefully do not lock, and rely on the channel to provide sync
	// actions, to avoid overhead of locking with each event save.
	for ev := range lc.canal {
		if err := lc.saveEventToFile(ev); err != nil {
			lc.logger.Error("failed to save event to file", "error", err)
		}
	}
}

// Stop optionally uploads and closes all open files.
func (lc *LocalClient) Stop() {
	for _, file := range lc.fileMap {
		err := file.Flush()
		if err != nil {
			lc.logger.Error("failed to flush file", "error", err)
		}
		err = file.Close()
		if err != nil {
			lc.logger.Error("failed to close file", "error", err)
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
