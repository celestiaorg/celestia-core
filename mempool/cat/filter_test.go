package cat

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
)

const testMaxTxSize = 1024

func mustMarshalMempoolMsg(t *testing.T, msg *protomem.Message) []byte {
	t.Helper()
	bz, err := msg.Marshal()
	require.NoError(t, err)
	return bz
}

func txsMsgBytes(t *testing.T, txs ...[]byte) []byte {
	t.Helper()
	return mustMarshalMempoolMsg(t, &protomem.Message{
		Sum: &protomem.Message_Txs{Txs: &protomem.Txs{Txs: txs}},
	})
}

func TestFilterTxsMsgBytes(t *testing.T) {
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
			name: "tx of exactly max size",
			msgB: txsMsgBytes(t, bytes.Repeat([]byte("x"), testMaxTxSize)),
		},
		{
			name:    "tx over max size",
			msgB:    txsMsgBytes(t, bytes.Repeat([]byte("x"), testMaxTxSize+1)),
			wantErr: ErrTxTooLarge,
		},
		{
			// The heap amplification vector: an empty entry costs ~2 bytes on
			// the wire but forces an allocation when unmarshalled.
			name:    "single empty tx entry",
			msgB:    txsMsgBytes(t, []byte{}),
			wantErr: ErrEmptyTxEntry,
		},
		{
			name:    "many empty tx entries",
			msgB:    txsMsgBytes(t, make([][]byte, 10_000)...),
			wantErr: ErrEmptyTxEntry,
		},
		{
			name:    "more entries than MaxTxsPerMessage",
			msgB:    txsMsgBytes(t, []byte("tx one"), []byte("tx two")),
			wantErr: ErrTooManyTxEntries,
		},
		{
			// SeenTx and WantTx share the mempool channels with Txs and must
			// pass through untouched.
			name: "SeenTx passes through",
			msgB: mustMarshalMempoolMsg(t, &protomem.Message{
				Sum: &protomem.Message_SeenTx{SeenTx: &protomem.SeenTx{TxKey: bytes.Repeat([]byte{1}, 32)}},
			}),
		},
		{
			name: "WantTx passes through",
			msgB: mustMarshalMempoolMsg(t, &protomem.Message{
				Sum: &protomem.Message_WantTx{WantTx: &protomem.WantTx{TxKey: bytes.Repeat([]byte{1}, 32)}},
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
			name:    "truncated length prefix",
			msgB:    []byte{0x0a, 0x05},
			wantErr: nil, // errors, but with a wire-level error; asserted below
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := filterTxsMsgBytes(tc.msgB, testMaxTxSize)
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.name == "truncated length prefix":
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
		})
	}
}

// TestFilterTxsMsgBytes_Malformed checks that malformed wire bytes are rejected
// rather than panicking.
func TestFilterTxsMsgBytes_Malformed(t *testing.T) {
	malformed := [][]byte{
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, // varint overflow
		{0x0a},             // tag with no length
		{0x0a, 0x02, 0x0a}, // nested length runs past the buffer
		{0x00},             // field number 0
	}
	for _, msgB := range malformed {
		require.Error(t, filterTxsMsgBytes(msgB, testMaxTxSize), "bytes: %x", msgB)
	}
}

// TestFilterTxsMsgBytes_UnknownFieldsSkipped ensures the scan tolerates fields
// it does not know about instead of rejecting the message.
func TestFilterTxsMsgBytes_UnknownFieldsSkipped(t *testing.T) {
	// field 15, wire type 0 (varint), value 1 — not a known Message member.
	unknown := []byte{0x78, 0x01}
	require.NoError(t, filterTxsMsgBytes(unknown, testMaxTxSize))
}

// TestFilterTxsMsgBytes_MatchesReactorLimit pins the pre-unmarshal entry cap to
// the post-unmarshal check in the reactor.
func TestFilterTxsMsgBytes_MatchesReactorLimit(t *testing.T) {
	txs := make([][]byte, 0, mempool.MaxTxsPerMessage+1)
	for range mempool.MaxTxsPerMessage {
		txs = append(txs, []byte("tx"))
	}
	require.NoError(t, filterTxsMsgBytes(txsMsgBytes(t, txs...), testMaxTxSize))

	txs = append(txs, []byte("one too many"))
	require.ErrorIs(t, filterTxsMsgBytes(txsMsgBytes(t, txs...), testMaxTxSize), ErrTooManyTxEntries)
}

// TestReactorChannelsPrecheckTxs ensures the filter is actually wired to every
// channel that can carry transactions, and that the wired limit tracks the
// reactor's configured MaxTxSize.
func TestReactorChannelsPrecheckTxs(t *testing.T) {
	reactor, _ := setupReactor(t)

	prechecks := make(map[byte]func([]byte) error)
	for _, chDesc := range reactor.GetChannels() {
		prechecks[chDesc.ID] = chDesc.RecvMessagePrecheck
	}

	for _, chID := range []byte{mempool.MempoolChannel, MempoolDataChannel} {
		precheck := prechecks[chID]
		require.NotNil(t, precheck, "channel %#x carries txs but has no precheck", chID)

		require.ErrorIs(t, precheck(txsMsgBytes(t, []byte{})), ErrEmptyTxEntry)
		require.NoError(t, precheck(txsMsgBytes(t, []byte("a valid transaction"))))

		maxTxSize := reactor.opts.MaxTxSize
		require.NoError(t, precheck(txsMsgBytes(t, bytes.Repeat([]byte("x"), maxTxSize))))
		require.ErrorIs(t, precheck(txsMsgBytes(t, bytes.Repeat([]byte("x"), maxTxSize+1))), ErrTxTooLarge)
	}
}
