package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

func TestInvalidHavePartHash(t *testing.T) {
	p2pCfg := cfg.DefaultP2PConfig()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")
	r1, r2 := reactors[0], reactors[1]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	prop, ps, _, metaData := createTestProposal(sm, 1, 2, 1000000)
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

	added := r1.AddProposal(cb)
	require.True(t, added)

	// make sure the peers are connected
	p1 := r1.getPeer(r2.self)
	require.NotNil(t, p1)

	// send a valid have
	haves := &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts:  []proptypes.PartMetaData{{Index: 0, Hash: partHashes[0]}},
	}
	r1.handleHaves(r2.self, haves)
	time.Sleep(100 * time.Millisecond)

	// make sure r1 processed the have and is still connected
	p1 = r1.getPeer(r2.self)
	require.NotNil(t, p1)
	p1Haves, has := p1.GetHaves(prop.Height, prop.Round)
	require.True(t, has)
	assert.True(t, p1Haves.GetIndex(0))

	// send an invalid have
	haves = &proptypes.HaveParts{
		Height: prop.Height,
		Round:  prop.Round,
		Parts:  []proptypes.PartMetaData{{Index: 1, Hash: []byte{0x01}}},
	}
	r1.handleHaves(r2.self, haves)
	time.Sleep(100 * time.Millisecond)

	// make sure r1 disconnected from r2
	p1 = r1.getPeer(r2.self)
	assert.Nil(t, p1)
}
