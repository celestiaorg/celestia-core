package propagation

import (
	"github.com/tendermint/tendermint/libs/bits"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

// TestSameHeightCatchup catchup with a peer that sent us haves and is still at the same height as us
func TestSameHeightCatchup(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "./same-height-catchup")
	r1, r2 := reactors[0], reactors[1]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
	cb, parityBlock := createCompactBlock(t, prop, ps, metaData)

	// set reactor 1 state
	added := r1.AddProposal(cb)
	require.True(t, added)
	_, parts, _, has := r1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)
	parts.SetProposalData(ps, parityBlock)

	// set reactor 2 state
	added = r2.AddProposal(cb)
	require.True(t, added)
	_, _, _, has = r2.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	// set reactor 2 haves from reactor 1
	firstPart, has := parts.GetPart(0)
	require.True(t, has)
	haves := bits.NewBitArray(int(parts.Total()))
	haves.SetIndex(int(firstPart.Index), true)
	r2.peerstate[r1.self].initialize(prop.Height, prop.Round, int(ps.Total()))
	r2.peerstate[r1.self].state[prop.Height][prop.Round].receivedHaves = haves

	// set the peer to the same height
	r2.peerstate[r1.self].latestHeight = 1

	// create next height
	prop2, ps2, _, metaData2 := createTestProposal(sm, 2, 2, 1000000)
	cb2, _ := createCompactBlock(t, prop2, ps2, metaData2)
	r2.AddCommitment(2, 0, &cb2.Proposal.BlockID.PartSetHeader)

	time.Sleep(500 * time.Millisecond)

	_, partSet, has := r2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	require.NotNil(t, partSet.GetPart(int(firstPart.Index)))
}

// TestHigherHeightCatchup catchup with a peer that sent us haves while being on a higher height
func TestHigherHeightCatchup(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "./same-height-catchup")
	r1, r2 := reactors[0], reactors[1]
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
	cb, parityBlock := createCompactBlock(t, prop, ps, metaData)

	// set reactor 1 state
	added := r1.AddProposal(cb)
	require.True(t, added)
	_, parts, _, has := r1.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)
	parts.SetProposalData(ps, parityBlock)

	// set reactor 2 state
	added = r2.AddProposal(cb)
	require.True(t, added)
	_, _, _, has = r2.getAllState(prop.Height, prop.Round, true)
	require.True(t, has)

	// set reactor 2 haves from reactor 1
	firstPart, has := parts.GetPart(0)
	require.True(t, has)
	haves := bits.NewBitArray(int(parts.Total()))
	haves.SetIndex(int(firstPart.Index), true)
	r2.peerstate[r1.self].initialize(prop.Height, prop.Round, int(ps.Total()))
	r2.peerstate[r1.self].state[prop.Height][prop.Round].receivedHaves = haves

	// set the peer to a higher height
	r2.peerstate[r1.self].latestHeight = 2

	// create next height
	prop2, ps2, _, metaData2 := createTestProposal(sm, 2, 2, 1000000)
	cb2, _ := createCompactBlock(t, prop2, ps2, metaData2)
	r2.AddCommitment(2, 0, &cb2.Proposal.BlockID.PartSetHeader)

	time.Sleep(500 * time.Millisecond)

	_, partSet, has := r2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	require.NotNil(t, partSet.GetPart(int(firstPart.Index)))
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
