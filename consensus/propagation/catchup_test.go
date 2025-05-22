package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestGapCatchup(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 3
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/tmp/test/gap_catchup")
	n1 := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(t, sm, 1, 2, 1000000)
	cb, parityBlock := createCompactBlock(t, prop, ps, metaData)

	added := n1.AddProposal(cb)
	require.True(t, added)

	_, parts, _, has := n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	parts.SetProposalData(ps, parityBlock)

	// add the partset header to the second node and trigger the call to retry
	// wants
	n2 := reactors[1]
	n3 := reactors[2]

	_, _, has = n2.GetProposal(prop.Height, prop.Round)
	require.False(t, has)
	_, _, has = n3.GetProposal(prop.Height, prop.Round)
	require.False(t, has)

	psh := ps.Header()

	// test two reactors catching up at the same time as that can increase
	// flakeyness if something is broken
	n2.AddCommitment(prop.Height, prop.Round, &psh)
	n3.AddCommitment(prop.Height, prop.Round, &psh)

	time.Sleep(800 * time.Millisecond)

	_, caughtUp, has := n2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	require.True(t, caughtUp.IsComplete())

	_, caughtUp, has = n3.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	require.True(t, caughtUp.IsComplete())
}

func defaultTestP2PConf() *cfg.P2PConfig {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 100000000
	p2pCfg.RecvRate = 100000000
	return p2pCfg
}

func createCompactBlock(
	t *testing.T,
	prop *types.Proposal,
	ps *types.PartSet,
	metaData []proptypes.TxMetaData,
) (*proptypes.CompactBlock, *types.PartSet) {
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
	return cb, parityBlock
}
