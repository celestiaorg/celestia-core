package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
)

// Event wraps some trace data with metadata that dictates the table and things
// like the chainID and nodeID.
type Event struct {
	ChainID   string      `json:"chain_id"`
	NodeID    string      `json:"node_id"`
	Table     string      `json:"table"`
	Timestamp time.Time   `json:"timestamp"`
	Msg       interface{} `json:"msg"`
}

// NewEvent creates a new Event with the given chainID, nodeID, table, and msg.
// It adds the current time as the timestamp.
func NewEvent(chainID, nodeID, table string, msg interface{}) Event {
	return Event{
		ChainID:   chainID,
		NodeID:    nodeID,
		Table:     table,
		Msg:       msg,
		Timestamp: time.Now(),
	}
}

// LocalClient saves all of the events passed to the retuen channel to files
// based on their "type" (a string field in the event). Each type gets its own
// file. The internals are purposefully not *explicity* thread safe to avoid the
// overhead of locking with each event save. Only pass events to the returned
// channel. Call CloseAll to close all open files.
type LocalClient struct {
	chainID, nodeID string
	logger          log.Logger

	mu sync.RWMutex // Protects access to the fileMap

	// fileMap maps tables to their open files files are threadsafe, but the map
	// is not. Therefore don't create new files after initialization to remain
	// threadsafe.
	fileMap map[string]*os.File
	// cannal is a channel for all events that are being written. It acts as an
	// extra buffer to avoid blocking the caller when writing to files.
	cannal chan Event
}

// NewLocalClient creates a struct that will save all of the events passed to
// the retuen channel to files based on their "table" (a string field in the
// event). Each type gets its own file. The internal are purposefully not thread
// safe to avoid the overhead of locking with each event save. Only pass events
// to the returned channel. Call CloseAll to close all open files. Goroutine to
// save events is started in this function.
func NewLocalClient(cfg *config.InstrumentationConfig, logger log.Logger, chainID, nodeID string) (*LocalClient, error) {
	fm := make(map[string]*os.File)
	for _, table := range splitAndTrimEmpty(cfg.TracingTables, ",", " ") {
		fileName := fmt.Sprintf("%s.jsonl", table)
		file, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open or create file %s: %w", fileName, err)
		}
		fm[table] = file
	}

	lc := &LocalClient{
		fileMap: fm,
		cannal:  make(chan Event, cfg.TraceBufferSize),
		chainID: chainID,
		nodeID:  nodeID,
		logger:  logger,
		mu:      sync.RWMutex{},
	}

	go lc.drainCannal()

	return lc, nil
}

func (fm *LocalClient) Write(e Entry) {
	if !fm.IsCollecting(e.Table()) {
		return
	}
	fm.cannal <- NewEvent(fm.chainID, fm.nodeID, e.Table(), e)
}

func (fm *LocalClient) ReadTable(table string) ([]byte, error) {
	return nil, errors.New("reading not supported using the Local tracing client")
}

func (fm *LocalClient) IsCollecting(table string) bool {
	if _, has := fm.getFile(table); has {
		return true
	}
	return false
}

// getFile gets or creates a file for the given type. This method is purposely
// not thread-safe to avoid the overhead of locking with each event save.
func (fm *LocalClient) getFile(table string) (*os.File, bool) {
	f, has := fm.fileMap[table]
	return f, has
}

// saveEventToFile marshals an Event into JSON and appends it to a file named after the event's Type.
func (fm *LocalClient) saveEventToFile(event Event) error {
	file, has := fm.getFile(event.Table)
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

// drainCannal takes a variadic number of channels of Event pointers and drains them into files.
func (fm *LocalClient) drainCannal() {
	// purposefully do not lock, and rely on the channel to provide sync
	// actions, to avoid overhead of locking with each event save.
	for ev := range fm.cannal {
		if err := fm.saveEventToFile(ev); err != nil {
			fm.logger.Error("failed to save event to file", "error", err)
		}
	}
}

// CloseAll closes all open files.
func (fm *LocalClient) Stop() {
	for _, file := range fm.fileMap {
		file.Close()
	}
}
