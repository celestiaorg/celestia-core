package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

func TestGapCatchup(t *testing.T) {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 100000000
	p2pCfg.RecvRate = 100000000
	nodes := 2

	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/home/evan/data/experiments/celestia/fast-recovery/debug")
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)

	// set the commitment and the data on the first node so that it can respond
	// to the catchup request
	n1 := reactors[0]
	parityBlock, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)

	partHashes := extractHashes(ps, parityBlock)
	proofs := extractProofs(ps, parityBlock)
	cb := &proptypes.CompactBlock{
		Proposal:    *prop,
		LastLen:     uint32(lastLen),
		Signature:   cmtrand.Bytes(64), // todo: sign the proposal with a real signature
		BpHash:      parityBlock.Hash(),
		Blobs:       metaData,
		PartsHashes: partHashes,
	}
	cb.SetProofCache(proofs)
	added := n1.AddProposal(cb)
	require.True(t, added)

	_, parts, _, has := n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)
	parts.SetProposalData(ps, parityBlock)

	n2 := reactors[1]

	t.Run("catchup with a peer that sent us haves", func(t *testing.T) {
		n2.handleCompactBlock(cb, n1.self, false)
		n2.requestManager.consensusHeight = prop.Height
		n2.requestManager.consensusRound = prop.Round
		firstPart, has := parts.GetPart(0)
		require.True(t, has)
		n2.handleHaves(n1.self, &proptypes.HaveParts{
			Height: prop.Height,
			Round:  prop.Round,
			Parts: []proptypes.PartMetaData{
				{
					Index: firstPart.Index,
					Hash:  firstPart.Proof.LeafHash,
				}},
		})
		haves, has := n2.peerstate[n1.self].GetReceivedHaves(prop.Height, prop.Round)
		require.True(t, has)
		require.True(t, haves.GetIndex(int(firstPart.Index)))

		time.Sleep(1000 * time.Millisecond)

		_, partSet, has := n2.GetProposal(prop.Height, prop.Round)
		require.True(t, has)
		require.NotNil(t, partSet.GetPart(int(firstPart.Index)))
	})

	t.Run("catchup with a peer at a higher height", func(t *testing.T) {

	})

	//_, _, has = n2.GetProposal(prop.Height, prop.Round)
	//require.False(t, has)

	//psh := ps.Header()
	//n2.AddCommitment(prop.Height, prop.Round, &psh)

	time.Sleep(80000 * time.Millisecond)

	//_, caughtUp, has := n2.GetProposal(prop.Height, prop.Round)
	//require.True(t, has)
	//require.True(t, caughtUp.IsComplete())
}
