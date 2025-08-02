package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/cometbft/cometbft/libs/log"
)

// cachedFile wraps the os.File with a channel based cache that ensures only
// complete data is written to the file. Data is serialized to JSON before being
// written. WARNING: Errors are only logged and if the cache is filled writes are ignored!
type cachedFile struct {
	wg     *sync.WaitGroup
	cache  chan Event[Entry]
	file   *os.File
	logger log.Logger
}

// newCachedFile creates a cachedFile which wraps a normal file to ensure that
// only complete data is ever written. cacheSize is the number of events that
// will be cached. Files must be opened using os.O_SYNC in
// order for rows of data to be written atomically.
func newCachedFile(file *os.File, logger log.Logger, cacheSize int) *cachedFile {
	cf := &cachedFile{
		file:   file,
		cache:  make(chan Event[Entry], cacheSize),
		logger: logger,
		wg:     &sync.WaitGroup{},
	}
	cf.wg.Add(1)
	go cf.startFlushing()
	return cf
}

// Cache caches the given bytes to be written to the file.
func (f *cachedFile) Cache(b Event[Entry]) {
	select {
	case f.cache <- b:
	default:
		f.logger.Error(fmt.Sprintf("tracing cache full, dropping event: %T", b))
	}
}

// startFlushing reads from the cache, serializes the event, and writes to the
// file.
func (f *cachedFile) startFlushing() {
	defer f.wg.Done()

	for {
		b, ok := <-f.cache
		if !ok {
			// Channel closed, exit
			return
		}

		bz, err := json.Marshal(b)
		if err != nil {
			f.logger.Error("failed to marshal event", "err", err)
			close(f.cache)
			return
		}

		// format the file to jsonl
		bz = append(bz, '\n')

		_, err = f.file.Write(bz)
		if err != nil {
			f.logger.Error("tracer failed to write event to file", "error", err)
		}
	}
}

// Close closes the file.
func (f *cachedFile) Close() error {
	// set reading to true to prevent writes while closing the file.
	close(f.cache)
	f.wg.Wait()
	err := f.file.Sync()
	if err != nil {
		return err
	}
	return f.file.Close()
}
