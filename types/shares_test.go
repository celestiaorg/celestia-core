package types

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	tmbytes "github.com/lazyledger/lazyledger-core/libs/bytes"
	"github.com/lazyledger/lazyledger-core/libs/protoio"
	"github.com/lazyledger/nmt/namespace"
	"github.com/lazyledger/rsmt2d"
	"github.com/stretchr/testify/assert"
)

type splitter interface {
	splitIntoShares() NamespacedShares
}

func TestMakeShares(t *testing.T) {
	reservedTxNamespaceID := append(bytes.Repeat([]byte{0}, 7), 1)
	reservedEvidenceNamespaceID := append(bytes.Repeat([]byte{0}, 7), 3)
	val := NewMockPV()
	blockID := makeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	blockID2 := makeBlockID([]byte("blockhash2"), 1000, []byte("partshash"))
	vote1 := makeVote(t, val, "chainID", 0, 10, 2, 1, blockID, defaultVoteTime)
	vote2 := makeVote(t, val, "chainID", 0, 10, 2, 1, blockID2, defaultVoteTime)
	testEvidence := &DuplicateVoteEvidence{
		VoteA: vote1,
		VoteB: vote2,
	}
	protoTestEvidence, err := EvidenceToProto(testEvidence)
	if err != nil {
		t.Error(err)
	}
	testEvidenceBytes, err := protoio.MarshalDelimited(protoTestEvidence)
	largeTx := Tx(bytes.Repeat([]byte("large Tx"), 50))
	largeTxLenDelimited, _ := largeTx.MarshalDelimited()
	smolTx := Tx("small Tx")
	smolTxLenDelimited, _ := smolTx.MarshalDelimited()
	msg1 := Message{
		NamespaceID: namespace.ID("8bytesss"),
		Data:        []byte("some data"),
	}
	msg1Marshaled, _ := msg1.MarshalDelimited()
	if err != nil {
		t.Fatalf("Could not encode evidence: %v, error: %v\n", testEvidence, err)
	}

	type args struct {
		data splitter
	}
	tests := []struct {
		name string
		args args
		want NamespacedShares
	}{
		{"evidence",
			args{
				data: &EvidenceData{
					Evidence: []Evidence{testEvidence},
				},
			}, NamespacedShares{NamespacedShare{
				Share: append(
					append(reservedEvidenceNamespaceID, byte(0)),
					testEvidenceBytes[:TxShareSize]...,
				),
				ID: reservedEvidenceNamespaceID,
			}, NamespacedShare{
				Share: append(
					append(reservedEvidenceNamespaceID, byte(0)),
					zeroPadIfNecessary(testEvidenceBytes[TxShareSize:], TxShareSize)...,
				),
				ID: reservedEvidenceNamespaceID,
			}},
		},
		{"small LL Tx",
			args{
				data: Txs{smolTx},
			},
			NamespacedShares{
				NamespacedShare{
					Share: append(
						append(reservedTxNamespaceID, byte(0)),
						zeroPadIfNecessary(smolTxLenDelimited, TxShareSize)...,
					),
					ID: reservedTxNamespaceID,
				},
			},
		},
		{"one large LL Tx",
			args{
				data: Txs{largeTx},
			},
			NamespacedShares{
				NamespacedShare{
					Share: append(
						append(reservedTxNamespaceID, byte(0)),
						largeTxLenDelimited[:TxShareSize]...,
					),
					ID: reservedTxNamespaceID,
				},
				NamespacedShare{
					Share: append(
						append(reservedTxNamespaceID, byte(0)),
						zeroPadIfNecessary(largeTxLenDelimited[TxShareSize:], TxShareSize)...,
					),
					ID: reservedTxNamespaceID,
				},
			},
		},
		{"large then small LL Tx",
			args{
				data: Txs{largeTx, smolTx},
			},
			NamespacedShares{
				NamespacedShare{
					Share: append(
						append(reservedTxNamespaceID, byte(0)),
						largeTxLenDelimited[:TxShareSize]...,
					),
					ID: reservedTxNamespaceID,
				},
				NamespacedShare{
					Share: append(
						append(reservedTxNamespaceID, byte(len(largeTxLenDelimited)-TxShareSize+NamespaceSize+ShareReservedBytes)),
						zeroPadIfNecessary(
							append(largeTxLenDelimited[TxShareSize:], smolTxLenDelimited...),
							TxShareSize,
						)...,
					),
					ID: reservedTxNamespaceID,
				},
			},
		},
		{"ll-app message",
			args{
				data: Messages{[]Message{msg1}},
			},
			NamespacedShares{
				NamespacedShare{
					Share: append(
						[]byte(msg1.NamespaceID),
						zeroPadIfNecessary(msg1Marshaled, MsgShareSize)...,
					),
					ID: msg1.NamespaceID,
				},
			},
		},
	}
	for i, tt := range tests {
		tt := tt // stupid scopelint :-/
		i := i
		t.Run(tt.name, func(t *testing.T) {
			got := tt.args.data.splitIntoShares()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%v: makeShares() = \n%+v\nwant\n%+v\n", i, got, tt.want)
			}
		})
	}
}

func Test_zeroPadIfNecessary(t *testing.T) {
	type args struct {
		share []byte
		width int
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		{"pad", args{[]byte{1, 2, 3}, 6}, []byte{1, 2, 3, 0, 0, 0}},
		{"not necessary (equal to shareSize)", args{[]byte{1, 2, 3}, 3}, []byte{1, 2, 3}},
		{"not necessary (greater shareSize)", args{[]byte{1, 2, 3}, 2}, []byte{1, 2, 3}},
	}
	for _, tt := range tests {
		tt := tt // stupid scopelint :-/
		t.Run(tt.name, func(t *testing.T) {
			if got := zeroPadIfNecessary(tt.args.share, tt.args.width); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("zeroPadIfNecessary() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_appendToSharesOverwrite(t *testing.T) {
	var shares NamespacedShares

	// generate some arbitrary namespaced shares first share that must be split
	newShare := generateRandomNamespacedShares(1, MsgShareSize+1)[0]

	// make a copy of the portion of the share to check if it's overwritten later
	extraCopy := make([]byte, MsgShareSize)
	copy(extraCopy, newShare.Share[:MsgShareSize])

	// use appendToShares to add our new share
	appendToShares(shares, newShare.ID, newShare.Share)

	// check if the original share data has been overwritten.
	assert.Equal(t, extraCopy, []byte(newShare.Share[:MsgShareSize]))
}

func TestDataFromSquare(t *testing.T) {
	type test struct {
		name     string
		txCount  int
		isrCount int
		evdCount int
		msgCount int
		maxSize  int // max size of each tx or msg
	}

	tests := []test{
		{"one of each random small size", 1, 1, 1, 1, 40},
		{"one of each random large size", 1, 1, 1, 1, 400},
		{"many of each random large size", 10, 10, 10, 10, 40},
		{"many of each random large size", 10, 10, 10, 10, 400},
		{"only transactions", 10, 0, 0, 0, 400},
		{"only intermediate state roots", 0, 10, 0, 0, 400},
		{"only evidence", 0, 0, 10, 0, 400},
		{"only messages", 0, 0, 0, 10, 400},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			// generate random data
			data := generateRandomBlockData(
				t,
				tc.txCount,
				tc.isrCount,
				tc.evdCount,
				tc.msgCount,
				tc.maxSize,
			)

			shares, _ := data.ComputeShares()
			rawShares := shares.RawShares()

			eds, err := rsmt2d.ComputeExtendedDataSquare(rawShares, rsmt2d.NewRSGF8Codec(), rsmt2d.NewDefaultTree)
			if err != nil {
				t.Error(err)
			}

			res, err := DataFromSquare(eds)
			if err != nil {
				t.Fatal(err)
			}

			// we have to compare the evidence by string because the the
			// timestamps differ not by actual time represented, but by
			// internals see https://github.com/stretchr/testify/issues/666
			for i := 0; i < len(data.Evidence.Evidence); i++ {
				inputEvidence := data.Evidence.Evidence[i].(*DuplicateVoteEvidence)
				resultEvidence := res.Evidence.Evidence[i].(*DuplicateVoteEvidence)
				assert.Equal(t, inputEvidence.String(), resultEvidence.String())
			}

			// compare the original to the result w/o the evidence
			data.Evidence = EvidenceData{}
			res.Evidence = EvidenceData{}

			assert.Equal(t, data, res)
		})
	}
}

func TestFuzz_DataFromSquare(t *testing.T) {
	t.Skip()
	// run random shares through processContiguousShares for a minute
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			TestDataFromSquare(t)
		}
	}
}

func Test_processContiguousShares(t *testing.T) {
	// exactTxShareSize is the length of tx that will fit exactly into a single
	// share, accounting for namespace id and the length delimiter prepended to
	// each tx
	const exactTxShareSize = TxShareSize - 1

	type test struct {
		name    string
		txSize  int
		txCount int
	}

	// each test is ran twice, once using txSize as an exact size, and again
	// using it as a cap for randomly sized txs
	tests := []test{
		{"single small tx", 10, 1},
		{"many small txs", 10, 10},
		{"single big tx", 1000, 1},
		{"many big txs", 1000, 10},
		{"single exact size tx", exactTxShareSize, 1},
		{"many exact size txs", exactTxShareSize, 10},
	}

	for _, tc := range tests {
		tc := tc

		// run the tests with identically sized txs
		t.Run(fmt.Sprintf("%s idendically sized ", tc.name), func(t *testing.T) {
			txs := generateRandomContiguousShares(tc.txCount, tc.txSize)

			shares := txs.splitIntoShares()

			parsedTxs, err := processContiguousShares(shares.RawShares())
			if err != nil {
				t.Error(err)
			}

			// check that the data parsed is identical
			for i := 0; i < len(txs); i++ {
				assert.Equal(t, []byte(txs[i]), parsedTxs[i])
			}
		})

		// run the same tests using randomly sized txs with caps of tc.txSize
		t.Run(fmt.Sprintf("%s randomly sized", tc.name), func(t *testing.T) {
			txs := generateRandomlySizedContiguousShares(tc.txCount, tc.txSize)

			shares := txs.splitIntoShares()

			parsedTxs, err := processContiguousShares(shares.RawShares())
			if err != nil {
				t.Error(err)
			}

			// check that the data parsed is identical to the original
			for i := 0; i < len(txs); i++ {
				assert.Equal(t, []byte(txs[i]), parsedTxs[i])
			}
		})
	}
}

func TestFuzz_processContiguousShares(t *testing.T) {
	t.Skip()
	// run random shares through processContiguousShares for a minute
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			Test_processContiguousShares(t)
		}
	}
}

func Test_parseMsgShares(t *testing.T) {
	// exactMsgShareSize is the length of message that will fit exactly into a single
	// share, accounting for namespace id and the length delimiter prepended to
	// each message
	const exactMsgShareSize = MsgShareSize - 2

	type test struct {
		name     string
		msgSize  int
		msgCount int
	}

	// each test is ran twice, once using msgSize as an exact size, and again
	// using it as a cap for randomly sized leaves
	tests := []test{
		{"single small msg", 1, 1},
		{"many small msgs", 4, 10},
		{"single big msg", 1000, 1},
		{"many big msgs", 1000, 10},
		{"single exact size msg", exactMsgShareSize, 1},
		{"many exact size msgs", exactMsgShareSize, 10},
	}

	for _, tc := range tests {
		tc := tc

		// run the tests with identically sized messagses
		t.Run(fmt.Sprintf("%s idendically sized ", tc.name), func(t *testing.T) {
			rawmsgs := make([]Message, tc.msgCount)
			for i := 0; i < tc.msgCount; i++ {
				rawmsgs[i] = generateRandomMessage(tc.msgSize)
			}
			msgs := Messages{MessagesList: rawmsgs}

			shares := msgs.splitIntoShares()

			parsedMsgs, err := parseMsgShares(shares.RawShares())
			if err != nil {
				t.Error(err)
			}

			// check that the namesapces and data are the same
			for i := 0; i < len(msgs.MessagesList); i++ {
				assert.Equal(t, msgs.MessagesList[i].NamespaceID, parsedMsgs[i].NamespaceID)
				assert.Equal(t, msgs.MessagesList[i].Data, parsedMsgs[i].Data)
			}
		})

		// run the same tests using randomly sized messages with caps of tc.msgSize
		t.Run(fmt.Sprintf("%s randomly sized", tc.name), func(t *testing.T) {
			msgs := generateRandomlySizedMessages(tc.msgCount, tc.msgSize)
			shares := msgs.splitIntoShares()

			parsedMsgs, err := parseMsgShares(shares.RawShares())
			if err != nil {
				t.Error(err)
			}

			// check that the namesapces and data are the same
			for i := 0; i < len(msgs.MessagesList); i++ {
				assert.Equal(t, msgs.MessagesList[i].NamespaceID, parsedMsgs[i].NamespaceID)
				assert.Equal(t, msgs.MessagesList[i].Data, parsedMsgs[i].Data)
			}
		})
	}
}

func Test_parseDelimiter(t *testing.T) {
	for i := uint64(0); i < 100; i++ {
		tx := generateRandomContiguousShares(1, int(i))[0]
		input, err := tx.MarshalDelimited()
		if err != nil {
			panic(err)
		}
		res, txLen, err := parseDelimiter(input)
		if err != nil {
			panic(err)
		}
		assert.Equal(t, i, txLen)
		assert.Equal(t, []byte(tx), res)
	}
}

// generateRandomBlockData returns randomly generated block data for testing purposes
func generateRandomBlockData(t *testing.T, txCount, isrCount, evdCount, msgCount, maxSize int) Data {
	var out Data
	out.Txs = generateRandomlySizedContiguousShares(txCount, maxSize)
	out.IntermediateStateRoots = generateRandomISR(isrCount)
	out.Evidence = generateIdenticalEvidence(t, evdCount)
	out.Messages = generateRandomlySizedMessages(msgCount, maxSize)
	return out
}

func generateRandomlySizedContiguousShares(count, max int) Txs {
	txs := make(Txs, count)
	for i := 0; i < count; i++ {
		size := rand.Intn(max)
		if size == 0 {
			size = 1
		}
		txs[i] = generateRandomContiguousShares(1, size)[0]
	}
	return txs
}

func generateRandomContiguousShares(count, size int) Txs {
	txs := make(Txs, count)
	for i := 0; i < count; i++ {
		tx := make([]byte, size)
		_, err := rand.Read(tx)
		if err != nil {
			panic(err)
		}
		txs[i] = Tx(tx)
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

func generateIdenticalEvidence(t *testing.T, count int) EvidenceData {
	evidence := make([]Evidence, count)
	for i := 0; i < count; i++ {
		ev := NewMockDuplicateVoteEvidence(math.MaxInt64, time.Now(), "chainID")
		evidence[i] = ev
	}
	return EvidenceData{Evidence: EvidenceList(evidence)}
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
	shares := generateRandNamespacedRawData(count, NamespaceSize, msgSize)
	msgs := make([]Message, count)
	for i, s := range shares {
		msgs[i] = Message{
			Data:        s[NamespaceSize:],
			NamespaceID: s[:NamespaceSize],
		}
	}
	return Messages{MessagesList: msgs}.splitIntoShares()
}
