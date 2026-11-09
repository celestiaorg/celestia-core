package cat

import (
	"bytes"
	"math"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"

	"github.com/cometbft/cometbft/internal/fuzzinput"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// maxFuzzEdits bounds the field edits applied to one structured message.
const maxFuzzEdits = 16

// mempoolEditSeeds are structured inputs: a fixture kind, an edit count, then
// per edit a field selector and its value.
var mempoolEditSeeds = [][]byte{
	{0, 1, 0, 0, 3, 'a', 'b', 'c'}, // Txs: second transaction, over MaxTxsPerMessage
	{0, 1, 0, 1},                   // Txs: no transactions
	{0, 1, 1, 1, 1},                // Txs: transaction wrapped in an IndexWrapper
	{0, 1, 1, 0, 0},                // Txs: transaction wrapped in a BlobTx
	{0, 1, 1, 0, 1},                // Txs: BlobTx with a short namespace
	{0, 2, 1, 1, 1, 2, 1, 0, 0},    // Txs: IndexWrapper with its first byte flipped
	{1, 1, 0, 2},                   // SeenTx: tx key one byte short
	{1, 1, 1, 0},                   // SeenTx: sequence MaxUint64
	{1, 1, 2, 3, 0},                // SeenTx: empty signer
	{2, 1, 0},                      // WantTx: empty tx key
	{2, 1, 1, 31, 0x80},            // WantTx: last tx key byte flipped
}

// mempoolFixtures returns one valid wire message of each CAT message type.
func mempoolFixtures(tb testing.TB) [][]byte {
	tb.Helper()
	seeds := []p2p.Wrapper{
		&protomem.Txs{Txs: [][]byte{[]byte("key=value")}},
		&protomem.SeenTx{TxKey: bytes.Repeat([]byte{1}, 32), Signer: []byte("signer"), Sequence: 1},
		&protomem.WantTx{TxKey: bytes.Repeat([]byte{1}, 32)},
	}
	fixtures := make([][]byte, 0, len(seeds))
	for _, seed := range seeds {
		raw, err := proto.Marshal(seed.Wrap())
		require.NoError(tb, err)
		require.NoError(tb, validateMempoolBytes(raw))
		var decoded protomem.Message
		require.NoError(tb, proto.Unmarshal(raw, &decoded))
		_, err = decoded.Unwrap()
		require.NoError(tb, err)
		fixtures = append(fixtures, raw)
	}
	return fixtures
}

// checkMempoolMessage runs the allocation precheck, envelope decode and
// unwrap the reactor applies to peer bytes, then what the reactor computes
// from each message. It returns the accepted message, or nil, after checking
// that it survives a proto round trip.
func checkMempoolMessage(t *testing.T, raw []byte) proto.Message {
	t.Helper()
	if err := validateMempoolBytes(raw); err != nil {
		return nil
	}
	var envelope protomem.Message
	if err := proto.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	msg, err := envelope.Unwrap()
	if err != nil {
		return nil
	}
	switch m := msg.(type) {
	case *protomem.Txs:
		require.LessOrEqual(t, len(m.Txs), mempool.MaxTxsPerMessage)
		for _, tx := range m.Txs {
			cached := cmttypes.Tx(tx).ToCachedTx()
			key := cached.Key()
			// Key and Hash unwrap the same way, so they name the same bytes.
			require.Equal(t, key[:], cmttypes.Tx(tx).Hash(), "tx key and hash disagree")
			_ = cached.Hash()
		}
	case *protomem.SeenTx:
		_, err := cmttypes.TxKeyFromBytes(m.TxKey)
		require.Equal(t, len(m.TxKey) == cmttypes.TxKeySize, err == nil)
	case *protomem.WantTx:
		_, err := cmttypes.TxKeyFromBytes(m.TxKey)
		require.Equal(t, len(m.TxKey) == cmttypes.TxKeySize, err == nil)
	default:
		t.Fatalf("unexpected mempool message %T", msg)
	}
	encoded, err := proto.Marshal(msg.(p2p.Wrapper).Wrap())
	require.NoError(t, err)
	require.NoError(t, validateMempoolBytes(encoded))
	var decoded protomem.Message
	require.NoError(t, proto.Unmarshal(encoded, &decoded))
	back, err := decoded.Unwrap()
	require.NoError(t, err)
	require.True(t, proto.Equal(msg, back), "message changed across proto round trip")
	return msg
}

// FuzzMempoolMessage exercises the allocation precheck before the full CAT
// envelope decoder. CheckTx itself is covered by test/fuzz/tests/FuzzMempool.
//
// With empty edits, raw is fed to the decoder as wire bytes. Otherwise edits
// selects a valid message, applies field-level edits to its proto form and
// sends the re-encoded envelope down the same path, including transactions
// wrapped as IndexWrapper or BlobTx so key derivation unwraps them.
func FuzzMempoolMessage(f *testing.F) {
	fixtures := mempoolFixtures(f)
	for _, raw := range fixtures {
		f.Add(raw, []byte(nil))
	}
	// Empty and over-limit lists, including a repeated oneof field, exercise
	// the precheck on inputs that never reach transaction handling.
	for _, raw := range [][]byte{nil, {0xff}, {0x0a, 0}, {0x0a, 2, 0x0a, 0, 0x0a, 2, 0x0a, 0}} {
		f.Add(raw, []byte(nil))
	}
	for kind := range fixtures {
		f.Add([]byte(nil), []byte{byte(kind), 0})
	}
	for _, edits := range mempoolEditSeeds {
		f.Add([]byte(nil), edits)
	}
	f.Fuzz(func(t *testing.T, raw []byte, edits []byte) {
		if len(edits) == 0 {
			if len(raw) > 1<<20 {
				return
			}
			checkMempoolMessage(t, raw)
			return
		}
		r := fuzzinput.New(edits)
		var envelope protomem.Message
		require.NoError(t, proto.Unmarshal(fixtures[r.Intn(len(fixtures))], &envelope))
		n := r.Intn(maxFuzzEdits + 1)
		for i := 0; i < n; i++ {
			editMempoolMessage(t, r, &envelope)
		}
		wire, err := proto.Marshal(&envelope)
		require.NoError(t, err)
		msg := checkMempoolMessage(t, wire)
		if n > 0 {
			return
		}
		require.NotNil(t, msg, "unedited fixture rejected")
		switch m := msg.(type) {
		case *protomem.SeenTx:
			_, err = cmttypes.TxKeyFromBytes(m.TxKey)
			require.NoError(t, err)
		case *protomem.WantTx:
			_, err = cmttypes.TxKeyFromBytes(m.TxKey)
			require.NoError(t, err)
		}
	})
}

// editMempoolMessage applies one field edit to the message in envelope.
func editMempoolMessage(t *testing.T, r *fuzzinput.Reader, envelope *protomem.Message) {
	switch m := envelope.Sum.(type) {
	case *protomem.Message_Txs:
		msg := m.Txs
		switch r.Intn(3) {
		case 0:
			// Append a transaction or clear the list.
			if r.Bool() {
				msg.Txs = nil
			} else if len(msg.Txs) < 4 {
				msg.Txs = append(msg.Txs, r.Bytes(128))
			}
		case 1:
			if len(msg.Txs) > 0 {
				i := len(msg.Txs) - 1
				msg.Txs[i] = wrapTx(t, r, msg.Txs[i])
			}
		case 2:
			if len(msg.Txs) > 0 {
				i := len(msg.Txs) - 1
				msg.Txs[i] = editBytes(r, msg.Txs[i])
			}
		}
	case *protomem.Message_SeenTx:
		msg := m.SeenTx
		switch r.Intn(3) {
		case 0:
			msg.TxKey = editBytes(r, msg.TxKey)
		case 1:
			switch r.Intn(3) {
			case 0:
				msg.Sequence = math.MaxUint64
			case 1:
				msg.Sequence = 0
			case 2:
				msg.Sequence = r.Uint64()
			}
		case 2:
			msg.Signer = editBytes(r, msg.Signer)
		}
	case *protomem.Message_WantTx:
		m.WantTx.TxKey = editBytes(r, m.WantTx.TxKey)
	}
}

// wrapTx wraps tx in an IndexWrapper or a BlobTx carrying one blob, whose
// namespace is either the valid size or one byte short.
func wrapTx(t *testing.T, r *fuzzinput.Reader, tx []byte) []byte {
	if r.Bool() {
		wrapped, err := cmttypes.MarshalIndexWrapper(tx, uint32(r.Byte()))
		require.NoError(t, err)
		return wrapped
	}
	ns := share.NamespaceIDSize
	if r.Bool() {
		ns--
	}
	// The legacy BlobTx and Blob messages are still accepted from peers.
	blobs := []*cmtproto.Blob{{NamespaceId: bytes.Repeat([]byte{1}, ns), Data: []byte("blob")}} //nolint:staticcheck

	blobTx := &cmtproto.BlobTx{Tx: tx, Blobs: blobs, TypeId: squaretx.ProtoBlobTxTypeID} //nolint:staticcheck
	wrapped, err := proto.Marshal(blobTx)
	require.NoError(t, err)
	return wrapped
}

// editBytes returns cur with one byte flipped, truncated by one, cleared, or
// replaced by up to 128 bytes from r.
func editBytes(r *fuzzinput.Reader, cur []byte) []byte {
	switch r.Intn(4) {
	case 0:
		return nil
	case 1:
		if len(cur) == 0 {
			return nil
		}
		out := append([]byte(nil), cur...)
		out[r.Intn(len(out))] ^= 1 | r.Byte()
		return out
	case 2:
		if len(cur) == 0 {
			return nil
		}
		return append([]byte(nil), cur[:len(cur)-1]...)
	default:
		return r.Bytes(128)
	}
}
