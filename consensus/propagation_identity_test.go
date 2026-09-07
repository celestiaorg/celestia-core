package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/types"
)

// backfillStubPropagator serves a fixed part set from GetProposal. Its nil
// channels keep syncData blocked on every branch except the backfill one.
type backfillStubPropagator struct {
	*propagation.NoOpPropagator
	parts *types.PartSet
}

func (s *backfillStubPropagator) GetProposal(int64, int32) (*types.Proposal, *types.PartSet, bool) {
	return nil, s.parts, true
}

// TestSyncDataBackfillChecksPartSetIdentity asserts that the syncData backfill
// only feeds consensus parts belonging to the part set it is collecting: a
// propagator bound to a different proposal identity at the same height and
// round contributes nothing.
func TestSyncDataBackfillChecksPartSetIdentity(t *testing.T) {
	cs, _ := randState(1)

	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)

	alias := aliasBlock(t, block)
	aliasParts, err := alias.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.NotEqual(t, parts.Header(), aliasParts.Header())

	cs.propagator = &backfillStubPropagator{
		NoOpPropagator: propagation.NewNoOpPropagator(),
		parts:          parts,
	}
	go cs.syncData()

	// consensus is collecting the alias part set; the propagator's complete
	// part set for the same height and round must not be fed to it.
	cs.rsMtx.Lock()
	cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(aliasParts.Header(), types.BlockPartSizeBytes)
	cs.rsMtx.Unlock()
	cs.newHeightOrRoundChan <- struct{}{}
	select {
	case mi := <-cs.internalMsgQueue:
		t.Fatalf("part from a different part set fed to consensus: %+v", mi)
	case <-time.After(250 * time.Millisecond):
	}

	// with a matching header the same part set is backfilled in full.
	cs.rsMtx.Lock()
	cs.rs.ProposalBlockParts = types.NewPartSetFromHeader(parts.Header(), types.BlockPartSizeBytes)
	cs.rsMtx.Unlock()
	cs.newHeightOrRoundChan <- struct{}{}
	for i := 0; i < int(parts.Total()); i++ {
		select {
		case mi := <-cs.internalMsgQueue:
			partMsg, ok := mi.Msg.(*BlockPartMessage)
			require.True(t, ok)
			require.NoError(t, partMsg.Part.Proof.Verify(parts.Header().Hash, partMsg.Part.Bytes))
		case <-time.After(2 * time.Second):
			t.Fatal("matching part set was not backfilled")
		}
	}
}
