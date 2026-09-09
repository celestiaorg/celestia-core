package trace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchesTraceFile(t *testing.T) {
	const prefix = "chain/node/"

	testCases := []struct {
		name      string
		key       string
		fileNames []string
		want      bool
	}{
		{"no names matches any key", prefix + "consensus.jsonl", nil, true},
		{"no names matches a second key", prefix + "mempool.jsonl", nil, true},
		{"named file matches", prefix + "consensus.jsonl", []string{"consensus"}, true},
		{"other named file matches", prefix + "mempool.jsonl", []string{"consensus", "mempool"}, true},
		{"unrequested file does not match", prefix + "mempool.jsonl", []string{"consensus"}, false},
		{"suffix must include the extension", prefix + "consensus.json", []string{"consensus"}, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchesTraceFile(tc.key, tc.fileNames))
		})
	}
}
