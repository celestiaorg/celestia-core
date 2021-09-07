package fraudproofs

import (
	"bytes"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/celestiaorg/celestia-core/pkg/consts"
	"github.com/celestiaorg/celestia-core/types"
	tmproto "github.com/proto/tendermint/types"
	"github.com/stretchr/testify/require"
)

type BadEncodingError int

func TestBadEncodingFraudProof(t *testing.T) {
	type test struct {
		name        string
		input       BadEncodingFraudProof
		dah         types.DataAvailabilityHeader
		output      bool
		expectedErr string
	}

	validBEFP := validBadEncodingProof()
	bvalidBadEncodingProof()
	// TODO: template for table driven test for befp
	// Call CreateBadEncodingFraudProof below to get shareProofs.
	// CreateBadEncodingFraudProof(block types.Block, dah *tmproto.DataAvailabilityHeader)
	tests := []test{
		{
			name: "Block with bad encoding",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: true,
		},
		{
			name: "BadEncodingFraudProof for a correct block",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil,
		},
		{
			name: "Incorrect number of shares",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil, // How do we denote the error type?
		},
		{
			name: "Position out of bound",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil,
		},
		{
			name: "Non committed shares",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil,
		},
		{
			name: "Default",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output:      false,
			expectedErr: "I expect something here",
		},
	}

	for _, tt := range tests {
		res, err := VerifyBadEncodingFraudProof(tt.input, tt.dah)
		require.Equal(t, tt.output, res)
		if tt.expectedErr != "" {
			require.Contains(t, err.Error(), tt.expectedErr)
		}
	}
}

func validBadEncodingProof()

// generateRandomBlockData returns randomly generated block data for testing purposes
func generateRandomBlockData(txCount, isrCount, evdCount, msgCount, maxSize int) types.Data {
	var out types.Data
	out.Txs = generateRandomlySizedContiguousShares(txCount, maxSize)
	out.IntermediateStateRoots = generateRandomISR(isrCount)
	out.Evidence = generateIdenticalEvidence(evdCount)
	out.Messages = generateRandomlySizedMessages(msgCount, maxSize)
	return out
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

func generateRandomISR(count int) IntermediateStateRoots {
	roots := make([]tmbytes.HexBytes, count)
	for i := 0; i < count; i++ {
		roots[i] = tmbytes.HexBytes(generateRandomContiguousShares(1, 32)[0])
	}
	return IntermediateStateRoots{RawRootsList: roots}
}

func generateIdenticalEvidence(count int) EvidenceData {
	evidence := make([]Evidence, count)
	for i := 0; i < count; i++ {
		ev := NewMockDuplicateVoteEvidence(math.MaxInt64, time.Now(), "chainID")
		evidence[i] = ev
	}
	return EvidenceData{Evidence: evidence}
}

func generateRandomlySizedMessages(count, maxMsgSize int) Messages {
	msgs := make([]Message, count)
	for i := 0; i < count; i++ {
		msgs[i] = generateRandomMessage(rand.Intn(maxMsgSize))
	}

	// this is just to let us use assert.Equal
	if count == 0 {
		msgs = nil
	}

	return Messages{MessagesList: msgs}
}

func generateRandomMessage(size int) Message {
	share := generateRandomNamespacedShares(1, size)[0]
	msg := Message{
		NamespaceID: share.NamespaceID(),
		Data:        share.Data(),
	}
	return msg
}

func generateRandomNamespacedShares(count, msgSize int) NamespacedShares {
	shares := generateRandNamespacedRawData(uint32(count), consts.NamespaceSize, uint32(msgSize))
	msgs := make([]Message, count)
	for i, s := range shares {
		msgs[i] = Message{
			Data:        s[consts.NamespaceSize:],
			NamespaceID: s[:consts.NamespaceSize],
		}
	}
	return Messages{MessagesList: msgs}.SplitIntoShares()
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
