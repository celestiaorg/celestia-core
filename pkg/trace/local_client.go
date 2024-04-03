package trace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
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
// file. The internals are purposefully not *explicity* thread safe to avoid the
// overhead of locking with each event save. Only pass events to the returned
// channel. Call CloseAll to close all open files.
type LocalClient struct {
	chainID, nodeID string
	logger          log.Logger
	cfg             *config.Config

	// fileMap maps tables to their open files files are threadsafe, but the map
	// is not. Therefore don't create new files after initialization to remain
	// threadsafe.
	fileMap map[string]*os.File
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
	fm := make(map[string]*os.File)
	path := path.Join(cfg.RootDir, "data", "traces")
	for _, table := range splitAndTrimEmpty(cfg.Instrumentation.TracingTables, ",", " ") {
		fileName := fmt.Sprintf("%s/%s.jsonl", path, table)
		err := os.MkdirAll(path, 0700)
		file, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open or create file %s: %w", fileName, err)
		}
		fm[table] = file
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
	file, has := lc.getFile(table)
	if !has {
		return nil, fmt.Errorf("table %s not found", table)
	}

	return file, nil
}

func (lc *LocalClient) IsCollecting(table string) bool {
	if _, has := lc.getFile(table); has {
		return true
	}
	return false
}

// getFile gets a file for the given type. This method is purposely
// not thread-safe to avoid the overhead of locking with each event save.
func (lc *LocalClient) getFile(table string) (*os.File, bool) {
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
	for table, file := range lc.fileMap {
		if lc.cfg.Instrumentation.TracePushURL != "" {
			err := UploadFile(lc.cfg.Instrumentation.TracePushURL, lc.chainID, lc.nodeID, table, file)
			if err != nil {
				lc.logger.Error("failed to upload trace file", "error", err)
			}
		}
		file.Close()
	}
}

// UploadFile uploads a file to the given URL. This function does not close the
// file.
func UploadFile(url, chainID, nodeID, table string, file *os.File) error {
	// Prepare a form that you will submit to that URL
	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	fileWriter, err := multipartWriter.CreateFormFile("file", file.Name())
	if err != nil {
		return err
	}

	if err := multipartWriter.WriteField("chain_id", chainID); err != nil {
		return err
	}

	if err := multipartWriter.WriteField("node_id", nodeID); err != nil {
		return err
	}

	if err := multipartWriter.WriteField("table", table); err != nil {
		return err
	}

	// Copy the file data to the multipart writer
	if _, err := io.Copy(fileWriter, file); err != nil {
		return err
	}
	multipartWriter.Close()

	// Create a new request to the given URL
	request, err := http.NewRequest("POST", url, &requestBody)
	if err != nil {
		return err
	}

	// Set the content type, this will contain the boundary.
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	// Do the request
	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Check the response
	if response.StatusCode != http.StatusOK {
		return io.ErrUnexpectedEOF
	}

	return nil
}
