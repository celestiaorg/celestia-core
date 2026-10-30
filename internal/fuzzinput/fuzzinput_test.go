package fuzzinput

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReader(t *testing.T) {
	r := New([]byte{1, 0x34, 0x12, 3, 'a', 'b', 'c', 'd', 7})
	require.True(t, r.Bool())
	require.Equal(t, uint16(0x1234), r.Uint16())
	require.Equal(t, []byte("abc"), r.Bytes(10))
	require.Equal(t, 2, r.Intn(7)) // 'd' is 100
	require.Equal(t, 1, r.Len())
}

func TestReaderExhausted(t *testing.T) {
	r := New([]byte{5, 'x'})
	require.Equal(t, []byte("x"), r.Bytes(10), "length is capped at the remaining input")
	require.Zero(t, r.Len())
	require.Zero(t, r.Byte())
	require.Zero(t, r.Uint64())
	require.False(t, r.Bool())
	require.Empty(t, r.Bytes(10))
	require.Zero(t, r.Intn(0))
}

func TestReaderBytesCap(t *testing.T) {
	r := New([]byte{9, 1, 2, 3, 4})
	require.Equal(t, []byte{1, 2}, r.Bytes(2))
	require.Equal(t, []byte{3, 4}, r.Rest())
}
