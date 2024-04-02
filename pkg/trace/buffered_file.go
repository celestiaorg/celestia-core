package trace

import (
	"bufio"
	"os"
	"sync"
)

type bufferedFile struct {
	mut *sync.RWMutex
	// file is the file that is being written to.
	file *os.File
	// writer is the buffered writer that is writing to the file.
	wr *bufio.Writer
}

// newbufferedFile creates a new buffered file that writes to the given file.
func newbufferedFile(file *os.File) *bufferedFile {
	return &bufferedFile{
		mut:  &sync.RWMutex{},
		file: file,
		wr:   bufio.NewWriter(file),
	}
}

// Write writes the given bytes to the file.
func (f *bufferedFile) Write(b []byte) (int, error) {
	f.mut.Lock()
	defer f.mut.Unlock()
	return f.wr.Write(b)
}

// Flush flushes the writer to the file.
func (f *bufferedFile) Flush() error {
	f.mut.Lock()
	defer f.mut.Unlock()
	return f.wr.Flush()
}

// File returns a new copy of *os.File.
func (f *bufferedFile) File() (*os.File, error) {
	err := f.Flush()
	if err != nil {
		return nil, err
	}
	f.mut.RLock()
	defer f.mut.RUnlock()
	return os.Open(f.file.Name())
}

// Close closes the file.
func (f *bufferedFile) Close() error {
	f.mut.Lock()
	defer f.mut.Unlock()
	return f.file.Close()
}
