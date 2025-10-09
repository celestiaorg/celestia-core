package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

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

	prop, ps, _, metaData := createTestProposal(t, sm, 1, 0, 2, 1000000)
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
	// flakiness if something is broken
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

func TestReceiveHaveOnCatchupBlock(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	n1 := reactors[0]
	n2 := reactors[1]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(t, sm, 1, 0, 2, 1000000)
	cb, _ := createCompactBlock(t, prop, ps, metaData)

	n1.AddCommitment(prop.Height, prop.Round, &cb.Proposal.BlockID.PartSetHeader)
	_, _, _, has := n1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	assert.NotNil(t, n1.getPeer(n2.self))
	// receive a have for this block added via catchup
	n1.handleHaves(n2.self, &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts: []proptypes.PartMetaData{
			{
				Index: 0,
				Hash:  cb.PartsHashes[0],
			},
		},
	})

	// make sure the error didn't happen, and we didn't disconnect from the peer
	assert.NotNil(t, n1.getPeer(n2.self))
}

func TestAddCommitment_ReplaceProposalData(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 1
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/tmp/test/add_commitment_replace_proposal_data")
	r1 := reactors[0]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	firstProposal, firstPartset, _, _ := createTestProposal(t, sm, 1, 0, 2, 1000000)
	firstPsh := firstPartset.Header()

	// set the first partset header
	r1.AddCommitment(firstProposal.Height, firstProposal.Round, &firstPsh)
	actualFirstPsh := r1.proposals[firstProposal.Height][firstProposal.Round].block.Original().Header()
	require.Equal(t, firstPartset.Total(), actualFirstPsh.Total)
	require.Equal(t, firstPsh.Hash, actualFirstPsh.Hash)

	// replace the existing partset header with a new one
	secondProposal, secondPartset, _, _ := createTestProposal(t, sm, 1, 0, 10, 1000000)
	secondPsh := secondPartset.Header()
	r1.AddCommitment(secondProposal.Height, secondProposal.Round, &secondPsh)

	// verify if the partset header got updated
	actualSecondPsh := r1.proposals[secondProposal.Height][secondProposal.Round].block.Original().Header()
	assert.Equal(t, secondPartset.Total(), actualSecondPsh.Total)
	assert.Equal(t, secondPsh.Hash, actualSecondPsh.Hash)
}

func defaultTestP2PConf() *cfg.P2PConfig {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 100000000
	p2pCfg.RecvRate = 100000000
	return p2pCfg
}

func createCompactBlock(
	t testing.TB,
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
