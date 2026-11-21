package types

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"

	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
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
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
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
			commit := state.MakeTestCommit(t, sm, pv, time.Now())
			block, partSet, err := sm.MakeBlock(1, data, commit, []types.Evidence{}, cmtrand.Bytes(20))
			require.NoError(t, err)

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
						Start: pos.Start,
						End:   pos.End,
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
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
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
		commit := state.MakeTestCommit(t, sm, pv, time.Now())
		block, partSet, err := sm.MakeBlock(1, data, commit, []types.Evidence{}, cmtrand.Bytes(20))
		require.NoError(t, err)

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			require.NoError(t, err)

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			require.NoError(t, err)

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
		commit := state.MakeTestCommit(t, sm, pv, time.Now())
		block, partSet, err := sm.MakeBlock(1, data, commit, []types.Evidence{}, cmtrand.Bytes(20))
		require.NoError(t, err)

		txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
		for i, pos := range partSet.TxPos {
			protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
			marshalledTx, err := proto.Marshal(&protoTxs)
			require.NoError(t, err)

			txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
			require.NoError(t, err)

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

// deterministicBlockTxs builds a block from txs of the given sizes, filled
// from seed, and returns its part set together with the UnmarshalledTx view
// of every tx. It uses no wall-clock or global randomness so fuzz inputs
// replay identically.
func deterministicBlockTxs(t testing.TB, seed int64, sizes []int) (*types.PartSet, []UnmarshalledTx) {
	r := rand.New(rand.NewSource(seed)) //nolint:gosec
	txs := make([]types.Tx, len(sizes))
	for i, size := range sizes {
		txs[i] = make([]byte, size)
		_, _ = r.Read(txs[i])
	}
	block := types.MakeBlock(1, types.Data{Txs: txs}, nil, nil)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		marshalledTx, err := proto.Marshal(&mempool.Txs{Txs: [][]byte{txs[i]}})
		require.NoError(t, err)
		txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
		require.NoError(t, err)
		txsFound[i] = UnmarshalledTx{
			MetaData: TxMetaData{Start: pos.Start, End: pos.End, Hash: block.Txs[i].Hash()},
			Key:      txKey,
			TxBytes:  marshalledTx,
		}
	}
	return partSet, txsFound
}

// checkTxsToParts runs TxsToParts on subset, with each tx's encoding passed
// whole and split into its prefix and tx bytes, and requires it to return
// exactly the block's parts that subset fully covers.
func checkTxsToParts(t *testing.T, partSet *types.PartSet, subset []UnmarshalledTx) {
	lastPart := partSet.GetPart(int(partSet.Total() - 1))
	want := coveredParts(subset, partSet.Total(), uint32(len(lastPart.Bytes)))

	split := make([]UnmarshalledTx, len(subset))
	for i, tx := range subset {
		_, n := binary.Uvarint(tx.TxBytes[1:])
		split[i] = tx
		split[i].Prefix, split[i].TxBytes = tx.TxBytes[:1+n], tx.TxBytes[1+n:]
	}

	for _, txs := range [][]UnmarshalledTx{subset, split} {
		parts, err := TxsToParts(txs, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)
		got := make([]uint32, 0, len(parts))
		for _, part := range parts {
			got = append(got, part.Index)
			require.Equal(t, partSet.GetPart(int(part.Index)).Bytes, part.Bytes)
		}
		require.Equal(t, want, got)
	}
}

// coveredParts returns, in order, the indexes of the parts whose bytes all
// lie within the txs.
func coveredParts(txs []UnmarshalledTx, total, lastLen uint32) []uint32 {
	in := make([]bool, (total-1)*types.BlockPartSizeBytes+lastLen)
	for _, tx := range txs {
		for j := tx.MetaData.Start; j < tx.MetaData.End; j++ {
			in[j] = true
		}
	}
	covered := make([]uint32, 0, total)
	for i := range total {
		start := i * types.BlockPartSizeBytes
		end := min(start+types.BlockPartSizeBytes, uint32(len(in)))
		if !slices.Contains(in[start:end], false) {
			covered = append(covered, i)
		}
	}
	return covered
}

// TestTxsToPartsPrefixAcrossParts covers parts that start inside a tx's prefix.
func TestTxsToPartsPrefixAcrossParts(t *testing.T) {
	// the third tx's 4-byte prefix starts 2 and 1 bytes before a part boundary.
	for _, sizes := range [][]int{{65230, 200, 70000}, {65231, 200, 70000}} {
		partSet, txs := deterministicBlockTxs(t, 1, sizes)
		checkTxsToParts(t, partSet, txs)
	}
}

// FuzzTxsToParts selects a subset of the transactions of a fixed block and
// verifies that TxsToParts returns exactly the block's parts.
// To run the fuzzer, use: go test -fuzz=FuzzTxsToParts
func FuzzTxsToParts(f *testing.F) {
	sizes := make([]int, 16)
	for i := range sizes {
		sizes[i] = int(types.BlockPartSizeBytes / 3)
	}
	partSet, txsFound := deterministicBlockTxs(f, 1, sizes)

	f.Add(uint16(0))
	f.Add(uint16(0xffff))
	f.Fuzz(func(t *testing.T, mask uint16) {
		var subset []UnmarshalledTx
		for i := range txsFound {
			if mask&(1<<i) != 0 {
				subset = append(subset, txsFound[i])
			}
		}
		checkTxsToParts(t, partSet, subset)
	})
}

// FuzzTxsToPartsSizes is FuzzTxsToParts over blocks of fuzzed tx sizes,
// including empty txs and ones spanning several parts.
func FuzzTxsToPartsSizes(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3}, uint64(0b1011))
	f.Add([]byte{255, 0, 128, 7, 64}, uint64(0b11110))
	f.Fuzz(func(t *testing.T, rawSizes []byte, mask uint64) {
		if len(rawSizes) == 0 || len(rawSizes) > 64 {
			return
		}
		sizes := make([]int, len(rawSizes))
		for i, b := range rawSizes {
			// up to ~4 parts, with sizes either side of the 1- and 2-byte varint boundaries.
			sizes[i] = int(b) * int(b) * 4
		}
		partSet, txsFound := deterministicBlockTxs(t, int64(len(rawSizes)), sizes)
		var subset []UnmarshalledTx
		for i := range txsFound {
			if mask&(1<<i) != 0 {
				subset = append(subset, txsFound[i])
			}
		}
		checkTxsToParts(t, partSet, subset)
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
		cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
		t.Cleanup(func() {
			cleanup(t)
		})

		txs := genHeterogeneousTxs(tt.originalTxs)

		data := types.Data{Txs: txs}
		commit := state.MakeTestCommit(t, sm, pv, time.Now())
		block, partSet, err := sm.MakeBlock(1, data, commit, []types.Evidence{}, cmtrand.Bytes(20))
		require.NoError(t, err)

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
		_, err = TxsToParts(subset, partSet.Total(), types.BlockPartSizeBytes, uint32(len(lastPart.Bytes)))
		require.NoError(t, err)
	}
}

// FuzzTxsToParts_Panic builds a block from up to 30 transactions of random
// sizes, derived only from the fuzz input, and checks TxsToParts on the
// subset of transactions selected by keep.
func FuzzTxsToParts_Panic(f *testing.F) {
	f.Add(int64(10), uint8(10), uint32(0x3))
	f.Add(int64(1), uint8(5), uint32(0x1))

	f.Fuzz(func(t *testing.T, seed int64, numTx uint8, keep uint32) {
		// Bound the block so an input stays fast: up to 30 transactions of
		// up to three parts each, enough for transactions to span parts.
		n := 1 + int(numTx)%30
		const maxTxSize = 3*int(types.BlockPartSizeBytes) + 1
		r := rand.New(rand.NewSource(seed)) //nolint:gosec
		sizes := make([]int, n)
		for i := range sizes {
			sizes[i] = 1 + r.Intn(maxTxSize)
		}
		partSet, txsFound := deterministicBlockTxs(t, seed, sizes)

		var subset []UnmarshalledTx
		for i := range txsFound {
			if keep&(1<<i) != 0 {
				subset = append(subset, txsFound[i])
			}
		}
		checkTxsToParts(t, partSet, subset)
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
