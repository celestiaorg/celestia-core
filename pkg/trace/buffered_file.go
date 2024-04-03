package trace

import (
	"bufio"
	"fmt"
	"os"
)

type bufferedFile struct {
	// file is the file that is being written to.
	file *os.File
	// writer is the buffered writer that is writing to the file.
	wr *bufio.Writer
}

// newbufferedFile creates a new buffered file that writes to the given file.
func newbufferedFile(file *os.File) *bufferedFile {
	return &bufferedFile{
		file: file,
		wr:   bufio.NewWriter(file),
	}
}

// Write writes the given bytes to the file.
func (f *bufferedFile) Write(b []byte) (int, error) {
	fmt.Println("writing to file", string(b))
	return f.wr.Write(b)
}

// Flush flushes the writer to the file.
func (f *bufferedFile) Flush() error {
	return f.wr.Flush()
}

// File returns a new copy of *os.File.
func (f *bufferedFile) File() (*os.File, error) {
	return os.Open(f.file.Name())
}

// Close closes the file.
func (f *bufferedFile) Close() error {
	return f.file.Close()
}
