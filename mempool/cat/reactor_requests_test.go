package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	cmttypes "github.com/cometbft/cometbft/types"
)

// wantTxKeys returns the keys of the WantTx messages in envs.
func wantTxKeys(envs []p2p.Envelope) []cmttypes.TxKey {
	var keys []cmttypes.TxKey
	for _, e := range envs {
		msg, ok := e.Message.(*protomem.Message)
		if !ok {
			continue
		}
		if w, ok := msg.Sum.(*protomem.Message_WantTx); ok {
			key, err := cmttypes.TxKeyFromBytes(w.WantTx.TxKey)
			if err == nil {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// setupRerequestedTx has peers 0 and 1 announce the same tx, lets the request
// to peer 0 time out so the tx is re-requested from peer 1, and returns the tx.
func setupRerequestedTx(t *testing.T, h *catFuzzHarness) fzTxMeta {
	m := h.tx(0, 0, 0)
	key := m.tx.Key()
	seen := &protomem.SeenTx{TxKey: key[:], Signer: m.signer, Sequence: 0}
	h.receive(0, seen, MempoolDataChannel)
	require.Equal(t, []cmttypes.TxKey{key}, wantTxKeys(h.peers[0].drain()))
	h.receive(1, seen, MempoolDataChannel)
	h.timeout(0, 0)
	require.Equal(t, []cmttypes.TxKey{key}, wantTxKeys(h.peers[1].drain()))
	require.Equal(t, h.peerID(1), h.r.requests.ForTx(key))
	require.True(t, h.r.requests.Has(h.peerID(0), key)) // late-response slot
	return m
}

func TestRemovePeerDoesNotReRequestPooledTx(t *testing.T) {
	h := newCatFuzzHarness(t, 0)
	m := setupRerequestedTx(t, h)
	key := m.tx.Key()

	h.receive(1, &protomem.Txs{Txs: [][]byte{m.tx}}, MempoolDataChannel)
	require.True(t, h.pool.Has(key))
	h.peers[1].drain()

	h.sw.StopPeerForError(h.peers[0], "gone", h.r.String())
	require.NotContains(t, wantTxKeys(h.peers[1].drain()), key, "re-requested a tx already in the pool")
}

func TestRemovePeerKeepsOtherPeersRequest(t *testing.T) {
	h := newCatFuzzHarness(t, 0)
	h.connect(2)
	m := setupRerequestedTx(t, h)
	key := m.tx.Key()
	h.receive(2, &protomem.SeenTx{TxKey: key[:], Signer: m.signer, Sequence: 0}, MempoolDataChannel)
	h.peers[2].drain()

	// peer 1's request is still in flight when peer 0 disconnects
	h.sw.StopPeerForError(h.peers[0], "gone", h.r.String())
	require.Equal(t, h.peerID(1), h.r.requests.ForTx(key))
	require.True(t, h.r.requests.Has(h.peerID(1), key))
	require.Empty(t, wantTxKeys(h.peers[1].drain()))
	require.Empty(t, wantTxKeys(h.peers[2].drain()), "requested a tx already requested from another peer")
}

func TestRequestTimeoutDoesNotReRequestPooledTx(t *testing.T) {
	h := newCatFuzzHarness(t, 0)
	m := setupRerequestedTx(t, h)
	key := m.tx.Key()

	// peer 0 answers late; peer 1's request is still outstanding
	h.receive(0, &protomem.Txs{Txs: [][]byte{m.tx}}, MempoolDataChannel)
	require.True(t, h.pool.Has(key))
	h.peers[0].drain()
	h.peers[1].drain()

	h.timeout(1, 0)
	require.NotContains(t, wantTxKeys(h.peers[0].drain()), key, "re-requested a tx already in the pool")
}
