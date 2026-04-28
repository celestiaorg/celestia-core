package mempool

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTxCodeToStr(t *testing.T) {
	tests := []struct {
		name string
		code uint32
		want string
	}{
		{"zero", 0, "0"},
		{"one", 1, "1"},
		{"large", 4294967295, "4294967295"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, TxCodeToStr(tc.code))
		})
	}
}

func TestTxCodeToStrOrLabel(t *testing.T) {
	const label = "postcheck"
	tests := []struct {
		name   string
		code   uint32
		hasErr bool
		want   string
	}{
		{"zero with err uses label", 0, true, label},
		{"zero without err formats code", 0, false, "0"},
		{"non-zero with err keeps code", 5, true, "5"},
		{"non-zero without err keeps code", 5, false, "5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, TxCodeToStrOrLabel(tc.code, tc.hasErr, label))
		})
	}
}
