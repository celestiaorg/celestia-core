// Package fuzzinput turns a fuzzer's byte slice into typed values, so a fuzz
// target can build structured inputs (messages, field edits, operation
// sequences) instead of feeding raw bytes to a decoder.
//
// Reads never fail: once the input is exhausted every read returns the zero
// value, which keeps targets deterministic and lets the fuzzer shrink inputs.
package fuzzinput

import "encoding/binary"

// Reader consumes typed values from the front of a fuzz input.
type Reader struct {
	data []byte
}

// New returns a Reader over data. The slice is not copied or modified.
func New(data []byte) *Reader {
	return &Reader{data: data}
}

// Len reports how many unread bytes remain.
func (r *Reader) Len() int {
	return len(r.data)
}

// Byte reads one byte.
func (r *Reader) Byte() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[0]
	r.data = r.data[1:]
	return b
}

// Bool reads one byte and reports whether its low bit is set.
func (r *Reader) Bool() bool {
	return r.Byte()&1 == 1
}

// Uint16 reads a little-endian uint16.
func (r *Reader) Uint16() uint16 {
	var buf [2]byte
	r.fill(buf[:])
	return binary.LittleEndian.Uint16(buf[:])
}

// Uint32 reads a little-endian uint32.
func (r *Reader) Uint32() uint32 {
	var buf [4]byte
	r.fill(buf[:])
	return binary.LittleEndian.Uint32(buf[:])
}

// Uint64 reads a little-endian uint64.
func (r *Reader) Uint64() uint64 {
	var buf [8]byte
	r.fill(buf[:])
	return binary.LittleEndian.Uint64(buf[:])
}

// Int32 reads a little-endian int32.
func (r *Reader) Int32() int32 {
	return int32(r.Uint32()) //nolint:gosec // reinterpreting fuzz bytes
}

// Int64 reads a little-endian int64.
func (r *Reader) Int64() int64 {
	return int64(r.Uint64()) //nolint:gosec // reinterpreting fuzz bytes
}

// Intn reads one byte and returns it modulo n. It returns 0 if n <= 0.
func (r *Reader) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.Byte()) % n
}

// Bytes reads a one-byte length, capped at limit, followed by that many bytes.
// A negative limit is treated as zero. The result is a copy, so callers may
// modify it.
func (r *Reader) Bytes(limit int) []byte {
	n := int(r.Byte())
	if n > limit {
		n = max(limit, 0)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	out := append([]byte(nil), r.data[:n]...)
	r.data = r.data[n:]
	return out
}

// Rest returns a copy of all unread bytes and consumes them.
func (r *Reader) Rest() []byte {
	out := append([]byte(nil), r.data...)
	r.data = nil
	return out
}

func (r *Reader) fill(buf []byte) {
	n := copy(buf, r.data)
	r.data = r.data[n:]
}
