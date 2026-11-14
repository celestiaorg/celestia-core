package cat

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

const testReceivedBufferMaxBytes = 1 << 20

func TestReceivedTxBuffer(t *testing.T) {
	signer1 := []byte("signer1")
	signer2 := []byte("signer2")

	makeTx := func(name string) (*types.CachedTx, types.TxKey) {
		tx := types.Tx(name)
		return tx.ToCachedTx(), tx.Key()
	}

	t.Run("add and get transaction", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")
		txInfo := mempool.TxInfo{SenderID: 1}

		added := buf.add(signer1, 5, tx, key, txInfo, "peer1")
		require.True(t, added)

		got := buf.get(signer1, 5)
		require.NotNil(t, got)
		require.Equal(t, tx, got.tx)
		require.Equal(t, key, got.txKey)
		require.Equal(t, uint64(5), got.sequence)
		require.Equal(t, "peer1", got.peerID)
	})

	t.Run("get returns nil for non-existent signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		buf.add(signer1, 5, tx, key, mempool.TxInfo{}, "peer1")

		got := buf.get(signer2, 5)
		require.Nil(t, got)
	})

	t.Run("get returns nil for non-existent sequence", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		buf.add(signer1, 5, tx, key, mempool.TxInfo{}, "peer1")

		got := buf.get(signer1, 6)
		require.Nil(t, got)
	})

	t.Run("add returns false for duplicate", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx1, key1 := makeTx("tx1")
		tx2, key2 := makeTx("tx2")

		added := buf.add(signer1, 5, tx1, key1, mempool.TxInfo{}, "peer1")
		require.True(t, added)

		// Same signer and sequence, different tx
		added = buf.add(signer1, 5, tx2, key2, mempool.TxInfo{}, "peer2")
		require.False(t, added)

		// Original tx should still be there
		got := buf.get(signer1, 5)
		require.Equal(t, tx1, got.tx)
	})

	t.Run("add returns false for empty signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		added := buf.add(nil, 5, tx, key, mempool.TxInfo{}, "peer1")
		require.False(t, added)

		added = buf.add([]byte{}, 5, tx, key, mempool.TxInfo{}, "peer1")
		require.False(t, added)
	})

	t.Run("get returns nil for empty signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		buf.add(signer1, 5, tx, key, mempool.TxInfo{}, "peer1")

		got := buf.get(nil, 5)
		require.Nil(t, got)

		got = buf.get([]byte{}, 5)
		require.Nil(t, got)
	})

	t.Run("respects max buffer size per signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)

		// Fill buffer to max for signer using different peers to avoid per-peer limit
		for i := uint64(0); i < maxReceivedBufferSize; i++ {
			tx, key := makeTx("tx" + string(rune(i)))
			peerID := "peer" + string(rune(i%10)) // rotate through 10 peers
			added := buf.add(signer1, i+100, tx, key, mempool.TxInfo{}, peerID)
			require.True(t, added, "should add tx at sequence %d", i+100)
		}

		// Next add should fail (signer buffer full)
		tx, key := makeTx("overflow")
		added := buf.add(signer1, 200, tx, key, mempool.TxInfo{}, "peer-new")
		require.False(t, added)

		// But adding for a different signer should work
		added = buf.add(signer2, 100, tx, key, mempool.TxInfo{}, "peer-new")
		require.True(t, added)
	})

	t.Run("respects max buffer size per peer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)

		// Fill buffer to max for peer1 using different signers
		for i := uint64(0); i < uint64(maxRequestsPerPeer); i++ {
			tx, key := makeTx("tx" + string(rune(i)))
			signer := []byte("signer" + string(rune(i)))
			added := buf.add(signer, i+100, tx, key, mempool.TxInfo{}, "peer1")
			require.True(t, added, "should add tx at sequence %d", i+100)
		}

		require.Equal(t, maxRequestsPerPeer, buf.countForPeer("peer1"))

		// Next add from peer1 should fail
		tx, key := makeTx("overflow")
		newSigner := []byte("new-signer")
		added := buf.add(newSigner, 200, tx, key, mempool.TxInfo{}, "peer1")
		require.False(t, added)

		// But adding from a different peer should work
		added = buf.add(newSigner, 200, tx, key, mempool.TxInfo{}, "peer2")
		require.True(t, added)
		require.Equal(t, 1, buf.countForPeer("peer2"))
	})

	t.Run("peer count decrements on removeLowerSeqs", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)

		// Add 5 txs from peer1
		for i := uint64(1); i <= 5; i++ {
			tx, key := makeTx("tx" + string(rune(i)))
			buf.add(signer1, i, tx, key, mempool.TxInfo{}, "peer1")
		}

		require.Equal(t, 5, buf.countForPeer("peer1"))

		// Remove sequences <= 3
		buf.removeLowerSeqs(signer1, 3)

		// Only 2 txs should remain (seq 4 and 5)
		require.Equal(t, 2, buf.countForPeer("peer1"))

		// Remove remaining
		buf.removeLowerSeqs(signer1, 10)

		// Peer count should be 0 (and cleaned up)
		require.Equal(t, 0, buf.countForPeer("peer1"))
	})

	t.Run("peer count tracks multiple peers correctly", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)

		// Add txs from different peers
		tx1, key1 := makeTx("tx1")
		tx2, key2 := makeTx("tx2")
		tx3, key3 := makeTx("tx3")

		buf.add(signer1, 1, tx1, key1, mempool.TxInfo{}, "peer1")
		buf.add(signer1, 2, tx2, key2, mempool.TxInfo{}, "peer2")
		buf.add(signer1, 3, tx3, key3, mempool.TxInfo{}, "peer1")

		require.Equal(t, 2, buf.countForPeer("peer1"))
		require.Equal(t, 1, buf.countForPeer("peer2"))

		// Remove seq 1 (from peer1)
		buf.removeLowerSeqs(signer1, 1)

		require.Equal(t, 1, buf.countForPeer("peer1"))
		require.Equal(t, 1, buf.countForPeer("peer2"))
	})

	t.Run("removeLowerSeqs removes transactions", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)

		for i := uint64(1); i <= 5; i++ {
			tx, key := makeTx("tx" + string(rune(i)))
			buf.add(signer1, i, tx, key, mempool.TxInfo{}, "peer1")
		}

		// Remove sequences <= 3
		buf.removeLowerSeqs(signer1, 3)

		// Sequences 1, 2, 3 should be gone
		require.Nil(t, buf.get(signer1, 1))
		require.Nil(t, buf.get(signer1, 2))
		require.Nil(t, buf.get(signer1, 3))

		// Sequences 4, 5 should remain
		require.NotNil(t, buf.get(signer1, 4))
		require.NotNil(t, buf.get(signer1, 5))
	})

	t.Run("removeLowerSeqs cleans up empty signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		buf.add(signer1, 5, tx, key, mempool.TxInfo{}, "peer1")
		require.Len(t, buf.signerKeys(), 1)
		buf.removeLowerSeqs(signer1, 10)

		// Signer should be removed from the map
		require.Len(t, buf.signerKeys(), 0)
	})

	t.Run("removeLowerSeqs handles empty signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		buf.add(signer1, 5, tx, key, mempool.TxInfo{}, "peer1")

		buf.removeLowerSeqs(nil, 5)
		buf.removeLowerSeqs([]byte{}, 5)

		// Original tx should still be there
		require.NotNil(t, buf.get(signer1, 5))
	})

	t.Run("removeLowerSeqs handles non-existent signer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx, key := makeTx("tx1")

		buf.add(signer1, 5, tx, key, mempool.TxInfo{}, "peer1")
		buf.removeLowerSeqs(signer2, 5)

		// Original tx should still be there
		require.NotNil(t, buf.get(signer1, 5))
	})

	t.Run("signerKeys returns all signers", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx1, key1 := makeTx("tx1")
		tx2, key2 := makeTx("tx2")

		buf.add(signer1, 1, tx1, key1, mempool.TxInfo{}, "peer1")
		buf.add(signer2, 1, tx2, key2, mempool.TxInfo{}, "peer1")

		signers := buf.signerKeys()
		require.Len(t, signers, 2)

		// Check both signers are present (order not guaranteed)
		signerSet := make(map[string]bool)
		for _, s := range signers {
			signerSet[string(s)] = true
		}
		require.True(t, signerSet[string(signer1)])
		require.True(t, signerSet[string(signer2)])
	})

	t.Run("signerKeys returns nil for empty buffer", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		require.Nil(t, buf.signerKeys())
	})

	t.Run("multiple signers are independent", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx1, key1 := makeTx("tx1")
		tx2, key2 := makeTx("tx2")

		buf.add(signer1, 5, tx1, key1, mempool.TxInfo{}, "peer1")
		buf.add(signer2, 5, tx2, key2, mempool.TxInfo{}, "peer2")

		got1 := buf.get(signer1, 5)
		got2 := buf.get(signer2, 5)

		require.NotNil(t, got1)
		require.NotNil(t, got2)
		require.Equal(t, tx1, got1.tx)
		require.Equal(t, tx2, got2.tx)

		// Remove from signer1 only
		buf.removeLowerSeqs(signer1, 5)

		require.Nil(t, buf.get(signer1, 5))
		require.NotNil(t, buf.get(signer2, 5))
	})

	t.Run("respects total byte limit across peers and signers", func(t *testing.T) {
		// Each peer may use 10 bytes, so 10 peers fill the 100-byte cap.
		buf := newReceivedTxBuffer(100, 0)
		for i := 0; i < receivedBufferPeerShare; i++ {
			tx, key := makeTx(fmt.Sprintf("tx-%07d", i)) // 10 bytes
			signer := []byte(fmt.Sprintf("signer-%d", i))
			require.True(t, buf.add(signer, 1, tx, key, mempool.TxInfo{}, fmt.Sprintf("peer%d", i)))
		}
		n, size := buf.size()
		require.Equal(t, receivedBufferPeerShare, n)
		require.Equal(t, int64(100), size)

		tx, key := makeTx("x")
		require.False(t, buf.add(signer1, 1, tx, key, mempool.TxInfo{}, "fresh-peer"), "would exceed byte limit")

		buf.removeLowerSeqs([]byte("signer-0"), 1)
		_, size = buf.size()
		require.Equal(t, int64(90), size)
		require.True(t, buf.add(signer1, 1, tx, key, mempool.TxInfo{}, "fresh-peer"), "space freed")
	})

	t.Run("respects byte budget per peer", func(t *testing.T) {
		buf := newReceivedTxBuffer(100, 0) // 10 bytes per peer
		tx1, key1 := makeTx("aaaaaa")      // 6 bytes
		tx2, key2 := makeTx("bbbbbb")
		tx3, key3 := makeTx("cccc")

		require.True(t, buf.add(signer1, 1, tx1, key1, mempool.TxInfo{}, "peer1"))
		require.False(t, buf.add(signer1, 2, tx2, key2, mempool.TxInfo{}, "peer1"), "would exceed peer budget")
		require.True(t, buf.add(signer1, 2, tx2, key2, mempool.TxInfo{}, "peer2"), "other peer has its own budget")
		require.True(t, buf.add(signer2, 1, tx3, key3, mempool.TxInfo{}, "peer1"), "fits the peer budget exactly")
		require.Equal(t, int64(10), buf.bytesForPeer("peer1"))
		require.Equal(t, int64(6), buf.bytesForPeer("peer2"))

		// Budget is released by removeLowerSeqs, removeBelow and pruneOlderThan.
		buf.removeLowerSeqs(signer1, 1)
		require.Equal(t, int64(4), buf.bytesForPeer("peer1"))
		require.Len(t, buf.removeBelow(signer2, 2), 1)
		require.Zero(t, buf.bytesForPeer("peer1"))
		buf.buffers[string(signer1)][2].addedAt = time.Now().Add(-time.Hour)
		require.Len(t, buf.pruneOlderThan(time.Now().Add(-time.Minute)), 1)
		require.Zero(t, buf.bytesForPeer("peer2"))
		require.Empty(t, buf.bytesByPeer)
	})

	t.Run("removeBelow keeps the boundary sequence", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		for seq := uint64(1); seq <= 4; seq++ {
			tx, key := makeTx(fmt.Sprintf("tx%d", seq))
			require.True(t, buf.add(signer1, seq, tx, key, mempool.TxInfo{}, "peer1"))
		}

		removed := buf.removeBelow(signer1, 3)
		require.Len(t, removed, 2)
		require.Nil(t, buf.get(signer1, 2))
		require.NotNil(t, buf.get(signer1, 3))
		require.Equal(t, 2, buf.countForPeer("peer1"))
		require.Empty(t, buf.removeBelow(signer1, 0))
		require.Empty(t, buf.removeBelow(nil, 10))
	})

	t.Run("pruneOlderThan removes only old entries", func(t *testing.T) {
		buf := newReceivedTxBuffer(testReceivedBufferMaxBytes, 0)
		tx1, key1 := makeTx("old")
		tx2, key2 := makeTx("new")
		require.True(t, buf.add(signer1, 1, tx1, key1, mempool.TxInfo{}, "peer1"))
		require.True(t, buf.add(signer2, 1, tx2, key2, mempool.TxInfo{}, "peer2"))
		buf.buffers[string(signer1)][1].addedAt = time.Now().Add(-time.Hour)

		removed := buf.pruneOlderThan(time.Now().Add(-time.Minute))
		require.Equal(t, []types.TxKey{key1}, removed)
		require.Zero(t, buf.countForPeer("peer1"))
		require.Equal(t, 1, buf.countForPeer("peer2"))
		require.Equal(t, [][]byte{signer2}, buf.signerKeys())
		_, size := buf.size()
		require.Equal(t, int64(len(tx2.Tx)), size)
	})
}

// TestReceivedTxBufferPeerCannotStarveOthers is a regression test for
// starvation on the shared byte cap: two peers answer with large junk txs
// under fake signers and fill the whole buffer, so an honest out-of-order tx
// from a third peer is refused. Each peer must only get a share of maxBytes.
func TestReceivedTxBufferPeerCannotStarveOthers(t *testing.T) {
	const maxBytes = testReceivedBufferMaxBytes
	buf := newReceivedTxBuffer(maxBytes, 0)

	// Each attacker uses up to maxRequestsPerPeer-1 slots, sized so that two
	// attackers together would fill the whole cap.
	junkSize := maxBytes / (2 * (maxRequestsPerPeer - 1))
	for _, attacker := range []string{"sybil1", "sybil2"} {
		for i := 0; i < maxRequestsPerPeer-1; i++ {
			junk := make([]byte, junkSize)
			copy(junk, fmt.Sprintf("%s-%d", attacker, i))
			tx := types.Tx(junk)
			fakeSigner := []byte(fmt.Sprintf("fake-%s-%d", attacker, i))
			buf.add(fakeSigner, 1, tx.ToCachedTx(), tx.Key(), mempool.TxInfo{}, attacker)
		}
		require.LessOrEqual(t, buf.bytesForPeer(attacker), int64(maxBytes/receivedBufferPeerShare))
	}

	honest := types.Tx(make([]byte, 1024))
	copy(honest, "honest")
	require.True(t,
		buf.add([]byte("honest-signer"), 7, honest.ToCachedTx(), honest.Key(), mempool.TxInfo{}, "honest-peer"),
		"an honest out-of-order tx must not be refused because two peers filled the buffer",
	)
}
