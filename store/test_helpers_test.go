package store

import (
	"crypto/rand"
	"encoding/binary"

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

// generateRandomTx creates a transaction with random data of specified size
func generateRandomTx(size int) (types.Tx, error) {
	// Create a buffer with the transaction size
	tx := make([]byte, size)

	// Fill the buffer with random data
	_, err := rand.Read(tx)
	if err != nil {
		return nil, err
	}

	// Add a small header to make it look more like a real transaction
	// This includes a version number and timestamp
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], 1) // version
	binary.LittleEndian.PutUint32(header[4:8], uint32(cmttime.Now().Unix()))

	// Combine header and random data
	return append(header, tx...), nil
}

// createTestingBlock creates a test block with the given height and number of transactions
func createTestingBlock(t testHelper, state sm.State, height int64, numTxs int) (*types.Block, *types.PartSet, *types.ExtendedCommit) {
	txs := make([]types.Tx, numTxs)

	// Generate transactions with varying sizes between 1KB and 10KB
	for i := 0; i < numTxs; i++ {
		// Generate a random size between 1KB and 10KB
		size := 500 // 500 bytes
		tx, err := generateRandomTx(size)
		require.NoError(t, err)
		txs[i] = tx
	}

	block := state.MakeBlock(height, types.MakeData(txs), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())

	return block, partSet, seenCommit
}
