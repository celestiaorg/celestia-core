package types

import (
	"context"
	"testing"

	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/p2p/ipld"
)

func TestRowSet(t *testing.T) {
	edsIn := ipld.RandEDS(t, 32)

	rsIn := NewRowSet(edsIn)
	assert.True(t, rsIn.Count() == rsIn.Total())
	assert.True(t, rsIn.IsComplete())

	edsOut, err := rsIn.Square()
	require.NoError(t, err)
	assert.True(t, rsIn.HasHeader(ipld.MakeDataHeader(edsOut)))
	assert.True(t, ipld.EqualEDS(edsIn, edsOut))

	rsOut := NewRowSetFromHeader(rsIn.DAHeader)
	for i := 0; i < rsIn.Total(); i++ {
		added, err := rsOut.AddRow(rsIn.GetRow(i))
		require.NoError(t, err)
		assert.True(t, added)
	}

	edsOut, err = rsOut.Square()
	require.NoError(t, err)
	assert.True(t, rsIn.HasHeader(ipld.MakeDataHeader(edsOut)))
	assert.True(t, ipld.EqualEDS(edsIn, edsOut))
}

func TestRowSetEmptyBlock(t *testing.T) {
	shares, _ := (&Data{}).ComputeShares()

	edsIn, err := ipld.PutData(context.TODO(), shares, mdutils.Mock())
	require.NoError(t, err)

	rsIn := NewRowSet(edsIn)
	assert.True(t, rsIn.Count() == rsIn.Total())
	assert.True(t, rsIn.IsComplete())

	rsOut := NewRowSetFromHeader(rsIn.DAHeader)
	for i := 0; i < rsIn.Total(); i++ {
		added, err := rsOut.AddRow(rsIn.GetRow(i))
		require.NoError(t, err)
		assert.True(t, added)
	}

	edsOut, err := rsOut.Square()
	require.NoError(t, err)
	assert.True(t, rsIn.HasHeader(ipld.MakeDataHeader(edsOut)))
	assert.True(t, ipld.EqualEDS(edsIn, edsOut))
}
