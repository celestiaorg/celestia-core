package blocksync

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func oversizedSyncBlock(t *testing.T) (*types.Block, []byte) {
	t.Helper()
	genesis, _ := randGenesisDoc(1, false, 10)
	state, err := sm.MakeGenesisState(genesis)
	require.NoError(t, err)
	block, _, err := state.MakeBlock(1, types.MakeData(make(types.Txs, types.MaxBlockTxs+1)),
		&types.Commit{}, nil, state.Validators.Proposer.Address)
	require.NoError(t, err)
	// A forged legacy version must not authorize transaction decoding.
	block.Version.App = 10
	pb, err := block.ToProto()
	require.NoError(t, err)
	return block, mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{Block: pb}))
}

func TestPrefetchedTxsDeferredAcrossActivation(t *testing.T) {
	original, wire := oversizedSyncBlock(t)
	channel := (&Reactor{}).GetChannels()[0]
	// Exercise the same clone/unmarshal/unwrap path as BaseReactor, while the
	// response's applicable app version is still unknown.
	message := proto.Clone(channel.MessageType)
	require.NoError(t, proto.Unmarshal(wire, message))
	unwrapped, err := message.(p2p.Unwrapper).Unwrap()
	require.NoError(t, err)
	response := unwrapped.(*deferredResponse)
	require.Empty(t, response.Block.Data.Txs, "prefetch must not allocate tx entries")
	lookahead, err := types.BlockFromProto(response.Block)
	require.NoError(t, err)
	require.Equal(t, original.Header, lookahead.Header)
	require.Equal(t, original.LastCommit.ToProto(), lookahead.LastCommit.ToProto())

	// On reaching the front of the pool, the applied state now reports v11.
	// Even though this response arrived under v10 and claims v10, reject it
	// before the full decoder allocates any transaction entries.
	decoded, err := decodeDeferredBlock(response.raw, 11)
	require.ErrorIs(t, err, errTooManyTxs)
	require.Nil(t, decoded)

	// The v10 compatibility ceiling is deliberately much larger than the v11
	// protocol limit, so this response retains its original validity.
	decoded, err = decodeDeferredBlock(response.raw, 10)
	require.NoError(t, err)
	require.Len(t, decoded.Txs, len(original.Txs))
	require.Equal(t, original.Hash(), decoded.Hash())
}

func TestDeferredDecodeV10SafetyLimit(t *testing.T) {
	wire := blockResponseWithTxs(t, types.MaxBlockTxsV10+1, nil)
	var message syncMessage
	require.NoError(t, message.Unmarshal(wire))
	response, err := message.Unwrap()
	require.NoError(t, err)
	deferred := response.(*deferredResponse)
	require.Empty(t, deferred.Block.Data.Txs)

	decoded, err := decodeDeferredBlock(deferred.raw, 10)
	require.ErrorIs(t, err, errTooManyTxs)
	require.Nil(t, decoded)
}

func TestDeferredResponsePoolSnapshotAndRetry(t *testing.T) {
	original, wire := oversizedSyncBlock(t)
	pool := NewBlockPool(1, nil, nil)
	requester := newBPRequester(pool, 1)
	requester.requestedFromPeers["peer"] = struct{}{}
	pool.requesters[1] = requester
	ok, err := requester.setBlock(original, nil, "peer", wire)
	require.NoError(t, err)
	require.True(t, ok)
	first, _, _, raw := pool.peekTwoBlockResponses()
	require.Same(t, original, first)
	require.Equal(t, wire, raw)

	requester.mtx.Lock()
	require.True(t, requester.tryRemoveBlock("peer"))
	requester.mtx.Unlock()
	first, _, _, cleared := pool.peekTwoBlockResponses()
	require.Nil(t, first)
	require.Empty(t, cleared, "retry must release the old response")
	_, err = decodeDeferredBlock(raw, 11)
	require.ErrorIs(t, err, errTooManyTxs, "a prior snapshot retains its bound")
}

func TestSyncMessageNormalAndDuplicateFields(t *testing.T) {
	for _, count := range []int{0, types.MaxBlockTxs, types.MaxBlockTxs + 1, 1_000_000} {
		wire := blockResponseWithTxs(t, count, nil)
		var message syncMessage
		require.NoError(t, message.Unmarshal(wire))
		response, err := message.Unwrap()
		require.NoError(t, err)
		if count <= types.MaxBlockTxs {
			require.Len(t, response.(*bcproto.BlockResponse).Block.Data.Txs, count)
		} else {
			require.Empty(t, response.(*deferredResponse).Block.Data.Txs)
		}
	}
	data := mustMarshal(t, &cmtproto.Data{Txs: make([][]byte, 501)})
	for _, wire := range [][]byte{
		wireField(3, wireField(1, wireField(2, data, data))),
		wireField(3, wireField(1, wireField(2, data), wireField(2, data))),
		wireField(3, wireField(1, wireField(2, data)), wireField(1, wireField(2, data))),
	} {
		var message syncMessage
		require.NoError(t, message.Unmarshal(wire))
		response, err := message.Unwrap()
		require.NoError(t, err)
		deferred := response.(*deferredResponse)
		require.Empty(t, deferred.Block.Data.Txs)
		_, err = decodeDeferredBlock(deferred.raw, 11)
		require.ErrorIs(t, err, errTooManyTxs)
	}
}

func TestDeferredDecodeAllocationBound(t *testing.T) {
	// Repeating singular Data fields must not create an allocation per tx.
	data := wireField(2, []byte{0x0a, 0})
	wire := wireField(3, wireField(1, bytes.Repeat(data, 100_000)))
	allocs := testing.AllocsPerRun(5, func() {
		var message syncMessage
		if err := message.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if len(message.message.GetBlockResponse().Block.Data.Txs) != 0 {
			t.Fatal("transactions allocated during prefetch")
		}
	})
	require.Less(t, allocs, float64(50))
	allocs = testing.AllocsPerRun(5, func() {
		_, err := decodeDeferredBlock(wire, 11)
		if !errors.Is(err, errTooManyTxs) {
			t.Fatal(err)
		}
	})
	require.Less(t, allocs, float64(50))
}
