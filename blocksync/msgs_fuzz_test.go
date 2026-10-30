package blocksync

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/p2p"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmttypes "github.com/cometbft/cometbft/types"
)

// FuzzBlockSyncMessage includes the signature-count precheck that runs on raw
// peer bytes before the full decoder, then follows the reactor's unwrap path.
func FuzzBlockSyncMessage(f *testing.F) {
	commit := &cmttypes.Commit{
		Height: 1,
		BlockID: cmttypes.BlockID{
			Hash:          bytes.Repeat([]byte{1}, 32),
			PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{2}, 32)},
		},
		Signatures: []cmttypes.CommitSig{cmttypes.NewCommitSigAbsent()},
	}
	block := cmttypes.MakeBlock(2, cmttypes.Data{Txs: cmttypes.Txs{cmttypes.Tx("key=value")}}, commit, nil)
	block.ValidatorsHash = bytes.Repeat([]byte{1}, 32)
	block.NextValidatorsHash = bytes.Repeat([]byte{2}, 32)
	block.ProposerAddress = bytes.Repeat([]byte{3}, 20)
	require.NoError(f, block.ValidateBasic())
	pbBlock, err := block.ToProto()
	require.NoError(f, err)
	for _, seed := range []p2p.Wrapper{
		&bcproto.BlockRequest{Height: 1},
		&bcproto.NoBlockResponse{Height: 1},
		&bcproto.BlockResponse{Block: pbBlock, ExtCommit: commit.WrappedExtendedCommit().ToProto()},
		&bcproto.StatusRequest{},
		&bcproto.StatusResponse{Height: 10, Base: 1},
	} {
		raw, err := proto.Marshal(seed.Wrap())
		require.NoError(f, err)
		require.NoError(f, validateBlockSyncBytes(raw))
		require.NoError(f, ValidateMsg(seed.(proto.Message)))
		var decoded bcproto.Message
		require.NoError(f, proto.Unmarshal(raw, &decoded))
		inner, err := decoded.Unwrap()
		require.NoError(f, err)
		if response, ok := inner.(*bcproto.BlockResponse); ok {
			_, err = cmttypes.BlockFromProto(response.Block)
			require.NoError(f, err)
			_, err = cmttypes.ExtendedCommitFromProto(response.ExtCommit)
			require.NoError(f, err)
		}
		f.Add(raw)
	}
	f.Add([]byte{})
	f.Add([]byte{0xff})
	// A BlockResponse with absent nested pointers must also reach conversion.
	raw, err := proto.Marshal((&bcproto.BlockResponse{}).Wrap())
	require.NoError(f, err)
	f.Add(raw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		// Bound per-worker memory and time; this is smaller than the network's
		// block-size ceiling and is not intended to test maximum-size blocks.
		if len(raw) > 1<<20 {
			return
		}
		if err := validateBlockSyncBytes(raw); err != nil {
			return
		}
		var envelope bcproto.Message
		if err := proto.Unmarshal(raw, &envelope); err != nil {
			return
		}
		msg, err := envelope.Unwrap()
		if err != nil {
			return
		}
		if err := ValidateMsg(msg); err != nil {
			return
		}
		if response, ok := msg.(*bcproto.BlockResponse); ok {
			// ValidateMsg deliberately leaves block conversion to the reactor.
			_, _ = cmttypes.BlockFromProto(response.Block)
			if response.ExtCommit != nil {
				_, _ = cmttypes.ExtendedCommitFromProto(response.ExtCommit)
			}
		}
		encoded, err := proto.Marshal(msg.(p2p.Wrapper).Wrap())
		require.NoError(t, err)
		require.NoError(t, validateBlockSyncBytes(encoded))
		var decoded bcproto.Message
		require.NoError(t, proto.Unmarshal(encoded, &decoded))
		back, err := decoded.Unwrap()
		require.NoError(t, err)
		require.NoError(t, ValidateMsg(back))
		require.True(t, proto.Equal(msg, back), "message changed across proto round trip")
	})
}
