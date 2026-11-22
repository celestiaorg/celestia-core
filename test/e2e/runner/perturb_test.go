package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	e2e "github.com/cometbft/cometbft/test/e2e/pkg"
)

func TestNetemArgs(t *testing.T) {
	testCases := []struct {
		name   string
		tcArgs string
		want   string
	}{
		{
			name:   "latency",
			tcArgs: netemLatency,
			want:   "for dev in $(ls /sys/class/net); do [ \"$dev\" = lo ] || tc qdisc replace dev \"$dev\" root netem delay 200ms 50ms distribution normal || exit 1; done",
		},
		{
			name:   "partition",
			tcArgs: netemPartition,
			want:   "for dev in $(ls /sys/class/net); do [ \"$dev\" = lo ] || tc qdisc replace dev \"$dev\" root netem loss 100% || exit 1; done",
		},
		{
			name:   "heal",
			tcArgs: netemHeal,
			want:   "for dev in $(ls /sys/class/net); do [ \"$dev\" = lo ] || tc qdisc del dev \"$dev\" root || exit 1; done",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, []string{
				"run", "--rm",
				"--network", "container:validator01",
				"--cap-add", "NET_ADMIN",
				"--entrypoint", "sh",
				e2e.LocalVersion,
				"-c", tc.want,
			}, netemArgs("validator01", tc.tcArgs))
		})
	}
}
