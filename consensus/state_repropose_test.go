package consensus

import (
	"context"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// A proposer re-proposing its ValidBlock must reuse the parts that received
// the polka. If the original proposer encoded the block non-canonically,
// re-encoding it yields a different PartSetHeader: validators locked on the
// original BlockID prevote it while the rest prevote the re-encoding, no
// polka forms, and the height stalls for as long as the byzantine proposer
// withholds its votes.
func TestStateReproposesValidBlockParts(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height := cs1.rs.Height
	propagator := &proposeRecordingPropagator{Propagator: cs1.propagator}
	cs1.propagator = propagator

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	newBlockCh := subscribe(cs1.eventBus, types.EventQueryNewBlockHeader)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	voteCh := subscribeToVoter(cs1, pv1.Address())

	// A byzantine proposer encoded a valid block non-canonically: the canonical
	// encoding followed by an unknown field (field 100, varint 1). The block
	// has txs so the re-proposal exercises the compact block's tx metadata.
	deliverTxsRange(t, cs1, 0, 5)
	block, canonicalParts, err := cs1.createProposalBlock(context.Background())
	require.NoError(t, err)
	pbb, err := block.ToProto()
	require.NoError(t, err)
	bz, err := proto.Marshal(pbb)
	require.NoError(t, err)
	bz = append(bz, 0xa0, 0x06, 0x01)
	var chunks [][]byte
	for len(bz) > int(types.BlockPartSizeBytes) {
		chunks = append(chunks, bz[:types.BlockPartSizeBytes])
		bz = bz[types.BlockPartSizeBytes:]
	}
	chunks = append(chunks, bz)
	root, proofs := merkle.ProofsFromByteSlices(chunks)
	header := types.PartSetHeader{Total: uint32(len(chunks)), Hash: root}
	require.NotEqual(t, canonicalParts.Header(), header)
	require.NotEmpty(t, block.Txs)
	parts := types.NewPartSetFromHeader(header, types.BlockPartSizeBytes)
	for i, c := range chunks {
		_, err := parts.AddPart(&types.Part{Index: uint32(i), Bytes: c, Proof: *proofs[i]})
		require.NoError(t, err)
	}
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: header}

	// cs1 locked the block and recorded it as ValidBlock after a polka in
	// round 0. Nodes that check the encoding before voting (PROTOCO-2791)
	// never reach this state, so it is set directly: this test covers nodes
	// without that check and keeps the re-proposal correct on its own.
	cs1.rs.LockedRound = 0
	cs1.rs.LockedBlock = block
	cs1.rs.LockedBlockParts = parts
	cs1.rs.ValidRound = 0
	cs1.rs.ValidBlock = block
	cs1.rs.ValidBlockParts = parts

	// Until cs1 is the proposer, nobody proposes. cs1 prevotes its locked
	// block, vs3 and vs4 vote nil, and vs2 withholds.
	round := int32(1)
	incrementRound(vs2, vs3, vs4)
	startTestRound(cs1, height, round)
	for {
		ensureNewRound(newRoundCh, height, round)
		if cs1.isProposer(pv1.Address()) {
			break
		}
		ensurePrevote(voteCh, height, round)
		validatePrevote(t, cs1, round, vss[0], blockID.Hash)
		signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, vs3, vs4)
		ensurePrecommit(voteCh, height, round)
		signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs3, vs4)
		round++
		incrementRound(vs2, vs3, vs4)
	}

	// cs1 re-proposes ValidBlock with the original parts, so its locked
	// prevote and the others' prevotes for the proposal form a polka.
	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	require.Equal(t, blockID, rs.Proposal.BlockID)
	require.Equal(t, int32(0), rs.Proposal.POLRound)
	// Peers validate the compact block's tx metadata; the stored parts have
	// no tx positions, so cs1 must send none rather than zero-value entries.
	proposed := propagator.proposedTxs()
	require.Len(t, proposed, 1)
	for _, md := range proposed[0] {
		require.NoError(t, md.ValidateBasic())
	}
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], blockID.Hash)
	signAddVotes(cs1, cmtproto.PrevoteType, rs.Proposal.BlockID.Hash, rs.Proposal.BlockID.PartSetHeader, false, vs3, vs4)
	ensurePrecommit(voteCh, height, round)
	validatePrecommit(t, cs1, round, round, vss[0], blockID.Hash, blockID.Hash)
	signAddVotes(cs1, cmtproto.PrecommitType, rs.Proposal.BlockID.Hash, rs.Proposal.BlockID.PartSetHeader, true, vs3, vs4)
	ensureNewBlockHeader(newBlockCh, height, blockID.Hash)
}

// proposeRecordingPropagator records the tx metadata of every block it proposes.
type proposeRecordingPropagator struct {
	propagation.Propagator
	mtx      cmtsync.Mutex
	proposed [][]proptypes.TxMetaData
}

func (r *proposeRecordingPropagator) ProposeBlock(proposal *types.Proposal, parts *types.PartSet, txs []proptypes.TxMetaData) error {
	r.mtx.Lock()
	r.proposed = append(r.proposed, txs)
	r.mtx.Unlock()
	return r.Propagator.ProposeBlock(proposal, parts, txs)
}

func (r *proposeRecordingPropagator) proposedTxs() [][]proptypes.TxMetaData {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return r.proposed
}
