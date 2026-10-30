package consensus

import (
	"bytes"
	"context"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

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
			if version == 10 {
				count = types.MaxBlockTxsCeiling + 1
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
			if count > limit {
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

// A malformed tail after the txs must not let the full decoder allocate them.
func TestProposalTxCountRejectsMalformedTail(t *testing.T) {
	pb := &cmtproto.Block{Data: cmtproto.Data{Txs: make([][]byte, types.MaxBlockTxs+1)}}
	wire, err := pb.Marshal()
	require.NoError(t, err)
	wire = append(wire, 0x12) // Block.data tag with a missing length
	require.Error(t, cmtproto.CheckBlockCardinality(wire, types.MaxBlockTxs, true))
	require.Error(t, proto.Unmarshal(wire, new(cmtproto.Block)), "the full decoder rejects it too")
}

// Unmarshal keeps only the last Evidence oneof value, so before v11 a replaced
// light client attack must not count; from v11 it does.
func TestProposalEvidenceOneofCountedFromV11(t *testing.T) {
	field := func(num protowire.Number, payload []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), payload)
	}
	junk := field(2, bytes.Repeat([]byte{0x1a, 0}, cmtproto.MaxBlockEvidenceElements+1))
	evidence := field(3, field(1, append(junk, field(1, nil)...)))

	for _, version := range []uint64{10, 11} {
		cs, _ := randState(4)
		cs.state.Version.Consensus.App = version
		block, _, err := cs.createProposalBlock(context.Background())
		require.NoError(t, err)
		pb, err := block.ToProto()
		require.NoError(t, err)
		wire, err := pb.Marshal()
		require.NoError(t, err)
		parts, err := types.NewPartSetFromData(append(wire, evidence...), types.BlockPartSizeBytes)
		require.NoError(t, err)
		cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(parts.Header(), types.BlockPartSizeBytes)
		for i := range parts.Total() {
			_, err = cs.addProposalBlockPart(&BlockPartMessage{
				Height: cs.rs.Height, Round: cs.rs.Round, Part: parts.GetPart(int(i)),
			}, "peer")
		}
		if version >= types.BlockTxLimitAppVersion {
			require.ErrorIs(t, err, cmtproto.ErrTooManyBlockEvidenceElements)
		} else {
			require.NotErrorIs(t, err, cmtproto.ErrTooManyBlockEvidenceElements)
		}
	}
}
