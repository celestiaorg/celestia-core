package prove

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/pkg/consts"
	"github.com/tendermint/tendermint/pkg/da"
	"github.com/tendermint/tendermint/types"
)

func TestTxInclusion(t *testing.T) {
	txCount := 100
	typicalBlockData := types.Data{
		Txs:      generateRandomlySizedContiguousShares(txCount, 200),
		Messages: generateRandomlySizedMessages(10, 150),
	}

	// compute the data availability header
	shares, _ := typicalBlockData.ComputeShares()

	squareSize := uint(math.Sqrt(float64(len(shares))))

	for i := 0; i < txCount; i++ {
		txProof, err := TxInclusion(consts.DefaultCodec(), typicalBlockData, squareSize, uint(i))
		require.NoError(t, err)
		assert.True(t, txProof.VerifyProof())
	}
}

func TestTxSharePosition(t *testing.T) {
	type test struct {
		name string
		txs  types.Txs
	}

	tests := []test{
		{
			name: "typical",
			txs:  generateRandomlySizedContiguousShares(44, 200),
		},
		{
			name: "many small tx",
			txs:  generateRandomlySizedContiguousShares(444, 100),
		},
		{
			name: "one small tx",
			txs:  generateRandomlySizedContiguousShares(1, 200),
		},
		{
			name: "one large tx",
			txs:  generateRandomlySizedContiguousShares(1, 2000),
		},
		{
			name: "many large txs",
			txs:  generateRandomlySizedContiguousShares(100, 2000),
		},
	}

	type startEndPoints struct {
		start, end uint
	}

	for _, tt := range tests {
		positions := make([]startEndPoints, len(tt.txs))
		for i := 0; i < len(tt.txs); i++ {
			start, end, err := txSharePosition(tt.txs, uint(i))
			require.NoError(t, err)
			positions[i] = startEndPoints{start: start, end: end}
		}

		shares := tt.txs.SplitIntoShares().RawShares()

		for i, pos := range positions {
			if pos.start == pos.end {
				assert.Contains(t, string(shares[pos.start]), string(tt.txs[i]), tt.name, i, pos)
			} else {
				assert.Contains(
					t,
					joinByteSlices(shares[pos.start:pos.end+1]...),
					string(tt.txs[i]),
					tt.name,
					pos,
					len(tt.txs[i]),
				)
			}
		}
	}
}

func Test_genRowShares(t *testing.T) {
	typicalBlockData := types.Data{
		Txs:      generateRandomlySizedContiguousShares(120, 200),
		Messages: generateRandomlySizedMessages(10, 1000),
	}

	allShares, _ := typicalBlockData.ComputeShares()
	rawShares := allShares.RawShares()

	originalSquareSize := uint(math.Sqrt(float64(len(rawShares))))

	eds, err := da.ExtendShares(uint64(originalSquareSize), rawShares)
	require.NoError(t, err)

	eds.ColRoots()

	rowShares, err := genRowShares(
		consts.DefaultCodec(),
		typicalBlockData,
		originalSquareSize,
		0,
		originalSquareSize-1,
	)
	require.NoError(t, err)

	for i := uint(0); i < uint(originalSquareSize); i++ {
		row := eds.Row(i)
		assert.Equal(t, row, rowShares[i], fmt.Sprintf("row %d", i))
		// also test fetching individual rows
		secondSet, err := genRowShares(consts.DefaultCodec(), typicalBlockData, originalSquareSize, i, i)
		require.NoError(t, err)
		assert.Equal(t, row, secondSet[0], fmt.Sprintf("row %d", i))
	}
}

func Test_genOrigRowShares(t *testing.T) {
	txCount := 100
	typicalBlockData := types.Data{
		Txs:      generateRandomlySizedContiguousShares(txCount, 200),
		Messages: generateRandomlySizedMessages(10, 1500),
	}

	allShares, _ := typicalBlockData.ComputeShares()
	rawShares := allShares.RawShares()

	genShares := genOrigRowShares(typicalBlockData, 8, 0, 7)

	require.Equal(t, len(allShares), len(genShares))
	assert.Equal(t, rawShares, genShares)
}

func joinByteSlices(s ...[]byte) string {
	out := make([]string, len(s))
	for i, sl := range s {
		sl, _, _ := types.ParseDelimiter(sl)
		out[i] = string(sl[consts.NamespaceSize:])
	}
	return strings.Join(out, "")
}

func generateRandomlySizedContiguousShares(count, max int) types.Txs {
	txs := make(types.Txs, count)
	for i := 0; i < count; i++ {
		size := rand.Intn(max)
		if size == 0 {
			size = 1
		}
		txs[i] = generateRandomContiguousShares(1, size)[0]
	}
	return txs
}

func generateRandomContiguousShares(count, size int) types.Txs {
	txs := make(types.Txs, count)
	for i := 0; i < count; i++ {
		tx := make([]byte, size)
		_, err := rand.Read(tx)
		if err != nil {
			panic(err)
		}
		txs[i] = tx
	}
	return txs
}

func generateRandomlySizedMessages(count, maxMsgSize int) types.Messages {
	msgs := make([]types.Message, count)
	for i := 0; i < count; i++ {
		msgs[i] = generateRandomMessage(rand.Intn(maxMsgSize))
	}

	// this is just to let us use assert.Equal
	if count == 0 {
		msgs = nil
	}

	return types.Messages{MessagesList: msgs}
}

func generateRandomMessage(size int) types.Message {
	share := generateRandomNamespacedShares(1, size)[0]
	msg := types.Message{
		NamespaceID: share.NamespaceID(),
		Data:        share.Data(),
	}
	return msg
}

func generateRandomNamespacedShares(count, msgSize int) types.NamespacedShares {
	shares := generateRandNamespacedRawData(uint32(count), consts.NamespaceSize, uint32(msgSize))
	msgs := make([]types.Message, count)
	for i, s := range shares {
		msgs[i] = types.Message{
			Data:        s[consts.NamespaceSize:],
			NamespaceID: s[:consts.NamespaceSize],
		}
	}
	return types.Messages{MessagesList: msgs}.SplitIntoShares()
}

func generateRandNamespacedRawData(total, nidSize, leafSize uint32) [][]byte {
	data := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		nid := make([]byte, nidSize)
		rand.Read(nid)
		data[i] = nid
	}
	sortByteArrays(data)
	for i := uint32(0); i < total; i++ {
		d := make([]byte, leafSize)
		rand.Read(d)
		data[i] = append(data[i], d...)
	}

	return data
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}
