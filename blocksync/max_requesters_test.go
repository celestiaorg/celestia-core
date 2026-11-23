package blocksync

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMaxRequesters(t *testing.T) {
	cases := map[string]int{
		"":      defaultMaxRequesters,
		"abc":   defaultMaxRequesters,
		"-5":    defaultMaxRequesters,
		"0":     defaultMaxRequesters,
		"1":     defaultMaxRequesters, // one requester can never apply a block
		"2":     2,
		"40":    40,
		"200":   200,
		"1000":  1000,
		"1001":  defaultMaxRequesters,
		"1e9":   defaultMaxRequesters,
		"99999": defaultMaxRequesters,
	}
	for in, want := range cases {
		require.Equal(t, want, parseMaxRequesters(in), "input %q", in)
	}
}
