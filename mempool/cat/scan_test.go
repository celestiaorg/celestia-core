package cat

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cometbft/cometbft/mempool"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
)

func TestValidateMempoolBytes(t *testing.T) {
	testCases := []struct {
		name    string
		msgB    []byte
		wantErr error
	}{
		{
			name: "single valid tx",
			msgB: txsMsgBytes(t, []byte("a valid transaction")),
		},
		{
			name:    "more entries than MaxTxsPerMessage",
			msgB:    txsMsgBytes(t, []byte("tx one"), []byte("tx two")),
			wantErr: errTooManyTxs,
		},
		{
			// The heap amplification vector: an empty entry costs ~2 bytes on
			// the wire but grows the [][]byte by a slice header when
			// unmarshalled into the real Txs message.
			name:    "many empty tx entries",
			msgB:    txsMsgBytes(t, make([][]byte, 10_000)...),
			wantErr: errTooManyTxs,
		},
		{
			// SeenTx and WantTx share the mempool channels with Txs and must
			// pass through untouched.
			name: "SeenTx passes through",
			msgB: mustMarshalMempoolMsg(t, &protomem.Message{
				Sum: &protomem.Message_SeenTx{SeenTx: &protomem.SeenTx{TxKey: make([]byte, 32)}},
			}),
		},
		{
			name: "WantTx passes through",
			msgB: mustMarshalMempoolMsg(t, &protomem.Message{
				Sum: &protomem.Message_WantTx{WantTx: &protomem.WantTx{TxKey: make([]byte, 32)}},
			}),
		},
		{
			name: "empty buffer passes through",
			msgB: nil,
		},
		{
			// A Txs submessage carrying no entries allocates nothing, so it is
			// left to the reactor to reject with its own error.
			name: "Txs with no entries passes through",
			msgB: txsMsgBytes(t),
		},
		{
			name: "single empty tx entry passes through",
			// One entry costs a single slice header, so there is nothing to
			// amplify; CheckTx owns the rejection.
			msgB: txsMsgBytes(t, []byte{}),
		},
		{
			name: "unknown field is skipped",
			// Field 15, wire type 0 (varint) — not a Message member.
			msgB: []byte{0x78, 0x01},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMempoolBytes(tc.msgB)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidateMempoolBytesMalformed checks that malformed wire bytes are
// rejected rather than panicking.
func TestValidateMempoolBytesMalformed(t *testing.T) {
	malformed := [][]byte{
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, // varint overflow
		{0x0a},             // tag with no length
		{0x0a, 0x02, 0x0a}, // nested length runs past the buffer
		{0x00},             // field number 0
	}
	for _, msgB := range malformed {
		require.Errorf(t, validateMempoolBytes(msgB), "bytes: %x", msgB)
	}
}

// TestValidateMempoolBytesMatchesReactorLimit pins the pre-unmarshal entry cap
// to the post-unmarshal check in the reactor.
func TestValidateMempoolBytesMatchesReactorLimit(t *testing.T) {
	txs := make([][]byte, 0, mempool.MaxTxsPerMessage+1)
	for range mempool.MaxTxsPerMessage {
		txs = append(txs, []byte("tx"))
	}
	require.NoError(t, validateMempoolBytes(txsMsgBytes(t, txs...)))

	txs = append(txs, []byte("one too many"))
	require.ErrorIs(t, validateMempoolBytes(txsMsgBytes(t, txs...)), errTooManyTxs)
}

// TestStubFieldNumbersMatchRealProto guards against the stub drifting from the
// message it mirrors — a mismatch would silently stop the filter from seeing
// the transactions it is meant to count.
func TestStubFieldNumbersMatchRealProto(t *testing.T) {
	pairs := []struct {
		name      string
		real      any
		realField string
		stub      any
		stubField string
	}{
		{"Message.txs", protomem.Message_Txs{}, "Txs", protomem.TxCountMessage{}, "Txs"},
		{"Txs.txs", protomem.Txs{}, "Txs", protomem.TxCountTxs{}, "Txs"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			require.Equal(t, protoFieldNum(t, p.real, p.realField), protoFieldNum(t, p.stub, p.stubField),
				"stub field number drifted from the real proto — update stub.proto and regenerate")
		})
	}
}

// TestStubUnmarshalAllocs is the whole point of the stub: unmarshalling must
// cost O(1) allocations no matter how many entries the message encodes.
func TestStubUnmarshalAllocs(t *testing.T) {
	for _, nTxs := range []int{10_000, 100_000, 1_000_000} {
		t.Run(strconv.Itoa(nTxs)+" empty txs", func(t *testing.T) {
			payload := txsMsgBytes(t, make([][]byte, nTxs)...)
			allocs := testing.AllocsPerRun(20, func() {
				var stub protomem.TxCountMessage
				require.NoError(t, stub.Unmarshal(payload))
				require.Len(t, stub.Txs.Txs, nTxs)
			})
			const maxAllocs = 50
			require.LessOrEqualf(t, int(allocs), maxAllocs, "unmarshal allocated %d times (max %d)", int(allocs), maxAllocs)
		})
	}
}

// TestReactorChannelsPrecheckTxs ensures the filter is actually wired to every
// channel that can carry transactions.
func TestReactorChannelsPrecheckTxs(t *testing.T) {
	reactor, _ := setupReactor(t)

	prechecks := make(map[byte]func([]byte) error)
	for _, chDesc := range reactor.GetChannels() {
		prechecks[chDesc.ID] = chDesc.RecvMessagePrecheck
	}

	for _, chID := range []byte{mempool.MempoolChannel, MempoolDataChannel} {
		precheck := prechecks[chID]
		require.NotNilf(t, precheck, "channel %#x carries txs but has no precheck", chID)

		require.NoError(t, precheck(txsMsgBytes(t, []byte("a valid transaction"))))
		require.ErrorIs(t, precheck(txsMsgBytes(t, make([][]byte, 10_000)...)), errTooManyTxs)
	}
}

func mustMarshalMempoolMsg(t *testing.T, msg *protomem.Message) []byte {
	t.Helper()
	bz, err := msg.Marshal()
	require.NoError(t, err)
	return bz
}

// txsMsgBytes encodes a mempool Message carrying the given transactions.
func txsMsgBytes(t *testing.T, txs ...[]byte) []byte {
	t.Helper()
	return mustMarshalMempoolMsg(t, &protomem.Message{
		Sum: &protomem.Message_Txs{Txs: &protomem.Txs{Txs: txs}},
	})
}

// protoFieldNum extracts the wire field number from a generated struct's
// protobuf tag, e.g. `protobuf:"bytes,1,opt,name=txs"` -> 1.
func protoFieldNum(t *testing.T, msg any, goField string) protowire.Number {
	t.Helper()
	f, ok := reflect.TypeOf(msg).FieldByName(goField)
	require.Truef(t, ok, "field %s not found on %T", goField, msg)
	parts := strings.Split(f.Tag.Get("protobuf"), ",")
	require.GreaterOrEqualf(t, len(parts), 2, "field %s on %T has no protobuf tag", goField, msg)
	num, err := strconv.Atoi(parts[1])
	require.NoError(t, err)
	return protowire.Number(num)
}

var _ proto.Message = (*protomem.TxCountMessage)(nil)
