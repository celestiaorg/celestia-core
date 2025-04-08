package types

import (
	"fmt"
	"math/rand"
	"runtime/debug"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/proto/tendermint/mempool"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

// generateTxs creates n transactions each with the given size.
func generateTxs(n int, size int) []types.Tx {
	txs := make([]types.Tx, 0, n)
	for i := 0; i < n; i++ {
		txs = append(txs, cmtrand.Bytes(size))
	}
	return txs
}

// StreamTxsCombinations streams all 2^n combinations as a slice of int (0 or 1).
func StreamTxsCombinations(n int) <-chan []int {
	out := make(chan []int)
	go func() {
		total := 1 << n // 2^n combinations
		for i := 0; i < total; i++ {
			bitArray := make([]int, n)
			for j := 0; j < n; j++ {
				bitArray[j] = (i >> j) & 1
			}
			out <- bitArray
		}
		close(out)
	}()
	return out
}

// TestTxsToParts_Correctness is a targeted table-driven test that verifies
// that for each given tx size the TxsToParts function returns the expected parts.
func TestTxsToParts_Correctness(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	numberOfTxs := 8 // reduced number for combinatorial explosion

	testCases := []struct {
		name string
		txs  []types.Tx
	}{
		{
			name: "txs size == types.BlockPartSizeBytes/3",
			txs:  generateTxs(numberOfTxs, int(types.BlockPartSizeBytes/3)),
		},
		{
			name: "txs size == types.BlockPartSizeBytes",
			txs:  generateTxs(numberOfTxs, int(types.BlockPartSizeBytes)),
		},
		{
			name: "txs size == types.BlockPartSizeBytes * 3",
			txs:  generateTxs(numberOfTxs, int(types.BlockPartSizeBytes)*3),
		},
		{
			name: "128MB block",
			txs:  generateTxs(64, int(types.BlockPartSizeBytes)*32),
		},
		{
			name: "very full mempool",
			txs:  generateTxs(140, 2000000),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := types.Data{Txs: tc.txs}
			block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

			// Precompute the expected UnmarshalledTx values.
			txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
			for i, pos := range partSet.TxPos {
				// Wrap the tx bytes in the mempool.Txs structure.
				protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
				marshalledTx, err := proto.Marshal(&protoTxs)
				require.NoError(t, err)

				txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
				require.NoError(t, err)

				txsFound[i] = UnmarshalledTx{
					MetaData: TxMetaData{
						Start: uint32(pos.Start),
						End:   uint32(pos.End),
						Hash:  block.Txs[i].Hash(),
					},
					Key:     txKey,
					TxBytes: marshalledTx,
				}
			}

			// For each possible combination of transactions, verify the parts.
			for combination := range StreamTxsCombinations(numberOfTxs) {
				t.Run(fmt.Sprintf("combination_%v", combination), func(t *testing.T) {
					var combinationTxs []UnmarshalledTx
					for index, bit := range combination {
						if bit == 1 {
							combinationTxs = append(combinationTxs, txsFound[index])
						}
					}

					lastPart := partSet.GetPart(int(partSet.Total() - 1))
					parts, err := TxsToParts(combinationTxs, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
					require.NoError(t, err)
					for _, part := range parts {
						expectedPart := partSet.GetPart(int(part.Index))
						require.Equal(t, expectedPart.Bytes, part.Bytes)
					}
				})
			}
		})
	}
}

// TestTxsToParts_EdgeCases adds additional tests for edge conditions.
func TestTxsToParts_EdgeCases(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() { cleanup(t) })

	t.Run("empty input", func(t *testing.T) {
		// No transactions provided.
		parts, err := TxsToParts([]UnmarshalledTx{}, 1, types.BlockPartSizeBytes, 1)
		require.NoError(t, err)
		require.Empty(t, parts)
	})

	t.Run("incomplete part", func(t *testing.T) {
		// Create a block where a single part is normally filled by two txs,
		// then provide only one transaction so that the part is incomplete.
		txs := generateTxs(2, int(types.BlockPartSizeBytes/2))
		data := types.Data{Txs: txs}
		block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			require.NoError(t, err)

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			require.NoError(t, err)

			txsFound[i] = UnmarshalledTx{
				MetaData: TxMetaData{
					Start: uint32(pos.Start),
					End:   uint32(pos.End),
					Hash:  block.Txs[i].Hash(),
				},
				Key:     txKey,
				TxBytes: marshalledTx,
			}
		}

		// Remove one transaction to simulate an incomplete part.
		incompleteTxsFound := txsFound[:1]
		lastPart := partSet.GetPart(int(partSet.Total() - 1))
		parts, err := TxsToParts(incompleteTxsFound, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)
		// Expect no complete part to be returned.
		require.Empty(t, parts)
	})

	t.Run("partial final part", func(t *testing.T) {
		// Create a block that normally would be divided into three parts.
		txs := generateTxs(3, int(types.BlockPartSizeBytes))
		data := types.Data{Txs: txs}
		block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			require.NoError(t, err)

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			require.NoError(t, err)

			txsFound[i] = UnmarshalledTx{
				MetaData: TxMetaData{
					Start: uint32(pos.Start),
					End:   uint32(pos.End),
					Hash:  block.Txs[i].Hash(),
				},
				Key:     txKey,
				TxBytes: marshalledTx,
			}
		}

		// Remove the last transaction to simulate that the final part is incomplete.
		incompleteTxsFound := txsFound[:len(txsFound)-1]
		lastPart := partSet.GetPart(int(partSet.Total() - 1))
		parts, err := TxsToParts(incompleteTxsFound, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)

		// Only the second part should be returned as the block has data not
		// included in the transactions
		require.Equal(t, 1, len(parts))
		for _, part := range parts {
			expectedPart := partSet.GetPart(int(part.Index))
			require.Equal(t, expectedPart.Bytes, part.Bytes)
		}
	})
}

// FuzzTxsToParts is a fuzz test that randomly selects a subset of transactions from a
// fixed block and then verifies that TxsToParts returns the expected parts.
// To run the fuzzer, use: go test -fuzz=FuzzTxsToParts
func FuzzTxsToParts(f *testing.F) {
	// Seed the fuzzer with an initial value.
	f.Add(uint16(0))

	f.Fuzz(func(t *testing.T, mask uint16) {
		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		numberOfTxs := 16
		// Using one tx size for fuzzing.
		txs := generateTxs(numberOfTxs, int(types.BlockPartSizeBytes/3))
		data := types.Data{Txs: txs}
		block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			if err != nil {
				t.Skip("Skipping due to proto.Marshal error")
			}

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			if err != nil {
				t.Skip("Skipping due to TxKeyFromBytes error")
			}

			txsFound[i] = UnmarshalledTx{
				MetaData: TxMetaData{
					Start: uint32(pos.Start),
					End:   uint32(pos.End),
					Hash:  block.Txs[i].Hash(),
				},
				Key:     txKey,
				TxBytes: marshalledTx,
			}
		}

		var subset []UnmarshalledTx
		for i := 0; i < numberOfTxs; i++ {
			if mask&(1<<i) != 0 {
				subset = append(subset, txsFound[i])
			}
		}

		lastPart := partSet.GetPart(int(partSet.Total() - 1))
		parts, err := TxsToParts(subset, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)
		for _, part := range parts {
			expectedPart := partSet.GetPart(int(part.Index))
			require.Equal(t, expectedPart.Bytes, part.Bytes)
		}
	})
}

func TestTxsToParts_Panic(t *testing.T) {
	type test struct {
		// originalTxs describes the number of and the size of each transaction
		originalTxs []int
		// providedTxs deterines which txs are actually given to the function
		providedTxs []int
	}
	tests := []test{
		{
			originalTxs: []int{100, 100, 100, 1000, 100000, 10000000},
			providedTxs: []int{4, 5},
		},
		{
			originalTxs: []int{1990000},
			providedTxs: []int{0},
		},
		{
			originalTxs: []int{1990000, 100, 1990000, 1990000, 1990000},
			providedTxs: []int{0},
		},
		{
			originalTxs: []int{1990000, 100, 1990000, 1990000, 1990000},
			providedTxs: []int{0},
		},
		{
			originalTxs: []int{1990000, 100, 1990000, 1990000, 1990000},
			providedTxs: []int{0, 1},
		},
		{
			originalTxs: []int{1990000, 100, 1990000, 1990000, 1990000},
			providedTxs: []int{0, 2, 3},
		},
	}
	for _, tt := range tests {
		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		txs := genHeterogeneousTxs(tt.originalTxs)

		data := types.Data{Txs: txs}
		block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			if err != nil {
				t.Skip("Skipping due to proto.Marshal error")
			}

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			if err != nil {
				t.Skip("Skipping due to TxKeyFromBytes error")
			}

			txsFound[i] = UnmarshalledTx{
				MetaData: TxMetaData{
					Start: pos.Start,
					End:   pos.End,
					Hash:  block.Txs[i].Hash(),
				},
				Key:     txKey,
				TxBytes: marshalledTx,
			}
		}

		lastPart := partSet.GetPart(int(partSet.Total() - 1))
		subset := filter(txsFound, tt.providedTxs)
		_, err := TxsToParts(subset, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)
	}
}

func FuzzTxsToParts_Panic(f *testing.F) {
	// Seed corpus: one value (e.g. 10 transactions)
	f.Add(uint(10))

	f.Fuzz(func(t *testing.T, numTx uint) {
		// Ensure at least one transaction.
		if numTx == 0 {
			numTx = 1
		}

		// Set an upper bound to avoid excessive allocations (e.g. no more than 1000 transactions).
		if numTx > 10 {
			numTx = 30
		}

		// Prepare a slice to hold sizes for each transaction.
		sizes := make([]int, numTx)
		// Define the maximum allowed size for a transaction.
		const maxTxSize = 3000000

		// Seed a pseudo-random number generator using the input (for reproducibility).
		r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(numTx)))

		// Generate a random size (up to 2,000,000) for each transaction.
		// If the random size exceeds maxTxSize, clamp it.
		for i := uint(0); i < numTx; i++ {
			size := r.Intn(maxTxSize)
			if size < 1 {
				size = 1
			}
			sizes[i] = size
		}

		// remove a random number of transactions
		keep := r.Intn(int(numTx))
		keepInd := make(map[int]bool)
		for i := 0; i < keep; i++ {
			k := r.Intn(int(numTx))
			keepInd[k] = true
		}
		kept := make([]int, 0, keep)
		for k := range keepInd {
			kept = append(kept, k)
		}

		fmt.Println("inputs", sizes, kept, f.Name())
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("inputs", sizes, kept)
				fmt.Printf("Recovered from panic: %v\n", r)
				fmt.Println(string(debug.Stack()))
				t.Fail()
			}
		}()

		// Setup the test environment.
		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		txs := genHeterogeneousTxs(sizes)

		dataObj := types.Data{Txs: txs}
		block, partSet := sm.MakeBlock(1, dataObj, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{dataObj.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			if err != nil {
				t.Skip("Skipping due to proto.Marshal error")
			}

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			if err != nil {
				t.Skip("Skipping due to TxKeyFromBytes error")
			}

			txsFound[i] = UnmarshalledTx{
				MetaData: TxMetaData{
					Start: uint32(pos.Start),
					End:   uint32(pos.End),
					Hash:  block.Txs[i].Hash(),
				},
				Key:     txKey,
				TxBytes: marshalledTx,
			}
		}

		lastPart := partSet.GetPart(int(partSet.Total() - 1))
		// Filter transactions using the provided indices.
		subset := filter(txsFound, kept)

		_, err := TxsToParts(subset, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)
	})
}

func genHeterogeneousTxs(sizes []int) []types.Tx {
	txs := make([]types.Tx, 0, len(sizes))
	for _, size := range sizes {
		txs = append(txs, cmtrand.Bytes(size))
	}
	return txs
}

func filter[T any](txs []T, keep []int) []T {
	kept := make([]T, 0, len(keep))
	for _, k := range keep {
		kept = append(kept, txs[k])
	}
	return kept
}
