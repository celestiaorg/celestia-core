package mempool

import (
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
)

func TestPostCheckMaxGas(t *testing.T) {
	testCases := []struct {
		name      string
		maxGas    int64
		gasWanted int64
		wantErr   bool
	}{
		{"within limit", 10, 5, false},
		{"at limit", 10, 10, false},
		{"above limit", 10, 11, true},
		{"negative with limit", 10, -1, true},
		{"unlimited", -1, 1_000_000, false},
		{"negative with unlimited", -1, -2, true},
		{"zero with unlimited", -1, 0, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := PostCheckMaxGas(tc.maxGas)(nil, &abci.ResponseCheckTx{GasWanted: tc.gasWanted})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
