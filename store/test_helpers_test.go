package store

import (
	"fmt"

	"github.com/stretchr/testify/require"

	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

// testHelper is an interface that both testing.T and testing.B implement
// so helpers can be used in both tests and benchmarks.
type testHelper interface {
	Helper()
	Fatal(args ...interface{})
	Fatalf(format string, args ...interface{})
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	Log(args ...interface{})
	Logf(format string, args ...interface{})
	Fail()
	FailNow()
	Failed() bool
	Skip(args ...interface{})
	Skipf(format string, args ...interface{})
	Skipped() bool
}

// createTestingBlock creates a test block with the given height and number of transactions
func createTestingBlock(t testHelper, state sm.State, height int64, numTxs int) (*types.Block, *types.PartSet, *types.ExtendedCommit) {
	txs := make([]types.Tx, numTxs)
	for i := 0; i < numTxs; i++ {
		txs[i] = []byte(fmt.Sprintf("tx%d", i))
	}

	block := state.MakeBlock(height, types.MakeData(txs), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())

	return block, partSet, seenCommit
}
