package consensus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestProposalTxLimitActivationAndRecovery(t *testing.T) {
	for _, version := range []uint64{9, 10, 11} {
		for _, tx := range [][]byte{nil, {1}} {
			cs, _ := randState(4)
			cs.state.Version.Consensus.App = version
			block, honestParts, err := cs.createProposalBlock(context.Background())
			require.NoError(t, err)
			pb, err := block.ToProto()
			require.NoError(t, err)
			// A forged legacy header must not switch off the v11 bound.
			pb.Header.Version.App = 10
			limit := types.MaxBlockTxsForAppVersion(version)
			count := types.MaxBlockTxs + 1
			if version == types.BlockTxSafetyLimitAppVersion {
				count = types.MaxBlockTxsV10 + 1
			}
			pb.Data.Txs = make([][]byte, count)
			for i := range pb.Data.Txs {
				pb.Data.Txs[i] = tx
			}
			pb.Data.Hash = pb.Header.DataHash
			wire, err := pb.Marshal()
			require.NoError(t, err)
			parts, err := types.NewPartSetFromData(wire, types.BlockPartSizeBytes)
			require.NoError(t, err)
			cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(parts.Header(), types.BlockPartSizeBytes)
			for i := range parts.Total() {
				_, err = cs.addProposalBlockPart(&BlockPartMessage{
					Height: cs.rs.Height, Round: cs.rs.Round, Part: parts.GetPart(int(i)),
				}, "peer")
			}
			if limit > 0 && count > limit {
				require.ErrorIs(t, err, cmtproto.ErrTooManyBlockTxs)
				require.Nil(t, cs.rs.ProposalBlock)
			} else {
				require.NoError(t, err, "legacy proposal decoding is unchanged")
				require.Len(t, cs.rs.ProposalBlock.Txs, count)
			}

			// Receiving another proposal's part set still works after rejection.
			cs.rs.ProposalBlock = nil
			cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(honestParts.Header(), types.BlockPartSizeBytes)
			for i := range honestParts.Total() {
				_, err := cs.addProposalBlockPart(&BlockPartMessage{
					Height: cs.rs.Height, Round: cs.rs.Round, Part: honestParts.GetPart(int(i)),
				}, "honest-peer")
				require.NoError(t, err)
			}
			require.NotNil(t, cs.rs.ProposalBlock)
		}
	}
}
