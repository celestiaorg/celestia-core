package consensus

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	abcimocks "github.com/cometbft/cometbft/abci/types/mocks"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/test"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/protoio"
	cmtpubsub "github.com/cometbft/cometbft/libs/pubsub"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	p2pmock "github.com/cometbft/cometbft/p2p/mock"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

/*

ProposeSuite
x * TestProposerSelection0 - round robin ordering, round 0
x * TestProposerSelection2 - round robin ordering, round 2++
x * TestEnterProposeNoValidator - timeout into prevote round
x * TestEnterPropose - finish propose without timing out (we have the proposal)
x * TestBadProposal - 2 vals, bad proposal (bad block state hash), should prevote and precommit nil
x * TestOversizedBlock - block with too many txs should be rejected
FullRoundSuite
x * TestFullRound1 - 1 val, full successful round
x * TestFullRoundNil - 1 val, full round of nil
x * TestFullRound2 - 2 vals, both required for full round
LockSuite
x * TestLockNoPOL - 2 vals, 4 rounds. one val locked, precommits nil every round except first.
x * TestLockPOLRelock - 4 vals, one precommits, other 3 polka at next round, so we unlock and precomit the polka
x * TestLockPOLUnlock - 4 vals, one precommits, other 3 polka nil at next round, so we unlock and precomit nil
x * TestLockPOLSafety1 - 4 vals. We shouldn't change lock based on polka at earlier round
x * TestLockPOLSafety2 - 4 vals. After unlocking, we shouldn't relock based on polka at earlier round
  * TestNetworkLock - once +1/3 precommits, network should be locked
  * TestNetworkLockPOL - once +1/3 precommits, the block with more recent polka is committed
SlashingSuite
x * TestSlashingPrevotes - a validator prevoting twice in a round gets slashed
x * TestSlashingPrecommits - a validator precomitting twice in a round gets slashed
CatchupSuite
  * TestCatchup - if we might be behind and we've seen any 2/3 prevotes, round skip to new round, precommit, or prevote
HaltSuite
x * TestHalt1 - if we see +2/3 precommits after timing out into new round, we should still commit

*/

//----------------------------------------------------------------------------------------------------
// ProposeSuite

func TestStateProposerSelection0(t *testing.T) {
	cs1, vss := randState(4)
	height, round := cs1.Height, cs1.Round

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)

	startTestRound(cs1, height, round)

	// Wait for new round so proposer is set.
	ensureNewRound(newRoundCh, height, round)

	// Commit a block and ensure proposer for the next height is correct.
	prop := cs1.GetRoundState().Validators.GetProposer()
	pv, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	address := pv.Address()
	if !bytes.Equal(prop.Address, address) {
		t.Fatalf("expected proposer to be validator %d. Got %X", 0, prop.Address)
	}

	// Wait for complete proposal.
	ensureNewProposal(proposalCh, height, round)

	rs := cs1.GetRoundState()
	signAddVotes(cs1, cmtproto.PrecommitType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), true, vss[1:]...)

	// Wait for new round so next validator is set.
	ensureNewRound(newRoundCh, height+1, 0)

	prop = cs1.GetRoundState().Validators.GetProposer()
	pv1, err := vss[1].GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	if !bytes.Equal(prop.Address, addr) {
		panic(fmt.Sprintf("expected proposer to be validator %d. Got %X", 1, prop.Address))
	}
}

// Now let's do it all again, but starting from round 2 instead of 0
func TestStateProposerSelection2(t *testing.T) {
	cs1, vss := randState(4) // test needs more work for more than 3 validators
	height := cs1.Height
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)

	// this time we jump in at round 2
	incrementRound(vss[1:]...)
	incrementRound(vss[1:]...)

	var round int32 = 2
	startTestRound(cs1, height, round)

	ensureNewRound(newRoundCh, height, round) // wait for the new round

	// everyone just votes nil. we get a new proposer each round
	for i := int32(0); int(i) < len(vss); i++ {
		prop := cs1.GetRoundState().Validators.GetProposer()
		pvk, err := vss[int(i+round)%len(vss)].GetPubKey()
		require.NoError(t, err)
		addr := pvk.Address()
		correctProposer := addr
		if !bytes.Equal(prop.Address, correctProposer) {
			panic(fmt.Sprintf(
				"expected RoundState.Validators.GetProposer() to be validator %d. Got %X",
				int(i+2)%len(vss),
				prop.Address))
		}

		rs := cs1.GetRoundState()
		signAddVotes(cs1, cmtproto.PrecommitType, nil, rs.ProposalBlockParts.Header(), true, vss[1:]...)
		ensureNewRound(newRoundCh, height, i+round+1) // wait for the new round event each round
		incrementRound(vss[1:]...)
	}
}

// a non-validator should timeout into the prevote round
func TestStateEnterProposeNoPrivValidator(t *testing.T) {
	cs, _ := randState(1)
	cs.SetPrivValidator(nil)
	height, round := cs.Height, cs.Round

	// Listen for propose timeout event
	timeoutCh := subscribe(cs.eventBus, types.EventQueryTimeoutPropose)

	startTestRound(cs, height, round)

	// if we're not a validator, EnterPropose should timeout
	ensureNewTimeout(timeoutCh, height, round, cs.config.TimeoutPropose.Nanoseconds())

	if cs.GetRoundState().Proposal != nil {
		t.Error("Expected to make no proposal, since no privValidator")
	}
}

// a validator should not timeout of the prevote round (TODO: unless the block is really big!)
func TestStateEnterProposeYesPrivValidator(t *testing.T) {
	cs, _ := randState(1)
	height, round := cs.Height, cs.Round

	// Listen for propose timeout event

	timeoutCh := subscribe(cs.eventBus, types.EventQueryTimeoutPropose)
	proposalCh := subscribe(cs.eventBus, types.EventQueryCompleteProposal)

	cs.enterNewRound(height, round)
	cs.startRoutines(3)

	ensureNewProposal(proposalCh, height, round)

	// Check that Proposal, ProposalBlock, ProposalBlockParts are set.
	rs := cs.GetRoundState()
	if rs.Proposal == nil {
		t.Error("rs.Proposal should be set")
	}
	if rs.ProposalBlock == nil {
		t.Error("rs.ProposalBlock should be set")
	}
	if rs.ProposalBlockParts.Total() == 0 {
		t.Error("rs.ProposalBlockParts should be set")
	}

	// if we're a validator, enterPropose should not timeout
	ensureNoNewTimeout(timeoutCh, cs.config.TimeoutPropose.Nanoseconds())
}

func TestStateBadProposal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(2)
	height, round := cs1.Height, cs1.Round
	vs2 := vss[1]

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	voteCh := subscribe(cs1.eventBus, types.EventQueryVote)

	propBlock, _, err := cs1.createProposalBlock(ctx) // changeProposer(t, cs1, vs2)
	require.NoError(t, err)

	// make the second validator the proposer by incrementing round
	round++
	incrementRound(vss[1:]...)

	// make the block bad by tampering with statehash
	stateHash := propBlock.AppHash
	if len(stateHash) == 0 {
		stateHash = make([]byte, 32)
	}
	stateHash[0] = (stateHash[0] + 1) % 255
	propBlock.AppHash = stateHash
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)
	blockID := types.BlockID{Hash: propBlock.Hash(), PartSetHeader: propBlockParts.Header()}
	proposal := types.NewProposal(vs2.Height, round, -1, blockID)
	p := proposal.ToProto()
	if err := vs2.SignProposal(cs1.state.ChainID, p); err != nil {
		t.Fatal("failed to sign bad proposal", err)
	}

	proposal.Signature = p.Signature

	// set the proposal block
	if err := cs1.SetProposalAndBlock(proposal, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	// start the machine
	startTestRound(cs1, height, round)

	// wait for proposal
	ensureProposal(proposalCh, height, round, blockID)

	// wait for prevote
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], nil)

	// add bad prevote from vs2 and wait for it
	bps, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	signAddVotes(cs1, cmtproto.PrevoteType, propBlock.Hash(), bps.Header(), false, vs2)
	ensurePrevote(voteCh, height, round)

	// wait for precommit
	ensurePrecommit(voteCh, height, round)
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	bps2, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)
	signAddVotes(cs1, cmtproto.PrecommitType, propBlock.Hash(), bps2.Header(), true, vs2)
}

func TestStateOversizedBlock(t *testing.T) {
	const maxBytes = int64(types.BlockPartSizeBytes)

	for _, testCase := range []struct {
		name      string
		oversized bool
	}{
		{
			name:      "max size, correct block",
			oversized: false,
		},
		{
			name:      "off-by-1 max size, incorrect block",
			oversized: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cs1, vss := randState(2)
			cs1.state.ConsensusParams.Block.MaxBytes = maxBytes
			height, round := cs1.Height, cs1.Round
			vs2 := vss[1]

			partSize := types.BlockPartSizeBytes

			propBlock, propBlockParts := findBlockSizeLimit(t, height, maxBytes, cs1, partSize, testCase.oversized)

			timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
			voteCh := subscribe(cs1.eventBus, types.EventQueryVote)

			// make the second validator the proposer by incrementing round
			round++
			incrementRound(vss[1:]...)

			blockID := types.BlockID{Hash: propBlock.Hash(), PartSetHeader: propBlockParts.Header()}
			proposal := types.NewProposal(height, round, -1, blockID)
			p := proposal.ToProto()
			if err := vs2.SignProposal(cs1.state.ChainID, p); err != nil {
				t.Fatal("failed to sign bad proposal", err)
			}
			proposal.Signature = p.Signature

			totalBytes := 0
			for i := 0; i < int(propBlockParts.Total()); i++ {
				part := propBlockParts.GetPart(i)
				totalBytes += len(part.Bytes)
			}

			maxBlockParts := maxBytes / int64(types.BlockPartSizeBytes)
			if maxBytes > maxBlockParts*int64(types.BlockPartSizeBytes) {
				maxBlockParts++
			}
			numBlockParts := int64(propBlockParts.Total())

			if err := cs1.SetProposalAndBlock(proposal, propBlock, propBlockParts, "some peer"); err != nil {
				t.Fatal(err)
			}

			// start the machine
			startTestRound(cs1, height, round)

			t.Log("Block Sizes;", "Limit", maxBytes, "Current", totalBytes)
			t.Log("Proposal Parts;", "Maximum", maxBlockParts, "Current", numBlockParts)

			validateHash := propBlock.Hash()
			lockedRound := int32(1)
			if testCase.oversized {
				validateHash = nil
				lockedRound = -1
				// if the block is oversized cs1 should log an error with the block part message as it exceeds
				// the consensus params. The block is not added to cs.ProposalBlock so the node timeouts.
				ensureNewTimeout(timeoutProposeCh, height, round, cs1.config.Propose(round).Nanoseconds())
				// and then should send nil prevote and precommit regardless of whether other validators prevote and
				// precommit on it
			}
			ensurePrevote(voteCh, height, round)
			validatePrevote(t, cs1, round, vss[0], validateHash)

			// Should not accept a Proposal with too many block parts
			if numBlockParts > maxBlockParts {
				require.Nil(t, cs1.Proposal)
			}

			bps, err := propBlock.MakePartSet(partSize)
			require.NoError(t, err)

			signAddVotes(cs1, cmtproto.PrevoteType, propBlock.Hash(), bps.Header(), false, vs2)
			ensurePrevote(voteCh, height, round)
			ensurePrecommit(voteCh, height, round)
			validatePrecommit(t, cs1, round, lockedRound, vss[0], validateHash, validateHash)

			bps2, err := propBlock.MakePartSet(partSize)
			require.NoError(t, err)
			signAddVotes(cs1, cmtproto.PrecommitType, propBlock.Hash(), bps2.Header(), true, vs2)
		})
	}
}

//----------------------------------------------------------------------------------------------------
// FullRoundSuite

// propose, prevote, and precommit a block
func TestStateFullRound1(t *testing.T) {
	cs, vss := randState(1)
	height, round := cs.Height, cs.Round

	// NOTE: buffer capacity of 0 ensures we can validate prevote and last commit
	// before consensus can move to the next height (and cause a race condition)
	if err := cs.eventBus.Stop(); err != nil {
		t.Error(err)
	}
	eventBus := types.NewEventBusWithBufferCapacity(0)
	eventBus.SetLogger(log.TestingLogger().With("module", "events"))
	cs.SetEventBus(eventBus)
	if err := eventBus.Start(); err != nil {
		t.Error(err)
	}

	voteCh := subscribeUnBuffered(cs.eventBus, types.EventQueryVote)
	propCh := subscribe(cs.eventBus, types.EventQueryCompleteProposal)
	newRoundCh := subscribe(cs.eventBus, types.EventQueryNewRound)

	// Maybe it would be better to call explicitly startRoutines(4)
	startTestRound(cs, height, round)

	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(propCh, height, round)
	propBlockHash := cs.GetRoundState().ProposalBlock.Hash()

	ensurePrevote(voteCh, height, round) // wait for prevote
	validatePrevote(t, cs, round, vss[0], propBlockHash)

	ensurePrecommit(voteCh, height, round) // wait for precommit

	// we're going to roll right into new height
	ensureNewRound(newRoundCh, height+1, 0)

	validateLastPrecommit(t, cs, vss[0], propBlockHash)
}

// nil is proposed, so prevote and precommit nil
func TestStateFullRoundNil(t *testing.T) {
	cs, _ := randState(1)
	height, round := cs.Height, cs.Round

	voteCh := subscribeUnBuffered(cs.eventBus, types.EventQueryVote)

	cs.enterPrevote(height, round)
	cs.startRoutines(4)

	ensurePrevoteMatch(t, voteCh, height, round, nil)   // prevote
	ensurePrecommitMatch(t, voteCh, height, round, nil) // precommit
}

// run through propose, prevote, precommit commit with two validators
// where the first validator has to wait for votes from the second
func TestStateFullRound2(t *testing.T) {
	cs1, vss := randState(2)
	vs2 := vss[1]
	height, round := cs1.Height, cs1.Round

	voteCh := subscribeUnBuffered(cs1.eventBus, types.EventQueryVote)
	newBlockCh := subscribe(cs1.eventBus, types.EventQueryNewBlock)

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)

	ensurePrevote(voteCh, height, round) // prevote

	// we should be stuck in limbo waiting for more prevotes
	rs := cs1.GetRoundState()
	propBlockHash, propPartSetHeader := rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header()

	// prevote arrives from vs2:
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, propPartSetHeader, false, vs2)
	ensurePrevote(voteCh, height, round) // prevote

	ensurePrecommit(voteCh, height, round) // precommit
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, 0, 0, vss[0], propBlockHash, propBlockHash)

	// we should be stuck in limbo waiting for more precommits

	// precommit arrives from vs2:
	signAddVotes(cs1, cmtproto.PrecommitType, propBlockHash, propPartSetHeader, true, vs2)
	ensurePrecommit(voteCh, height, round)

	// wait to finish commit, propose in next height
	ensureNewBlock(newBlockCh, height)
}

//------------------------------------------------------------------------------------------
// LockSuite

// two validators, 4 rounds.
// two vals take turns proposing. val1 locks on first one, precommits nil on everything else
func TestStateLockNoPOL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(2)
	vs2 := vss[1]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	voteCh := subscribeUnBuffered(cs1.eventBus, types.EventQueryVote)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)

	/*
		Round1 (cs1, B) // B B // B B2
	*/

	// start round and wait for prevote
	cs1.enterNewRound(height, round)
	cs1.startRoutines(0)

	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	roundState := cs1.GetRoundState()
	theBlockHash := roundState.ProposalBlock.Hash()
	thePartSetHeader := roundState.ProposalBlockParts.Header()

	ensurePrevote(voteCh, height, round) // prevote

	// we should now be stuck in limbo forever, waiting for more prevotes
	// prevote arrives from vs2:
	signAddVotes(cs1, cmtproto.PrevoteType, theBlockHash, thePartSetHeader, false, vs2)
	ensurePrevote(voteCh, height, round) // prevote

	ensurePrecommit(voteCh, height, round) // precommit
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], theBlockHash, theBlockHash)

	// we should now be stuck in limbo forever, waiting for more precommits
	// lets add one for a different block
	hash := make([]byte, len(theBlockHash))
	copy(hash, theBlockHash)
	hash[0] = (hash[0] + 1) % 255
	signAddVotes(cs1, cmtproto.PrecommitType, hash, thePartSetHeader, true, vs2)
	ensurePrecommit(voteCh, height, round) // precommit

	// (note we're entering precommit for a second time this round)
	// but with invalid args. then we enterPrecommitWait, and the timeout to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	///

	round++ // moving to the next round
	ensureNewRound(newRoundCh, height, round)
	t.Log("#### ONTO ROUND 1")
	/*
		Round2 (cs1, B) // B B2
	*/

	incrementRound(vs2)

	// now we're on a new round and not the proposer, so wait for timeout
	ensureNewTimeout(timeoutProposeCh, height, round, cs1.config.Propose(round).Nanoseconds())

	rs := cs1.GetRoundState()

	require.Nil(t, rs.ProposalBlock, "Expected proposal block to be nil")

	// wait to finish prevote
	ensurePrevote(voteCh, height, round)
	// we should have prevoted our locked block
	validatePrevote(t, cs1, round, vss[0], rs.LockedBlock.Hash())

	// add a conflicting prevote from the other validator
	bps, err := rs.LockedBlock.MakePartSet(partSize)
	require.NoError(t, err)

	signAddVotes(cs1, cmtproto.PrevoteType, hash, bps.Header(), false, vs2)
	ensurePrevote(voteCh, height, round)

	// now we're going to enter prevote again, but with invalid args
	// and then prevote wait, which should timeout. then wait for precommit
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Prevote(round).Nanoseconds())

	ensurePrecommit(voteCh, height, round) // precommit
	// the proposed block should still be locked and our precommit added
	// we should precommit nil and be locked on the proposal
	validatePrecommit(t, cs1, round, 0, vss[0], nil, theBlockHash)

	// add conflicting precommit from vs2
	bps2, err := rs.LockedBlock.MakePartSet(partSize)
	require.NoError(t, err)
	signAddVotes(cs1, cmtproto.PrecommitType, hash, bps2.Header(), true, vs2)
	ensurePrecommit(voteCh, height, round)

	// (note we're entering precommit for a second time this round, but with invalid args
	// then we enterPrecommitWait and timeout into NewRound
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // entering new round
	ensureNewRound(newRoundCh, height, round)
	t.Log("#### ONTO ROUND 2")
	/*
		Round3 (vs2, _) // B, B2
	*/

	incrementRound(vs2)

	ensureNewProposal(proposalCh, height, round)
	rs = cs1.GetRoundState()

	// now we're on a new round and are the proposer
	if !bytes.Equal(rs.ProposalBlock.Hash(), rs.LockedBlock.Hash()) {
		panic(fmt.Sprintf(
			"Expected proposal block to be locked block. Got %v, Expected %v",
			rs.ProposalBlock,
			rs.LockedBlock))
	}

	ensurePrevote(voteCh, height, round) // prevote
	validatePrevote(t, cs1, round, vss[0], rs.LockedBlock.Hash())

	bps0, err := rs.ProposalBlock.MakePartSet(partSize)
	require.NoError(t, err)
	signAddVotes(cs1, cmtproto.PrevoteType, hash, bps0.Header(), false, vs2)
	ensurePrevote(voteCh, height, round)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Prevote(round).Nanoseconds())
	ensurePrecommit(voteCh, height, round) // precommit

	validatePrecommit(t, cs1, round, 0, vss[0], nil, theBlockHash) // precommit nil but be locked on proposal

	bps1, err := rs.ProposalBlock.MakePartSet(partSize)
	require.NoError(t, err)
	signAddVotes(
		cs1,
		cmtproto.PrecommitType,
		hash,
		bps1.Header(),
		true,
		vs2) // NOTE: conflicting precommits at same height
	ensurePrecommit(voteCh, height, round)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	cs2, _ := randState(2) // needed so generated block is different than locked block
	// before we time out into new round, set next proposal block
	prop, propBlock := decideProposal(ctx, t, cs2, vs2, vs2.Height, vs2.Round+1)
	if prop == nil || propBlock == nil {
		t.Fatal("Failed to create proposal block with vs2")
	}

	incrementRound(vs2)

	round++ // entering new round
	ensureNewRound(newRoundCh, height, round)
	t.Log("#### ONTO ROUND 3")
	/*
		Round4 (vs2, C) // B C // B C
	*/

	// now we're on a new round and not the proposer
	// so set the proposal block
	bps3, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)
	if err := cs1.SetProposalAndBlock(prop, propBlock, bps3, ""); err != nil {
		t.Fatal(err)
	}

	ensureNewProposal(proposalCh, height, round)
	ensurePrevote(voteCh, height, round) // prevote
	// prevote for locked block (not proposal)
	validatePrevote(t, cs1, 3, vss[0], cs1.LockedBlock.Hash())

	// prevote for proposed block
	bps4, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	signAddVotes(cs1, cmtproto.PrevoteType, propBlock.Hash(), bps4.Header(), false, vs2)
	ensurePrevote(voteCh, height, round)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Prevote(round).Nanoseconds())
	ensurePrecommit(voteCh, height, round)
	validatePrecommit(t, cs1, round, 0, vss[0], nil, theBlockHash) // precommit nil but locked on proposal

	bps5, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)
	signAddVotes(
		cs1,
		cmtproto.PrecommitType,
		propBlock.Hash(),
		bps5.Header(),
		true,
		vs2) // NOTE: conflicting precommits at same height
	ensurePrecommit(voteCh, height, round)
}

// 4 vals in two rounds,
// in round one: v1 precommits, other 3 only prevote so the block isn't committed
// in round two: v1 prevotes the same block that the node is locked on
// the others prevote a new block hence v1 changes lock and precommits the new block with the others
func TestStateLockPOLRelock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	newBlockCh := subscribe(cs1.eventBus, types.EventQueryNewBlockHeader)

	// everything done from perspective of cs1

	/*
		Round1 (cs1, B) // B B B B// B nil B nil

		eg. vs2 and vs4 didn't see the 2/3 prevotes
	*/

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)

	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	theBlockHash := rs.ProposalBlock.Hash()
	theBlockParts := rs.ProposalBlockParts.Header()

	ensurePrevote(voteCh, height, round) // prevote

	signAddVotes(cs1, cmtproto.PrevoteType, theBlockHash, theBlockParts, false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round) // our precommit
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], theBlockHash, theBlockHash)

	// add precommits from the rest
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	// before we timeout to the new round set the new proposal
	cs2 := newState(cs1.state, vs2, kvstore.NewInMemoryApplication())
	prop, propBlock := decideProposal(ctx, t, cs2, vs2, vs2.Height, vs2.Round+1)
	if prop == nil || propBlock == nil {
		t.Fatal("Failed to create proposal block with vs2")
	}
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	propBlockHash := propBlock.Hash()
	require.NotEqual(t, propBlockHash, theBlockHash)

	incrementRound(vs2, vs3, vs4)

	// timeout to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round
	// XXX: this isnt guaranteed to get there before the timeoutPropose ...
	if err := cs1.SetProposalAndBlock(prop, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	ensureNewRound(newRoundCh, height, round)
	t.Log("### ONTO ROUND 1")

	/*
		Round2 (vs2, C) // B C C C // C C C _)

		cs1 changes lock!
	*/

	// now we're on a new round and not the proposer
	// but we should receive the proposal
	ensureNewProposal(proposalCh, height, round)

	// go to prevote, node should prevote for locked block (not the new proposal) - this is relocking
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], theBlockHash)

	// now lets add prevotes from everyone else for the new block
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, propBlockParts.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// we should have unlocked and locked on the new block, sending a precommit for this new block
	validatePrecommit(t, cs1, round, round, vss[0], propBlockHash, propBlockHash)

	// more prevote creating a majority on the new block and this is then committed
	signAddVotes(cs1, cmtproto.PrecommitType, propBlockHash, propBlockParts.Header(), true, vs2, vs3)
	ensureNewBlockHeader(newBlockCh, height, propBlockHash)

	ensureNewRound(newRoundCh, height+1, 0)
}

// 4 vals, one precommits, other 3 polka at next round, so we unlock and precomit the polka
func TestStateLockPOLUnlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	unlockCh := subscribe(cs1.eventBus, types.EventQueryUnlock)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// everything done from perspective of cs1

	/*
		Round1 (cs1, B) // B B B B // B nil B nil
		eg. didn't see the 2/3 prevotes
	*/

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	theBlockHash := rs.ProposalBlock.Hash()
	theBlockParts := rs.ProposalBlockParts.Header()

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], theBlockHash)

	signAddVotes(cs1, cmtproto.PrevoteType, theBlockHash, theBlockParts, false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], theBlockHash, theBlockHash)

	// add precommits from the rest
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs4)
	signAddVotes(cs1, cmtproto.PrecommitType, theBlockHash, theBlockParts, true, vs3)

	// before we time out into new round, set next proposal block
	prop, propBlock := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round+1)
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	// timeout to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())
	rs = cs1.GetRoundState()
	lockedBlockHash := rs.LockedBlock.Hash()

	incrementRound(vs2, vs3, vs4)
	round++ // moving to the next round

	ensureNewRound(newRoundCh, height, round)
	t.Log("#### ONTO ROUND 1")
	/*
		Round2 (vs2, C) // B nil nil nil // nil nil nil _
		cs1 unlocks!
	*/
	//XXX: this isnt guaranteed to get there before the timeoutPropose ...
	if err := cs1.SetProposalAndBlock(prop, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	ensureNewProposal(proposalCh, height, round)

	// go to prevote, prevote for locked block (not proposal)
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], lockedBlockHash)
	// now lets add prevotes from everyone else for nil (a polka!)
	signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, vs2, vs3, vs4)

	// the polka makes us unlock and precommit nil
	ensureNewUnlock(unlockCh, height, round)
	ensurePrecommit(voteCh, height, round)

	// we should have unlocked and committed nil
	// NOTE: since we don't relock on nil, the lock round is -1
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3)
	ensureNewRound(newRoundCh, height, round+1)
}

// 4 vals, v1 locks on proposed block in the first round but the other validators only prevote
// In the second round, v1 misses the proposal but sees a majority prevote an unknown block so
// v1 should unlock and precommit nil. In the third round another block is proposed, all vals
// prevote and now v1 can lock onto the third block and precommit that
func TestStateLockPOLUnlockOnUnknownBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	// everything done from perspective of cs1

	/*
		Round0 (cs1, A) // A A A A// A nil nil nil
	*/

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)

	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	firstBlockHash := rs.ProposalBlock.Hash()
	firstBlockParts := rs.ProposalBlockParts.Header()

	ensurePrevote(voteCh, height, round) // prevote

	signAddVotes(cs1, cmtproto.PrevoteType, firstBlockHash, firstBlockParts, false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round) // our precommit
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], firstBlockHash, firstBlockHash)

	// add precommits from the rest
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	// before we timeout to the new round set the new proposal
	cs2 := newState(cs1.state, vs2, kvstore.NewInMemoryApplication())
	prop, propBlock := decideProposal(ctx, t, cs2, vs2, vs2.Height, vs2.Round+1)
	if prop == nil || propBlock == nil {
		t.Fatal("Failed to create proposal block with vs2")
	}
	secondBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	secondBlockHash := propBlock.Hash()
	require.NotEqual(t, secondBlockHash, firstBlockHash)

	incrementRound(vs2, vs3, vs4)

	// timeout to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round

	ensureNewRound(newRoundCh, height, round)
	t.Log("### ONTO ROUND 1")

	/*
		Round1 (vs2, B) // A B B B // nil nil nil nil)
	*/

	// now we're on a new round but v1 misses the proposal

	// go to prevote, node should prevote for locked block (not the new proposal) - this is relocking
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], firstBlockHash)

	// now lets add prevotes from everyone else for the new block
	signAddVotes(cs1, cmtproto.PrevoteType, secondBlockHash, secondBlockParts.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// we should have unlocked and locked on the new block, sending a precommit for this new block
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	if err := cs1.SetProposalAndBlock(prop, propBlock, secondBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	// more prevote creating a majority on the new block and this is then committed
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	// before we timeout to the new round set the new proposal
	cs3 := newState(cs1.state, vs3, kvstore.NewInMemoryApplication())
	prop, propBlock = decideProposal(ctx, t, cs3, vs3, vs3.Height, vs3.Round+1)
	if prop == nil || propBlock == nil {
		t.Fatal("Failed to create proposal block with vs2")
	}
	thirdPropBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)
	thirdPropBlockHash := propBlock.Hash()
	require.NotEqual(t, secondBlockHash, thirdPropBlockHash)

	incrementRound(vs2, vs3, vs4)

	// timeout to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round
	ensureNewRound(newRoundCh, height, round)
	t.Log("### ONTO ROUND 2")

	/*
		Round2 (vs3, C) // C C C C // C nil nil nil)
	*/

	if err := cs1.SetProposalAndBlock(prop, propBlock, thirdPropBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	ensurePrevote(voteCh, height, round)
	// we are no longer locked to the first block so we should be able to prevote
	validatePrevote(t, cs1, round, vss[0], thirdPropBlockHash)

	signAddVotes(cs1, cmtproto.PrevoteType, thirdPropBlockHash, thirdPropBlockParts.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// we have a majority, now vs1 can change lock to the third block
	validatePrecommit(t, cs1, round, round, vss[0], thirdPropBlockHash, thirdPropBlockHash)
}

// 4 vals
// a polka at round 1 but we miss it
// then a polka at round 2 that we lock on
// then we see the polka from round 1 but shouldn't unlock
func TestStateLockPOLSafety1(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round and wait for propose and prevote
	startTestRound(cs1, cs1.Height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	propBlock := rs.ProposalBlock

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlock.Hash())

	// the others sign a polka but we don't see it
	bps, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	prevotes := signVotes(cmtproto.PrevoteType, propBlock.Hash(), bps.Header(), false, vs2, vs3, vs4)

	t.Logf("old prop hash %v", fmt.Sprintf("%X", propBlock.Hash()))

	// we do see them precommit nil
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	// cs1 precommit nil
	ensurePrecommit(voteCh, height, round)
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	t.Log("### ONTO ROUND 1")

	prop, propBlock := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round+1)
	propBlockHash := propBlock.Hash()
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	incrementRound(vs2, vs3, vs4)

	round++ // moving to the next round
	ensureNewRound(newRoundCh, height, round)

	// XXX: this isnt guaranteed to get there before the timeoutPropose ...
	if err := cs1.SetProposalAndBlock(prop, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}
	/*Round2
	// we timeout and prevote our lock
	// a polka happened but we didn't see it!
	*/

	ensureNewProposal(proposalCh, height, round)

	rs = cs1.GetRoundState()

	if rs.LockedBlock != nil {
		panic("we should not be locked!")
	}
	t.Logf("new prop hash %v", fmt.Sprintf("%X", propBlockHash))

	// go to prevote, prevote for proposal block
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlockHash)

	// now we see the others prevote for it, so we should lock on it
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, propBlockParts.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// we should have precommitted
	validatePrecommit(t, cs1, round, round, vss[0], propBlockHash, propBlockHash)

	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	incrementRound(vs2, vs3, vs4)
	round++ // moving to the next round

	ensureNewRound(newRoundCh, height, round)

	t.Log("### ONTO ROUND 2")
	/*Round3
	we see the polka from round 1 but we shouldn't unlock!
	*/

	// timeout of propose
	ensureNewTimeout(timeoutProposeCh, height, round, cs1.config.Propose(round).Nanoseconds())

	// finish prevote
	ensurePrevote(voteCh, height, round)
	// we should prevote what we're locked on
	validatePrevote(t, cs1, round, vss[0], propBlockHash)

	newStepCh := subscribe(cs1.eventBus, types.EventQueryNewRoundStep)

	// before prevotes from the previous round are added
	// add prevotes from the earlier round
	addVotes(cs1, prevotes...)

	t.Log("Done adding prevotes!")

	ensureNoNewRoundStep(newStepCh)
}

// 4 vals.
// polka P0 at R0, P1 at R1, and P2 at R2,
// we lock on P0 at R0, don't see P1, and unlock using P2 at R2
// then we should make sure we don't lock using P1

// What we want:
// dont see P0, lock on P1 at R1, dont unlock using P0 at R2
func TestStateLockPOLSafety2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	unlockCh := subscribe(cs1.eventBus, types.EventQueryUnlock)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// the block for R0: gets polkad but we miss it
	// (even though we signed it, shhh)
	_, propBlock0 := decideProposal(ctx, t, cs1, vss[0], height, round)
	propBlockHash0 := propBlock0.Hash()
	propBlockParts0, err := propBlock0.MakePartSet(partSize)
	require.NoError(t, err)
	propBlockID0 := types.BlockID{Hash: propBlockHash0, PartSetHeader: propBlockParts0.Header()}

	// the others sign a polka but we don't see it
	prevotes := signVotes(cmtproto.PrevoteType, propBlockHash0, propBlockParts0.Header(), false, vs2, vs3, vs4)

	// the block for round 1
	prop1, propBlock1 := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round+1)
	propBlockHash1 := propBlock1.Hash()
	propBlockParts1, err := propBlock1.MakePartSet(partSize)
	require.NoError(t, err)

	incrementRound(vs2, vs3, vs4)

	round++ // moving to the next round
	t.Log("### ONTO Round 1")
	// jump in at round 1
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	if err := cs1.SetProposalAndBlock(prop1, propBlock1, propBlockParts1, "some peer"); err != nil {
		t.Fatal(err)
	}
	ensureNewProposal(proposalCh, height, round)

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlockHash1)

	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash1, propBlockParts1.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], propBlockHash1, propBlockHash1)

	// add precommits from the rest
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs4)
	signAddVotes(cs1, cmtproto.PrecommitType, propBlockHash1, propBlockParts1.Header(), true, vs3)

	incrementRound(vs2, vs3, vs4)

	// timeout of precommit wait to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round
	// in round 2 we see the polkad block from round 0
	newProp := types.NewProposal(height, round, 0, propBlockID0)
	p := newProp.ToProto()
	if err := vs3.SignProposal(cs1.state.ChainID, p); err != nil {
		t.Fatal(err)
	}

	newProp.Signature = p.Signature

	if err := cs1.SetProposalAndBlock(newProp, propBlock0, propBlockParts0, "some peer"); err != nil {
		t.Fatal(err)
	}

	// Add the pol votes
	addVotes(cs1, prevotes...)

	ensureNewRound(newRoundCh, height, round)
	t.Log("### ONTO Round 2")
	/*Round2
	// now we see the polka from round 1, but we shouldnt unlock
	*/
	ensureNewProposal(proposalCh, height, round)

	ensureNoNewUnlock(unlockCh)
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlockHash1)
}

// 4 vals.
// polka P0 at R0 for B0. We lock B0 on P0 at R0. P0 unlocks value at R1.

// What we want:
// P0 proposes B0 at R3.
func TestProposeValidBlock(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	unlockCh := subscribe(cs1.eventBus, types.EventQueryUnlock)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round and wait for propose and prevote
	startTestRound(cs1, cs1.Height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	propBlock := rs.ProposalBlock
	propBlockHash := propBlock.Hash()

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlockHash)

	// the others sign a polka
	bps, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, bps.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// we should have precommitted
	validatePrecommit(t, cs1, round, round, vss[0], propBlockHash, propBlockHash)

	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	incrementRound(vs2, vs3, vs4)
	round++ // moving to the next round

	ensureNewRound(newRoundCh, height, round)

	t.Log("### ONTO ROUND 2")

	// timeout of propose
	ensureNewTimeout(timeoutProposeCh, height, round, cs1.config.Propose(round).Nanoseconds())

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlockHash)

	signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, vs2, vs3, vs4)

	ensureNewUnlock(unlockCh, height, round)

	ensurePrecommit(voteCh, height, round)
	// we should have precommitted
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	incrementRound(vs2, vs3, vs4)
	incrementRound(vs2, vs3, vs4)

	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	round += 2 // moving to the next round

	ensureNewRound(newRoundCh, height, round)
	t.Log("### ONTO ROUND 3")

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round

	ensureNewRound(newRoundCh, height, round)

	t.Log("### ONTO ROUND 4")

	ensureNewProposal(proposalCh, height, round)

	rs = cs1.GetRoundState()
	assert.True(t, bytes.Equal(rs.ProposalBlock.Hash(), propBlockHash))
	assert.True(t, bytes.Equal(rs.ProposalBlock.Hash(), rs.ValidBlock.Hash()))
	assert.True(t, rs.Proposal.POLRound == rs.ValidRound)
	assert.True(t, bytes.Equal(rs.Proposal.BlockID.Hash, rs.ValidBlock.Hash()))
}

// What we want:
// P0 miss to lock B but set valid block to B after receiving delayed prevote.
func TestSetValidBlockOnDelayedPrevote(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	validBlockCh := subscribe(cs1.eventBus, types.EventQueryValidBlock)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round and wait for propose and prevote
	startTestRound(cs1, cs1.Height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	propBlock := rs.ProposalBlock
	propBlockHash := propBlock.Hash()
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], propBlockHash)

	// vs2 send prevote for propBlock
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, propBlockParts.Header(), false, vs2)

	// vs3 send prevote nil
	signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, vs3)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Prevote(round).Nanoseconds())

	ensurePrecommit(voteCh, height, round)
	// we should have precommitted
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	rs = cs1.GetRoundState()

	assert.True(t, rs.ValidBlock == nil)
	assert.True(t, rs.ValidBlockParts == nil)
	assert.True(t, rs.ValidRound == -1)

	// vs2 send (delayed) prevote for propBlock
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, propBlockParts.Header(), false, vs4)

	ensureNewValidBlock(validBlockCh, height, round)

	rs = cs1.GetRoundState()

	assert.True(t, bytes.Equal(rs.ValidBlock.Hash(), propBlockHash))
	assert.True(t, rs.ValidBlockParts.Header().Equals(propBlockParts.Header()))
	assert.True(t, rs.ValidRound == round)
}

// What we want:
// P0 miss to lock B as Proposal Block is missing, but set valid block to B after
// receiving delayed Block Proposal.
func TestSetValidBlockOnDelayedProposal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	validBlockCh := subscribe(cs1.eventBus, types.EventQueryValidBlock)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)

	round++ // move to round in which P0 is not proposer
	incrementRound(vs2, vs3, vs4)

	startTestRound(cs1, cs1.Height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewTimeout(timeoutProposeCh, height, round, cs1.config.Propose(round).Nanoseconds())

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], nil)

	prop, propBlock := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round+1)
	propBlockHash := propBlock.Hash()
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	// vs2, vs3 and vs4 send prevote for propBlock
	signAddVotes(cs1, cmtproto.PrevoteType, propBlockHash, propBlockParts.Header(), false, vs2, vs3, vs4)
	ensureNewValidBlock(validBlockCh, height, round)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Prevote(round).Nanoseconds())

	ensurePrecommit(voteCh, height, round)
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	if err := cs1.SetProposalAndBlock(prop, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()

	assert.True(t, bytes.Equal(rs.ValidBlock.Hash(), propBlockHash))
	assert.True(t, rs.ValidBlockParts.Header().Equals(propBlockParts.Header()))
	assert.True(t, rs.ValidRound == round)
}

func TestProcessProposalAccept(t *testing.T) {
	for _, testCase := range []struct {
		name               string
		accept             bool
		expectedNilPrevote bool
	}{
		{
			name:               "accepted block is prevoted",
			accept:             true,
			expectedNilPrevote: false,
		},
		{
			name:               "rejected block is not prevoted",
			accept:             false,
			expectedNilPrevote: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			m := abcimocks.NewApplication(t)
			status := abci.ResponseProcessProposal_REJECT
			if testCase.accept {
				status = abci.ResponseProcessProposal_ACCEPT
			}
			m.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: status}, nil)
			m.On("PrepareProposal", mock.Anything, mock.Anything).Return(&abci.ResponsePrepareProposal{}, nil).Maybe()
			cs1, _ := randStateWithApp(4, m)
			height, round := cs1.Height, cs1.Round

			proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
			newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
			pv1, err := cs1.privValidator.GetPubKey()
			require.NoError(t, err)
			addr := pv1.Address()
			voteCh := subscribeToVoter(cs1, addr)

			startTestRound(cs1, cs1.Height, round)
			ensureNewRound(newRoundCh, height, round)

			ensureNewProposal(proposalCh, height, round)
			rs := cs1.GetRoundState()
			var prevoteHash cmtbytes.HexBytes
			if !testCase.expectedNilPrevote {
				prevoteHash = rs.ProposalBlock.Hash()
			}
			ensurePrevoteMatch(t, voteCh, height, round, prevoteHash)
		})
	}
}

// TestExtendVoteCalledWhenEnabled tests that the vote extension methods are called at the
// correct point in the consensus algorithm when vote extensions are enabled.
func TestExtendVoteCalledWhenEnabled(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		enabled bool
	}{
		{
			name:    "enabled",
			enabled: true,
		},
		{
			name:    "disabled",
			enabled: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			m := abcimocks.NewApplication(t)
			m.On("PrepareProposal", mock.Anything, mock.Anything).Return(&abci.ResponsePrepareProposal{}, nil)
			m.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)
			if testCase.enabled {
				m.On("ExtendVote", mock.Anything, mock.Anything).Return(&abci.ResponseExtendVote{
					VoteExtension: []byte("extension"),
				}, nil)
				m.On("VerifyVoteExtension", mock.Anything, mock.Anything).Return(&abci.ResponseVerifyVoteExtension{
					Status: abci.ResponseVerifyVoteExtension_ACCEPT,
				}, nil)
			}
			m.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil).Maybe()
			m.On("FinalizeBlock", mock.Anything, mock.Anything).Return(&abci.ResponseFinalizeBlock{}, nil).Maybe()
			height := int64(1)
			if !testCase.enabled {
				height = 0
			}
			cs1, vss := randStateWithAppWithHeight(4, m, height)

			height, round := cs1.Height, cs1.Round

			proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
			newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
			pv1, err := cs1.privValidator.GetPubKey()
			require.NoError(t, err)
			addr := pv1.Address()
			voteCh := subscribeToVoter(cs1, addr)

			startTestRound(cs1, cs1.Height, round)
			ensureNewRound(newRoundCh, height, round)
			ensureNewProposal(proposalCh, height, round)

			m.AssertNotCalled(t, "ExtendVote", mock.Anything, mock.Anything)

			rs := cs1.GetRoundState()

			blockID := types.BlockID{
				Hash:          rs.ProposalBlock.Hash(),
				PartSetHeader: rs.ProposalBlockParts.Header(),
			}
			signAddVotes(cs1, cmtproto.PrevoteType, blockID.Hash, blockID.PartSetHeader, false, vss[1:]...)
			ensurePrevoteMatch(t, voteCh, height, round, blockID.Hash)

			ensurePrecommit(voteCh, height, round)

			if testCase.enabled {
				m.AssertCalled(t, "ExtendVote", context.TODO(), &abci.RequestExtendVote{
					Height:             height,
					Hash:               blockID.Hash,
					Time:               rs.ProposalBlock.Time,
					Txs:                rs.ProposalBlock.Txs.ToSliceOfBytes(),
					ProposedLastCommit: abci.CommitInfo{},
					Misbehavior:        rs.ProposalBlock.Evidence.Evidence.ToABCI(),
					NextValidatorsHash: rs.ProposalBlock.NextValidatorsHash,
					ProposerAddress:    rs.ProposalBlock.ProposerAddress,
				})
			} else {
				m.AssertNotCalled(t, "ExtendVote", mock.Anything, mock.Anything)
			}

			signAddVotes(cs1, cmtproto.PrecommitType, blockID.Hash, blockID.PartSetHeader, testCase.enabled, vss[1:]...)
			ensureNewRound(newRoundCh, height+1, 0)
			m.AssertExpectations(t)

			// Only 3 of the vote extensions are seen, as consensus proceeds as soon as the +2/3 threshold
			// is observed by the consensus engine.
			for _, pv := range vss[1:3] {
				pv, err := pv.GetPubKey()
				require.NoError(t, err)
				addr := pv.Address()
				if testCase.enabled {
					m.AssertCalled(t, "VerifyVoteExtension", context.TODO(), &abci.RequestVerifyVoteExtension{
						Hash:             blockID.Hash,
						ValidatorAddress: addr,
						Height:           height,
						VoteExtension:    []byte("extension"),
					})
				} else {
					m.AssertNotCalled(t, "VerifyVoteExtension", mock.Anything, mock.Anything)
				}
			}
		})
	}
}

// TestVerifyVoteExtensionNotCalledOnAbsentPrecommit tests that the VerifyVoteExtension
// method is not called for a validator's vote that is never delivered.
func TestVerifyVoteExtensionNotCalledOnAbsentPrecommit(t *testing.T) {
	m := abcimocks.NewApplication(t)
	m.On("PrepareProposal", mock.Anything, mock.Anything).Return(&abci.ResponsePrepareProposal{}, nil)
	m.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)
	m.On("ExtendVote", mock.Anything, mock.Anything).Return(&abci.ResponseExtendVote{
		VoteExtension: []byte("extension"),
	}, nil)
	m.On("VerifyVoteExtension", mock.Anything, mock.Anything).Return(&abci.ResponseVerifyVoteExtension{
		Status: abci.ResponseVerifyVoteExtension_ACCEPT,
	}, nil)
	m.On("FinalizeBlock", mock.Anything, mock.Anything).Return(&abci.ResponseFinalizeBlock{}, nil).Maybe()
	m.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil).Maybe()
	cs1, vss := randStateWithApp(4, m)
	height, round := cs1.Height, cs1.Round
	cs1.state.ConsensusParams.ABCI.VoteExtensionsEnableHeight = cs1.Height

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	startTestRound(cs1, cs1.Height, round)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()

	blockID := types.BlockID{
		Hash:          rs.ProposalBlock.Hash(),
		PartSetHeader: rs.ProposalBlockParts.Header(),
	}
	signAddVotes(cs1, cmtproto.PrevoteType, blockID.Hash, blockID.PartSetHeader, false, vss...)
	ensurePrevoteMatch(t, voteCh, height, round, blockID.Hash)

	ensurePrecommit(voteCh, height, round)

	m.AssertCalled(t, "ExtendVote", context.TODO(), &abci.RequestExtendVote{
		Height:             height,
		Hash:               blockID.Hash,
		Time:               rs.ProposalBlock.Time,
		Txs:                rs.ProposalBlock.Txs.ToSliceOfBytes(),
		ProposedLastCommit: abci.CommitInfo{},
		Misbehavior:        rs.ProposalBlock.Evidence.Evidence.ToABCI(),
		NextValidatorsHash: rs.ProposalBlock.NextValidatorsHash,
		ProposerAddress:    rs.ProposalBlock.ProposerAddress,
	})

	signAddVotes(cs1, cmtproto.PrecommitType, blockID.Hash, blockID.PartSetHeader, true, vss[2:]...)
	ensureNewRound(newRoundCh, height+1, 0)
	m.AssertExpectations(t)

	// vss[1] did not issue a precommit for the block, ensure that a vote extension
	// for its address was not sent to the application.
	pv, err := vss[1].GetPubKey()
	require.NoError(t, err)
	addr = pv.Address()

	m.AssertNotCalled(t, "VerifyVoteExtension", context.TODO(), &abci.RequestVerifyVoteExtension{
		Hash:             blockID.Hash,
		ValidatorAddress: addr,
		Height:           height,
		VoteExtension:    []byte("extension"),
	})
}

// TestPrepareProposalReceivesVoteExtensions tests that the PrepareProposal method
// is called with the vote extensions from the previous height. The test functions
// by completing a consensus height with a mock application as the proposer. The
// test then proceeds to fail several rounds of consensus until the mock application
// is the proposer again and ensures that the mock application receives the set of
// vote extensions from the previous consensus instance.
func TestPrepareProposalReceivesVoteExtensions(t *testing.T) {
	// create a list of vote extensions, one for each validator.
	voteExtensions := [][]byte{
		[]byte("extension 0"),
		[]byte("extension 1"),
		[]byte("extension 2"),
		[]byte("extension 3"),
	}

	m := abcimocks.NewApplication(t)
	m.On("ExtendVote", mock.Anything, mock.Anything).Return(&abci.ResponseExtendVote{
		VoteExtension: voteExtensions[0],
	}, nil)
	m.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)

	// capture the prepare proposal request.
	rpp := &abci.RequestPrepareProposal{}
	m.On("PrepareProposal", mock.Anything, mock.MatchedBy(func(r *abci.RequestPrepareProposal) bool {
		rpp = r
		return true
	})).Return(&abci.ResponsePrepareProposal{}, nil)

	m.On("VerifyVoteExtension", mock.Anything, mock.Anything).Return(&abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil)
	m.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil).Maybe()
	m.On("FinalizeBlock", mock.Anything, mock.Anything).Return(&abci.ResponseFinalizeBlock{}, nil)

	cs1, vss := randStateWithApp(4, m)
	height, round := cs1.Height, cs1.Round

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	rs := cs1.GetRoundState()
	blockID := types.BlockID{
		Hash:          rs.ProposalBlock.Hash(),
		PartSetHeader: rs.ProposalBlockParts.Header(),
	}
	signAddVotes(cs1, cmtproto.PrevoteType, blockID.Hash, blockID.PartSetHeader, false, vss[1:]...)

	// create a precommit for each validator with the associated vote extension.
	for i, vs := range vss[1:] {
		signAddPrecommitWithExtension(t, cs1, blockID.Hash, blockID.PartSetHeader, voteExtensions[i+1], vs)
	}

	ensurePrevote(voteCh, height, round)

	// ensure that the height is committed.
	ensurePrecommitMatch(t, voteCh, height, round, blockID.Hash)
	incrementHeight(vss[1:]...)

	height++
	round = 0
	ensureNewRound(newRoundCh, height, round)
	incrementRound(vss[1:]...)
	incrementRound(vss[1:]...)
	incrementRound(vss[1:]...)
	round = 3

	blockID2 := types.BlockID{}
	signAddVotes(cs1, cmtproto.PrecommitType, blockID2.Hash, blockID2.PartSetHeader, true, vss[1:]...)
	ensureNewRound(newRoundCh, height, round)
	ensureNewProposal(proposalCh, height, round)

	// ensure that the proposer received the list of vote extensions from the
	// previous height.
	require.Len(t, rpp.LocalLastCommit.Votes, len(vss))
	for i := range vss {
		vote := &rpp.LocalLastCommit.Votes[i]
		require.Equal(t, vote.VoteExtension, voteExtensions[i])

		require.NotZero(t, len(vote.ExtensionSignature))
		cve := cmtproto.CanonicalVoteExtension{
			Extension: vote.VoteExtension,
			Height:    height - 1, // the vote extension was signed in the previous height
			Round:     int64(rpp.LocalLastCommit.Round),
			ChainId:   test.DefaultTestChainID,
		}
		extSignBytes, err := protoio.MarshalDelimited(&cve)
		require.NoError(t, err)
		pubKey, err := vss[i].PrivValidator.GetPubKey() //nolint:staticcheck
		require.NoError(t, err)
		require.True(t, pubKey.VerifySignature(extSignBytes, vote.ExtensionSignature))
	}
}

func TestFinalizeBlockCalled(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		voteNil      bool
		expectCalled bool
	}{
		{
			name:         "finalize block called when block committed",
			voteNil:      false,
			expectCalled: true,
		},
		{
			name:         "not called when block not committed",
			voteNil:      true,
			expectCalled: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			m := abcimocks.NewApplication(t)
			m.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{
				Status: abci.ResponseProcessProposal_ACCEPT,
			}, nil)
			m.On("PrepareProposal", mock.Anything, mock.Anything).Return(&abci.ResponsePrepareProposal{}, nil)
			// We only expect VerifyVoteExtension to be called on non-nil precommits.
			// https://github.com/tendermint/tendermint/issues/8487
			if !testCase.voteNil {
				m.On("ExtendVote", mock.Anything, mock.Anything).Return(&abci.ResponseExtendVote{}, nil)
				m.On("VerifyVoteExtension", mock.Anything, mock.Anything).Return(&abci.ResponseVerifyVoteExtension{
					Status: abci.ResponseVerifyVoteExtension_ACCEPT,
				}, nil)
			}
			r := &abci.ResponseFinalizeBlock{AppHash: []byte("the_hash")}
			m.On("FinalizeBlock", mock.Anything, mock.Anything).Return(r, nil).Maybe()
			m.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil).Maybe()

			cs1, vss := randStateWithApp(4, m)
			height, round := cs1.Height, cs1.Round

			proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
			newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
			pv1, err := cs1.privValidator.GetPubKey()
			require.NoError(t, err)
			addr := pv1.Address()
			voteCh := subscribeToVoter(cs1, addr)

			startTestRound(cs1, cs1.Height, round)
			ensureNewRound(newRoundCh, height, round)
			ensureNewProposal(proposalCh, height, round)
			rs := cs1.GetRoundState()

			blockID := types.BlockID{}
			nextRound := round + 1
			nextHeight := height
			if !testCase.voteNil {
				nextRound = 0
				nextHeight = height + 1
				blockID = types.BlockID{
					Hash:          rs.ProposalBlock.Hash(),
					PartSetHeader: rs.ProposalBlockParts.Header(),
				}
			}

			signAddVotes(cs1, cmtproto.PrevoteType, blockID.Hash, blockID.PartSetHeader, false, vss[1:]...)
			ensurePrevoteMatch(t, voteCh, height, round, rs.ProposalBlock.Hash())

			signAddVotes(cs1, cmtproto.PrecommitType, blockID.Hash, blockID.PartSetHeader, true, vss[1:]...)
			ensurePrecommit(voteCh, height, round)

			ensureNewRound(newRoundCh, nextHeight, nextRound)
			m.AssertExpectations(t)

			if !testCase.expectCalled {
				m.AssertNotCalled(t, "FinalizeBlock", context.TODO(), mock.Anything)
			} else {
				m.AssertCalled(t, "FinalizeBlock", context.TODO(), mock.Anything)
			}
		})
	}
}

// TestVoteExtensionEnableHeight tests that 'ExtensionRequireHeight' correctly
// enforces that vote extensions be present in consensus for heights greater than
// or equal to the configured value.
func TestVoteExtensionEnableHeight(t *testing.T) {
	for _, testCase := range []struct {
		name                  string
		enableHeight          int64
		hasExtension          bool
		expectExtendCalled    bool
		expectVerifyCalled    bool
		expectSuccessfulRound bool
	}{
		{
			name:                  "extension present but not enabled",
			hasExtension:          true,
			enableHeight:          0,
			expectExtendCalled:    false,
			expectVerifyCalled:    false,
			expectSuccessfulRound: false,
		},
		{
			name:                  "extension absent but not required",
			hasExtension:          false,
			enableHeight:          0,
			expectExtendCalled:    false,
			expectVerifyCalled:    false,
			expectSuccessfulRound: true,
		},
		{
			name:                  "extension present and required",
			hasExtension:          true,
			enableHeight:          1,
			expectExtendCalled:    true,
			expectVerifyCalled:    true,
			expectSuccessfulRound: true,
		},
		{
			name:                  "extension absent but required",
			hasExtension:          false,
			enableHeight:          1,
			expectExtendCalled:    true,
			expectVerifyCalled:    false,
			expectSuccessfulRound: false,
		},
		{
			name:                  "extension absent but required in future height",
			hasExtension:          false,
			enableHeight:          2,
			expectExtendCalled:    false,
			expectVerifyCalled:    false,
			expectSuccessfulRound: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			numValidators := 3
			m := abcimocks.NewApplication(t)
			m.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{
				Status: abci.ResponseProcessProposal_ACCEPT,
			}, nil)
			m.On("PrepareProposal", mock.Anything, mock.Anything).Return(&abci.ResponsePrepareProposal{}, nil)
			if testCase.expectExtendCalled {
				m.On("ExtendVote", mock.Anything, mock.Anything).Return(&abci.ResponseExtendVote{}, nil)
			}
			if testCase.expectVerifyCalled {
				m.On("VerifyVoteExtension", mock.Anything, mock.Anything).Return(&abci.ResponseVerifyVoteExtension{
					Status: abci.ResponseVerifyVoteExtension_ACCEPT,
				}, nil).Times(numValidators - 1)
			}
			m.On("FinalizeBlock", mock.Anything, mock.Anything).Return(&abci.ResponseFinalizeBlock{}, nil).Maybe()
			m.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil).Maybe()
			cs1, vss := randStateWithAppWithHeight(numValidators, m, testCase.enableHeight)
			cs1.state.ConsensusParams.ABCI.VoteExtensionsEnableHeight = testCase.enableHeight
			height, round := cs1.Height, cs1.Round

			timeoutCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
			proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
			newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
			pv1, err := cs1.privValidator.GetPubKey()
			require.NoError(t, err)
			addr := pv1.Address()
			voteCh := subscribeToVoter(cs1, addr)

			startTestRound(cs1, cs1.Height, round)
			ensureNewRound(newRoundCh, height, round)
			ensureNewProposal(proposalCh, height, round)
			rs := cs1.GetRoundState()

			// sign all of the votes
			signAddVotes(cs1, cmtproto.PrevoteType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), false, vss[1:]...)
			ensurePrevoteMatch(t, voteCh, height, round, rs.ProposalBlock.Hash())

			var ext []byte
			if testCase.hasExtension {
				ext = []byte("extension")
			}

			for _, vs := range vss[1:] {
				vote, err := vs.signVote(cmtproto.PrecommitType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), ext, testCase.hasExtension)
				require.NoError(t, err)
				addVotes(cs1, vote)
			}
			if testCase.expectSuccessfulRound {
				ensurePrecommit(voteCh, height, round)
				height++
				ensureNewRound(newRoundCh, height, round)
			} else {
				ensureNoNewTimeout(timeoutCh, cs1.config.Precommit(round).Nanoseconds())
			}

			m.AssertExpectations(t)
		})
	}
}

// TestStateDoesntCrashOnInvalidVote tests that the state does not crash when
// receiving an invalid vote. In particular, one with the incorrect
// ValidatorIndex.
func TestStateDoesntCrashOnInvalidVote(t *testing.T) {
	cs, vss := randState(2)
	height, round := cs.Height, cs.Round
	// create dummy peer
	peer := p2pmock.NewPeer(nil)

	startTestRound(cs, height, round)

	_, propBlock := decideProposal(context.Background(), t, cs, vss[0], height, round)
	propBlockParts, err := propBlock.MakePartSet(types.BlockPartSizeBytes)
	assert.NoError(t, err)

	vote := signVote(vss[1], cmtproto.PrecommitType, propBlock.Hash(), propBlockParts.Header(), true)

	// Non-existent validator index
	vote.ValidatorIndex = int32(len(vss))

	voteMessage := &VoteMessage{vote}
	assert.NotPanics(t, func() {
		cs.handleMsg(msgInfo{voteMessage, peer.ID()})
	})

	added, err := cs.AddVote(vote, peer.ID())
	assert.False(t, added)
	assert.NoError(t, err)
	// TODO: uncomment once we punish peer and return an error
	// assert.Equal(t, ErrInvalidVote{Reason: "ValidatorIndex 2 is out of bounds [0, 2)"}, err)
}

// 4 vals, 3 Nil Precommits at P0
// What we want:
// P0 waits for timeoutPrecommit before starting next round
func TestWaitingTimeoutOnNilPolka(*testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)

	// start round
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())
	ensureNewRound(newRoundCh, height, round+1)
}

// 4 vals, 3 Prevotes for nil from the higher round.
// What we want:
// P0 waits for timeoutPropose in the next round before entering prevote
func TestWaitingTimeoutProposeOnNewRound(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	ensurePrevote(voteCh, height, round)

	incrementRound(vss[1:]...)
	signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, vs2, vs3, vs4)

	round++ // moving to the next round
	ensureNewRound(newRoundCh, height, round)

	rs := cs1.GetRoundState()
	assert.True(t, rs.Step == cstypes.RoundStepPropose) // P0 does not prevote before timeoutPropose expires

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Propose(round).Nanoseconds())

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], nil)
}

// 4 vals, 3 Precommits for nil from the higher round.
// What we want:
// P0 jump to higher round, precommit and start precommit wait
func TestRoundSkipOnNilPolkaFromHigherRound(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	ensurePrevote(voteCh, height, round)

	incrementRound(vss[1:]...)
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2, vs3, vs4)

	round++ // moving to the next round
	ensureNewRound(newRoundCh, height, round)

	ensurePrecommit(voteCh, height, round)
	validatePrecommit(t, cs1, round, -1, vss[0], nil, nil)

	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round
	ensureNewRound(newRoundCh, height, round)
}

// 4 vals, 3 Prevotes for nil in the current round.
// What we want:
// P0 wait for timeoutPropose to expire before sending prevote.
func TestWaitTimeoutProposeOnNilPolkaForTheCurrentRound(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, int32(1)

	timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round in which PO is not proposer
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	incrementRound(vss[1:]...)
	signAddVotes(cs1, cmtproto.PrevoteType, nil, types.PartSetHeader{}, false, vs2, vs3, vs4)

	ensureNewTimeout(timeoutProposeCh, height, round, cs1.config.Propose(round).Nanoseconds())

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], nil)
}

// What we want:
// P0 emit NewValidBlock event upon receiving 2/3+ Precommit for B but hasn't received block B yet
func TestEmitNewValidBlockEventOnCommitWithoutBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, int32(1)

	incrementRound(vs2, vs3, vs4)

	partSize := types.BlockPartSizeBytes

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	validBlockCh := subscribe(cs1.eventBus, types.EventQueryValidBlock)

	_, propBlock := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round)
	propBlockHash := propBlock.Hash()
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	// start round in which PO is not proposer
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	// vs2, vs3 and vs4 send precommit for propBlock
	signAddVotes(cs1, cmtproto.PrecommitType, propBlockHash, propBlockParts.Header(), true, vs2, vs3, vs4)
	ensureNewValidBlock(validBlockCh, height, round)

	rs := cs1.GetRoundState()
	assert.True(t, rs.Step == cstypes.RoundStepCommit)
	assert.True(t, rs.ProposalBlock == nil)
	assert.True(t, rs.ProposalBlockParts.Header().Equals(propBlockParts.Header()))
}

// What we want:
// P0 receives 2/3+ Precommit for B for round 0, while being in round 1. It emits NewValidBlock event.
// After receiving block, it executes block and moves to the next height.
func TestCommitFromPreviousRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, int32(1)

	partSize := types.BlockPartSizeBytes

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	validBlockCh := subscribe(cs1.eventBus, types.EventQueryValidBlock)
	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)

	prop, propBlock := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round)
	propBlockHash := propBlock.Hash()
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	// start round in which PO is not proposer
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	// vs2, vs3 and vs4 send precommit for propBlock for the previous round
	signAddVotes(cs1, cmtproto.PrecommitType, propBlockHash, propBlockParts.Header(), true, vs2, vs3, vs4)

	ensureNewValidBlock(validBlockCh, height, round)

	rs := cs1.GetRoundState()
	assert.True(t, rs.Step == cstypes.RoundStepCommit)
	assert.True(t, rs.CommitRound == vs2.Round)
	assert.True(t, rs.ProposalBlock == nil)
	assert.True(t, rs.ProposalBlockParts.Header().Equals(propBlockParts.Header()))

	if err := cs1.SetProposalAndBlock(prop, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}

	ensureNewProposal(proposalCh, height, round)
	ensureNewRound(newRoundCh, height+1, 0)
}

type fakeTxNotifier struct {
	ch chan struct{}
}

func (n *fakeTxNotifier) TxsAvailable() <-chan struct{} {
	return n.ch
}

func (n *fakeTxNotifier) Notify() {
	n.ch <- struct{}{}
}

// 2 vals precommit votes for a block but node times out waiting for the third. Move to next round
// and third precommit arrives which leads to the commit of that header and the correct
// start of the next round
func TestStartNextHeightCorrectlyAfterTimeout(t *testing.T) {
	config := ResetConfig("consensus_reactor_test")
	config.Consensus.SkipTimeoutCommit = false
	cs1, vss := randState(4)
	cs1.txNotifier = &fakeTxNotifier{ch: make(chan struct{})}

	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutProposeCh := subscribe(cs1.eventBus, types.EventQueryTimeoutPropose)
	precommitTimeoutCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	newBlockHeader := subscribe(cs1.eventBus, types.EventQueryNewBlockHeader)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	theBlockHash := rs.ProposalBlock.Hash()
	theBlockParts := rs.ProposalBlockParts.Header()

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], theBlockHash)

	signAddVotes(cs1, cmtproto.PrevoteType, theBlockHash, theBlockParts, false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], theBlockHash, theBlockHash)

	// add precommits
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2)
	signAddVotes(cs1, cmtproto.PrecommitType, theBlockHash, theBlockParts, true, vs3)

	// wait till timeout occurs
	ensurePrecommitTimeout(precommitTimeoutCh)

	ensureNewRound(newRoundCh, height, round+1)

	// majority is now reached
	signAddVotes(cs1, cmtproto.PrecommitType, theBlockHash, theBlockParts, true, vs4)

	ensureNewBlockHeader(newBlockHeader, height, theBlockHash)

	cs1.txNotifier.(*fakeTxNotifier).Notify()

	ensureNewTimeout(timeoutProposeCh, height+1, round, cs1.config.Propose(round).Nanoseconds())
	rs = cs1.GetRoundState()
	assert.False(
		t,
		rs.TriggeredTimeoutPrecommit,
		"triggeredTimeoutPrecommit should be false at the beginning of each round")
}

func TestResetTimeoutPrecommitUponNewHeight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	config := ResetConfig("consensus_reactor_test")
	config.Consensus.SkipTimeoutCommit = false
	cs1, vss := randState(4)

	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round

	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)

	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	newBlockHeader := subscribe(cs1.eventBus, types.EventQueryNewBlockHeader)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	theBlockHash := rs.ProposalBlock.Hash()
	theBlockParts := rs.ProposalBlockParts.Header()

	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], theBlockHash)

	signAddVotes(cs1, cmtproto.PrevoteType, theBlockHash, theBlockParts, false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	validatePrecommit(t, cs1, round, round, vss[0], theBlockHash, theBlockHash)

	// add precommits
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2)
	signAddVotes(cs1, cmtproto.PrecommitType, theBlockHash, theBlockParts, true, vs3)
	signAddVotes(cs1, cmtproto.PrecommitType, theBlockHash, theBlockParts, true, vs4)

	ensureNewBlockHeader(newBlockHeader, height, theBlockHash)

	prop, propBlock := decideProposal(ctx, t, cs1, vs2, height+1, 0)
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	if err := cs1.SetProposalAndBlock(prop, propBlock, propBlockParts, "some peer"); err != nil {
		t.Fatal(err)
	}
	ensureNewProposal(proposalCh, height+1, 0)

	rs = cs1.GetRoundState()
	assert.False(
		t,
		rs.TriggeredTimeoutPrecommit,
		"triggeredTimeoutPrecommit should be false at the beginning of each height")
}

//------------------------------------------------------------------------------------------
// SlashingSuite
// TODO: Slashing

/*
func TestStateSlashingPrevotes(t *testing.T) {
	cs1, vss := randState(2)
	vs2 := vss[1]


	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	voteCh := subscribeToVoter(cs1, cs1.privValidator.GetAddress())

	// start round and wait for propose and prevote
	startTestRound(cs1, cs1.Height, 0)
	<-newRoundCh
	re := <-proposalCh
	<-voteCh // prevote

	rs := re.(types.EventDataRoundState).RoundState.(*cstypes.RoundState)

	// we should now be stuck in limbo forever, waiting for more prevotes
	// add one for a different block should cause us to go into prevote wait
	hash := rs.ProposalBlock.Hash()
	hash[0] = byte(hash[0]+1) % 255
	signAddVotes(cs1, cmtproto.PrevoteType, hash, rs.ProposalBlockParts.Header(), vs2)

	<-timeoutWaitCh

	// NOTE: we have to send the vote for different block first so we don't just go into precommit round right
	// away and ignore more prevotes (and thus fail to slash!)

	// add the conflicting vote
	signAddVotes(cs1, cmtproto.PrevoteType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), vs2)

	// XXX: Check for existence of Dupeout info
}

func TestStateSlashingPrecommits(t *testing.T) {
	cs1, vss := randState(2)
	vs2 := vss[1]


	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	voteCh := subscribeToVoter(cs1, cs1.privValidator.GetAddress())

	// start round and wait for propose and prevote
	startTestRound(cs1, cs1.Height, 0)
	<-newRoundCh
	re := <-proposalCh
	<-voteCh // prevote

	// add prevote from vs2
	signAddVotes(cs1, cmtproto.PrevoteType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), vs2)

	<-voteCh // precommit

	// we should now be stuck in limbo forever, waiting for more prevotes
	// add one for a different block should cause us to go into prevote wait
	hash := rs.ProposalBlock.Hash()
	hash[0] = byte(hash[0]+1) % 255
	signAddVotes(cs1, cmtproto.PrecommitType, hash, rs.ProposalBlockParts.Header(), vs2)

	// NOTE: we have to send the vote for different block first so we don't just go into precommit round right
	// away and ignore more prevotes (and thus fail to slash!)

	// add precommit from vs2
	signAddVotes(cs1, cmtproto.PrecommitType, rs.ProposalBlock.Hash(), rs.ProposalBlockParts.Header(), vs2)

	// XXX: Check for existence of Dupeout info
}
*/

//------------------------------------------------------------------------------------------
// CatchupSuite

//------------------------------------------------------------------------------------------
// HaltSuite

// 4 vals.
// we receive a final precommit after going into next round, but others might have gone to commit already!
func TestStateHalt1(t *testing.T) {
	cs1, vss := randState(4)
	vs2, vs3, vs4 := vss[1], vss[2], vss[3]
	height, round := cs1.Height, cs1.Round
	partSize := types.BlockPartSizeBytes

	proposalCh := subscribe(cs1.eventBus, types.EventQueryCompleteProposal)
	timeoutWaitCh := subscribe(cs1.eventBus, types.EventQueryTimeoutWait)
	newRoundCh := subscribe(cs1.eventBus, types.EventQueryNewRound)
	newBlockCh := subscribe(cs1.eventBus, types.EventQueryNewBlock)
	pv1, err := cs1.privValidator.GetPubKey()
	require.NoError(t, err)
	addr := pv1.Address()
	voteCh := subscribeToVoter(cs1, addr)

	// start round and wait for propose and prevote
	startTestRound(cs1, height, round)
	ensureNewRound(newRoundCh, height, round)

	ensureNewProposal(proposalCh, height, round)
	rs := cs1.GetRoundState()
	propBlock := rs.ProposalBlock
	propBlockParts, err := propBlock.MakePartSet(partSize)
	require.NoError(t, err)

	ensurePrevote(voteCh, height, round)

	signAddVotes(cs1, cmtproto.PrevoteType, propBlock.Hash(), propBlockParts.Header(), false, vs2, vs3, vs4)

	ensurePrecommit(voteCh, height, round)
	// the proposed block should now be locked and our precommit added
	validatePrecommit(t, cs1, round, round, vss[0], propBlock.Hash(), propBlock.Hash())

	// add precommits from the rest
	signAddVotes(cs1, cmtproto.PrecommitType, nil, types.PartSetHeader{}, true, vs2) // didnt receive proposal
	signAddVotes(cs1, cmtproto.PrecommitType, propBlock.Hash(), propBlockParts.Header(), true, vs3)
	// we receive this later, but vs3 might receive it earlier and with ours will go to commit!
	precommit4 := signVote(vs4, cmtproto.PrecommitType, propBlock.Hash(), propBlockParts.Header(), true)

	incrementRound(vs2, vs3, vs4)

	// timeout to new round
	ensureNewTimeout(timeoutWaitCh, height, round, cs1.config.Precommit(round).Nanoseconds())

	round++ // moving to the next round

	ensureNewRound(newRoundCh, height, round)
	rs = cs1.GetRoundState()

	t.Log("### ONTO ROUND 1")
	/*Round2
	// we timeout and prevote our lock
	// a polka happened but we didn't see it!
	*/

	// go to prevote, prevote for locked block
	ensurePrevote(voteCh, height, round)
	validatePrevote(t, cs1, round, vss[0], rs.LockedBlock.Hash())

	// now we receive the precommit from the previous round
	addVotes(cs1, precommit4)

	// receiving that precommit should take us straight to commit
	ensureNewBlock(newBlockCh, height)

	ensureNewRound(newRoundCh, height+1, 0)
}

func TestStateOutputsBlockPartsStats(t *testing.T) {
	// create dummy peer
	cs, _ := randState(1)
	peer := p2pmock.NewPeer(nil)

	// 1) new block part
	parts, err := types.NewPartSetFromData(cmtrand.Bytes(100), 10)
	require.NoError(t, err)
	msg := &BlockPartMessage{
		Height: 1,
		Round:  0,
		Part:   parts.GetPart(0),
	}

	cs.ProposalBlockParts = types.NewPartSetFromHeader(parts.Header())
	cs.handleMsg(msgInfo{msg, peer.ID()})

	statsMessage := <-cs.statsMsgQueue
	require.Equal(t, msg, statsMessage.Msg, "")
	require.Equal(t, peer.ID(), statsMessage.PeerID, "")

	// sending the same part from different peer
	cs.handleMsg(msgInfo{msg, "peer2"})

	// sending the part with the same height, but different round
	msg.Round = 1
	cs.handleMsg(msgInfo{msg, peer.ID()})

	// sending the part from the smaller height
	msg.Height = 0
	cs.handleMsg(msgInfo{msg, peer.ID()})

	// sending the part from the bigger height
	msg.Height = 3
	cs.handleMsg(msgInfo{msg, peer.ID()})

	select {
	case <-cs.statsMsgQueue:
		t.Errorf("should not output stats message after receiving the known block part!")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStateOutputVoteStats(t *testing.T) {
	cs, vss := randState(2)
	// create dummy peer
	peer := p2pmock.NewPeer(nil)

	randBytes := cmtrand.Bytes(tmhash.Size)

	vote := signVote(vss[1], cmtproto.PrecommitType, randBytes, types.PartSetHeader{}, true)

	voteMessage := &VoteMessage{vote}
	cs.handleMsg(msgInfo{voteMessage, peer.ID()})

	statsMessage := <-cs.statsMsgQueue
	require.Equal(t, voteMessage, statsMessage.Msg, "")
	require.Equal(t, peer.ID(), statsMessage.PeerID, "")

	// sending the same part from different peer
	cs.handleMsg(msgInfo{&VoteMessage{vote}, "peer2"})

	// sending the vote for the bigger height
	incrementHeight(vss[1])
	vote = signVote(vss[1], cmtproto.PrecommitType, randBytes, types.PartSetHeader{}, true)

	cs.handleMsg(msgInfo{&VoteMessage{vote}, peer.ID()})

	select {
	case <-cs.statsMsgQueue:
		t.Errorf("should not output stats message after receiving the known vote or vote from bigger height")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSignSameVoteTwice(t *testing.T) {
	_, vss := randState(2)

	randBytes := cmtrand.Bytes(tmhash.Size)

	vote := signVote(vss[1],
		cmtproto.PrecommitType,
		randBytes,
		types.PartSetHeader{Total: 10, Hash: randBytes},
		true,
	)

	vote2 := signVote(vss[1],
		cmtproto.PrecommitType,
		randBytes,
		types.PartSetHeader{Total: 10, Hash: randBytes},
		true,
	)

	require.Equal(t, vote, vote2)
}

// subscribe subscribes test client to the given query and returns a channel with cap = 1.
func subscribe(eventBus *types.EventBus, q cmtpubsub.Query) <-chan cmtpubsub.Message {
	sub, err := eventBus.Subscribe(context.Background(), testSubscriber, q)
	if err != nil {
		panic(fmt.Sprintf("failed to subscribe %s to %v", testSubscriber, q))
	}
	return sub.Out()
}

// subscribe subscribes test client to the given query and returns a channel with cap = 0.
func subscribeUnBuffered(eventBus *types.EventBus, q cmtpubsub.Query) <-chan cmtpubsub.Message {
	sub, err := eventBus.SubscribeUnbuffered(context.Background(), testSubscriber, q)
	if err != nil {
		panic(fmt.Sprintf("failed to subscribe %s to %v", testSubscriber, q))
	}
	return sub.Out()
}

func signAddPrecommitWithExtension(
	t *testing.T,
	cs *State,
	hash []byte,
	header types.PartSetHeader,
	extension []byte,
	stub *validatorStub,
) {
	v, err := stub.signVote(cmtproto.PrecommitType, hash, header, extension, true)
	require.NoError(t, err, "failed to sign vote")
	addVotes(cs, v)
}

func findBlockSizeLimit(t *testing.T, height, maxBytes int64, cs *State, _ uint32, oversized bool) (*types.Block, *types.PartSet) {
	var offset int64
	if !oversized {
		offset = -2
	}
	softMaxDataBytes := int(types.MaxDataBytes(maxBytes, 0, 0))
	for i := softMaxDataBytes; i < softMaxDataBytes*2; i++ {
		propBlock, propBlockParts, err := cs.state.MakeBlock(
			height,
			types.MakeData([]types.Tx{[]byte("a=" + strings.Repeat("o", i-2))}),
			&types.Commit{},
			nil,
			cs.privValidatorPubKey.Address(),
		)
		require.NoError(t, err)
		if propBlockParts.ByteSize() > maxBytes+offset {
			s := "real max"
			if oversized {
				s = "off-by-1"
			}
			t.Log("Detected "+s+" data size for block;", "size", i, "softMaxDataBytes", softMaxDataBytes)
			return propBlock, propBlockParts
		}
	}
	require.Fail(t, "We shouldn't hit the end of the loop")
	return nil, nil
}
