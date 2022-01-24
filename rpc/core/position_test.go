package core

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/pkg/consts"
	"github.com/tendermint/tendermint/pkg/da"
	"github.com/tendermint/tendermint/types"
)

func TestProveTxInclusion(t *testing.T) {
	txCount := 100
	typicalBlockData := types.Data{
		Txs:      generateRandomlySizedContiguousShares(txCount, 200),
		Messages: generateRandomlySizedMessages(10, 1500),
	}

	// compute the data availability header
	// todo(evan): add the non redundant shares back into the header
	shares, _ := typicalBlockData.ComputeShares()
	rawShares := shares.RawShares()

	sortByteArrays(rawShares)

	squareSize := uint64(math.Sqrt(float64(len(shares))))

	eds, err := da.ExtendShares(squareSize, rawShares)
	if err != nil {
		panic(err)
	}

	dah := da.NewDataAvailabilityHeader(eds)

	for i := 0; i < txCount; i++ {
		proofs, err := ProveTxInclusion(consts.DefaultCodec(), typicalBlockData, int(squareSize), i)
		require.NoError(t, err)
		for j, proof := range proofs {
			proof.VerifyInclusion(
				consts.NewBaseHashFunc(),
				consts.TxNamespaceID,
				typicalBlockData.Txs[i],
				dah.RowsRoots[0],
			)
			fmt.Println("j", j)
		}
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
			txs:  generateRandomlySizedContiguousShares(44, 100),
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
		start, end int
	}

	rand.Seed(time.Now().UTC().UnixNano())

	for _, tt := range tests {
		positions := make([]startEndPoints, len(tt.txs))
		for i := 0; i < len(tt.txs); i++ {
			start, end := txSharePosition(tt.txs, i)
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

func Test_generateRowShares(t *testing.T) {
	typicalBlockData := types.Data{
		Txs:      generateRandomlySizedContiguousShares(100, 200),
		Messages: generateRandomlySizedMessages(10, 1500),
	}

	allShares, _ := typicalBlockData.ComputeShares()
	rawShares := allShares.RawShares()
	squareSize := int(math.Sqrt(float64(len(rawShares))))

	firstRow := generateRowShares(typicalBlockData, squareSize, 0, squareSize/2)
	assert.Equal(t, rawShares[:squareSize-1], firstRow)

	secondRow := generateRowShares(typicalBlockData, squareSize, squareSize+1, squareSize+2)
	assert.Equal(t, rawShares[squareSize:(squareSize*2)-1], secondRow)

	firstAndSecondRow := generateRowShares(typicalBlockData, squareSize, squareSize-2, squareSize+1)
	assert.Equal(t, rawShares[:(squareSize*2)-1], firstAndSecondRow)
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
