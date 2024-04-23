package trace

import (
	"bufio"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// bufferedFile is a file that is being written to and read from. It is thread
// safe, however, when reading from the file, writes will be ignored.
type bufferedFile struct {
	// reading protects the file from being written to while it is being read
	// from. This is needed beyond in addition to the mutex so that writes can
	// be ignored while reading.
	reading atomic.Bool

	// mut protects the buffered writer.
	mut *sync.Mutex

	// file is the file that is being written to.
	file *os.File

	// writer is the buffered writer that is writing to the file.
	wr *bufio.Writer
}

// newbufferedFile creates a new buffered file that writes to the given file.
func newbufferedFile(file *os.File) *bufferedFile {
	return &bufferedFile{
		file:    file,
		wr:      bufio.NewWriter(file),
		reading: atomic.Bool{},
		mut:     &sync.Mutex{},
	}
}

// Write writes the given bytes to the file. If the file is currently being read
// from, the write will be lost.
func (f *bufferedFile) Write(b []byte) (int, error) {
	if f.reading.Load() {
		return 0, nil
	}
	f.mut.Lock()
	defer f.mut.Unlock()
	return f.wr.Write(b)
}

func (f *bufferedFile) startReading() error {
	f.reading.Store(true)
	f.mut.Lock()
	defer f.mut.Unlock()

	err := f.wr.Flush()
	if err != nil {
		return err
	}

	_, err = f.file.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}

	return nil
}

func (f *bufferedFile) stopReading() error {
	defer f.reading.Store(false)
	_, err := f.file.Seek(0, io.SeekEnd)
	return err
}

// File returns the underlying file with the seek point reset. The caller should
// not close the file. The caller must call the returned function when they are
// done reading from the file. This function resets the seek point to where it
// was being written to.
func (f *bufferedFile) File() (*os.File, func() error, error) {
	err := f.startReading()
	if err != nil {
		return nil, func() error { return nil }, err
	}
	err = f.wr.Flush()
	if err != nil {
		return nil, func() error { return nil }, err
	}

	return f.file, f.stopReading, nil
}

// Close closes the file.
func (f *bufferedFile) Close() error {
	// set reading to true to prevent writes while closing the file.
	f.reading.Store(true)
	return f.file.Close()
}
