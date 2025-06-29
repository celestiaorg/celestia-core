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
// written. The cache is flushed when the chunk size is reached. WARNING: Errors
// are only logged and if the cache is filled writes are ignored!
type cachedFile struct {
	wg        *sync.WaitGroup
	cache     chan Event[Entry]
	file      *os.File
	chunkSize int
	logger    log.Logger
}

// newCachedFile creates a cachedFile which wraps a normal file to ensure that
// only complete data is ever written. cacheSize is the number of events that
// will be cached and chunkSize is the number of events that will trigger a
// write. cacheSize needs to be sufficiently larger (10x to be safe) than
// chunkSize in order to avoid blocking. Files must be opened using os.O_SYNC in
// order for rows of data to be written atomically.
func newCachedFile(file *os.File, logger log.Logger, cacheSize int, chunkSize int) *cachedFile {
	cf := &cachedFile{
		file:      file,
		cache:     make(chan Event[Entry], cacheSize),
		chunkSize: chunkSize,
		logger:    logger,
		wg:        &sync.WaitGroup{},
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
	buffer := make([][]byte, 0, f.chunkSize)
	total := 0
	defer f.wg.Done()

	for {
		b, ok := <-f.cache
		if !ok {
			// Channel closed, flush remaining data and exit
			if len(buffer) > 0 {
				_, err := f.flush(total, buffer)
				if err != nil {
					f.logger.Error("failure to flush remaining events", "error", err)
				}
			}
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
		total += len(bz)

		buffer = append(buffer, bz)
		if len(buffer) >= f.chunkSize {
			_, err := f.flush(total, buffer)
			if err != nil {
				f.logger.Error("tracer failed to write buffered files to file", "error", err)
			}
			buffer = buffer[:0] // reset buffer
			total = 0
		}
	}
}

// flush writes the given bytes to the file. This method requires that the file
// be opened with os.O_SYNC in order to write atomically to the file.
func (f *cachedFile) flush(total int, buffer [][]byte) (int, error) {
	bz := make([]byte, 0, total)
	for _, b := range buffer {
		bz = append(bz, b...)
	}
	return f.file.Write(bz)
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
