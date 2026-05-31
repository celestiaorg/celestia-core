package cat

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

func largeCATTestTx(size int) types.Tx {
	prefix := []byte("sender-000-0=")
	suffix := []byte("=1")
	if size < len(prefix)+len(suffix)+1 {
		size = len(prefix) + len(suffix) + 1
	}
	tx := make([]byte, 0, size)
	tx = append(tx, prefix...)
	tx = append(tx, bytes.Repeat([]byte{'A'}, size-len(prefix)-len(suffix))...)
	tx = append(tx, suffix...)
	return types.Tx(tx)
}

func setupLargeTxReactor(t *testing.T, threshold, chunkSize int) (*Reactor, *TxPool) {
	t.Helper()
	reactor, pool := setupReactor(t)
	reactor.opts.LargeTxThreshold = threshold
	reactor.opts.LargeTxChunkSize = chunkSize
	reactor.opts.LargeTxRequestParallelism = 2
	reactor.opts.LargeTxMaxInflightChunksPerPeer = 2
	reactor.opts.LargeTxChunkTimeout = time.Hour
	reactor.opts.LargeTxReconstructionTimeout = time.Hour
	reactor.opts.LargeTxMaxAdvertisePeers = 15
	reactor.opts.LargeTxOptimisticPushChunks = 2
	t.Cleanup(reactor.stopLargeTxReconstructionSessions)
	return reactor, pool
}

func TestLargeTxManifestValidation(t *testing.T) {
	reactor, _ := setupLargeTxReactor(t, 1, 16)

	tx := largeCATTestTx(96)
	local, err := buildLocalLargeTx(tx, 16, []byte("sender-000-0"), 1, 10)
	require.NoError(t, err)

	key, err := reactor.validateTxManifest(local.manifest)
	require.NoError(t, err)
	require.Equal(t, tx.Key(), key)

	invalid := cloneTxManifest(local.manifest)
	invalid.ChunkHashes[0] = invalid.ChunkHashes[0][:8]
	_, err = reactor.validateTxManifest(invalid)
	require.Error(t, err)
	require.ErrorIs(t, err, errInvalidTxManifest)
}

func TestLargeTxReconstructionSuccessDuplicateAndCorruptChunk(t *testing.T) {
	reactor, _ := setupLargeTxReactor(t, 1, 16)

	tx := largeCATTestTx(96)
	local, err := buildLocalLargeTx(tx, 16, []byte("sender-000-0"), 1, 10)
	require.NoError(t, err)
	txKey := tx.Key()

	created, err := reactor.upsertReconstructionSession(txKey, local.manifest, 1, false)
	require.NoError(t, err)
	require.True(t, created)

	corrupt := append([]byte(nil), local.chunks[0]...)
	corrupt[0] ^= 0xff
	reconstructed, err := reactor.acceptTxChunk(txKey, &protomem.TxChunk{
		TxKey: txKey[:],
		Index: 0,
		Data:  corrupt,
	}, 1)
	require.Nil(t, reconstructed)
	require.Error(t, err)
	require.ErrorIs(t, err, errInvalidTxChunk)

	reconstructed, err = reactor.acceptTxChunk(txKey, &protomem.TxChunk{
		TxKey: txKey[:],
		Index: 0,
		Data:  local.chunks[0],
	}, 1)
	require.NoError(t, err)
	require.Nil(t, reconstructed)

	reconstructed, err = reactor.acceptTxChunk(txKey, &protomem.TxChunk{
		TxKey: txKey[:],
		Index: 0,
		Data:  local.chunks[0],
	}, 1)
	require.NoError(t, err)
	require.Nil(t, reconstructed, "duplicate chunks should be ignored until reconstruction completes")

	for i := 1; i < len(local.chunks); i++ {
		reconstructed, err = reactor.acceptTxChunk(txKey, &protomem.TxChunk{
			TxKey: txKey[:],
			Index: uint32(i),
			Data:  local.chunks[i],
		}, 1)
		require.NoError(t, err)
	}
	require.Equal(t, tx, reconstructed)

	reactor.largeMu.Lock()
	_, exists := reactor.reconstructions[txKey]
	reactor.largeMu.Unlock()
	require.False(t, exists)
}

func TestLargeTxSchedulerRequestsDisjointChunksFromPeers(t *testing.T) {
	reactor, _ := setupLargeTxReactor(t, 1, 16)

	tx := largeCATTestTx(96)
	local, err := buildLocalLargeTx(tx, 16, []byte("sender-000-0"), 1, 10)
	require.NoError(t, err)
	txKey := tx.Key()

	peers := genPeers(2)
	for _, peer := range peers {
		_, err := reactor.InitPeer(peer)
		require.NoError(t, err)
	}
	peerA := reactor.ids.GetIDForPeer(peers[0].ID())
	peerB := reactor.ids.GetIDForPeer(peers[1].ID())

	_, err = reactor.upsertReconstructionSession(txKey, local.manifest, peerA, false)
	require.NoError(t, err)
	_, err = reactor.upsertReconstructionSession(txKey, local.manifest, peerB, false)
	require.NoError(t, err)

	peers[0].On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantChunk{WantChunk: &protomem.WantChunk{
				TxKey:   txKey[:],
				Indexes: []uint32{0, 1},
			}},
		},
	}).Return(true).Once()
	peers[1].On("TrySend", p2p.Envelope{
		ChannelID: MempoolWantsChannel,
		Message: &protomem.Message{
			Sum: &protomem.Message_WantChunk{WantChunk: &protomem.WantChunk{
				TxKey:   txKey[:],
				Indexes: []uint32{2, 3},
			}},
		},
	}).Return(true).Once()

	reactor.scheduleChunkRequests(txKey)

	peers[0].AssertExpectations(t)
	peers[1].AssertExpectations(t)
}

func TestLargeTxBroadcastUsesManifestByDefault(t *testing.T) {
	reactor, pool := setupLargeTxReactor(t, 32, 16)

	peer := genPeer()
	_, err := reactor.InitPeer(peer)
	require.NoError(t, err)

	tx := largeCATTestTx(96)
	txKey := tx.Key()
	require.NoError(t, pool.CheckTx(tx, nil, mempool.TxInfo{}))
	wtx := pool.store.get(txKey)
	require.NotNil(t, wtx)

	peer.On("Send", mock.MatchedBy(func(env p2p.Envelope) bool {
		msg, ok := env.Message.(*protomem.Message)
		return ok &&
			env.ChannelID == MempoolDataChannel &&
			msg.GetTxManifest() != nil &&
			bytes.Equal(msg.GetTxManifest().TxKey, txKey[:])
	})).Return(true).Once()
	peer.On("TrySend", mock.MatchedBy(func(env p2p.Envelope) bool {
		msg, ok := env.Message.(*protomem.Message)
		if !ok || env.ChannelID != MempoolChunkChannel || msg.GetTxChunk() == nil {
			return false
		}
		index := msg.GetTxChunk().Index
		return index == 0 || index == 1
	})).Return(true).Twice()

	reactor.broadcastNewTx(wtx)

	peer.AssertExpectations(t)
	require.True(t, pool.seenByPeersSet.Has(txKey, reactor.ids.GetIDForPeer(peer.ID())))

	reactor.largeMu.Lock()
	_, exists := reactor.largeTxs[txKey]
	reactor.largeMu.Unlock()
	require.True(t, exists)
}

func TestLargeTxFastPathDefaultsEnabled(t *testing.T) {
	opts := ReactorOptions{}
	require.NoError(t, opts.VerifyAndComplete())
	require.Positive(t, opts.LargeTxThreshold)
	require.Positive(t, opts.LargeTxChunkSize)
	require.Positive(t, opts.LargeTxRequestParallelism)
	require.Positive(t, opts.LargeTxMaxInflightChunksPerPeer)
	require.Positive(t, opts.LargeTxChunkTimeout)
	require.Positive(t, opts.LargeTxReconstructionTimeout)
	require.Positive(t, opts.LargeTxMaxAdvertisePeers)
	require.Positive(t, opts.LargeTxOptimisticPushChunks)
	require.Positive(t, opts.LargeTxPeerScoreHalflife)
}

func TestPeerScoreUpdates(t *testing.T) {
	scores := newPeerScoreTable(time.Minute)
	scores.RecordChunk(1, 1024, time.Millisecond)
	require.Greater(t, scores.Score(1), 0.0)

	scores.RecordTimeout(1)
	require.Greater(t, scores.Score(1), 0.0)

	scores.RecordInvalidChunk(1)
	require.Less(t, scores.Score(1), 0.0)

	scores.RecordSendFailure(2)
	require.Less(t, scores.Score(2), 0.0)
}
