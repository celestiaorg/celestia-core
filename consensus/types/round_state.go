package types

import (
	"encoding/json"
	"fmt"
	"github.com/cometbft/cometbft/libs/sync"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/types"
)

//-----------------------------------------------------------------------------
// RoundStepType enum type

// RoundStepType enumerates the state of the consensus state machine
type RoundStepType uint8 // These must be numeric, ordered.

// RoundStepType
const (
	RoundStepNewHeight     = RoundStepType(0x01) // Wait til CommitTime + timeoutCommit
	RoundStepNewRound      = RoundStepType(0x02) // Setup new round and go to RoundStepPropose
	RoundStepPropose       = RoundStepType(0x03) // Did propose, gossip proposal
	RoundStepPrevote       = RoundStepType(0x04) // Did prevote, gossip prevotes
	RoundStepPrevoteWait   = RoundStepType(0x05) // Did receive any +2/3 prevotes, start timeout
	RoundStepPrecommit     = RoundStepType(0x06) // Did precommit, gossip precommits
	RoundStepPrecommitWait = RoundStepType(0x07) // Did receive any +2/3 precommits, start timeout
	RoundStepCommit        = RoundStepType(0x08) // Entered commit state machine
	// NOTE: RoundStepNewHeight acts as RoundStepCommitWait.

	// NOTE: Update IsValid method if you change this!
)

// IsValid returns true if the step is valid, false if unknown/undefined.
func (rs RoundStepType) IsValid() bool {
	return uint8(rs) >= 0x01 && uint8(rs) <= 0x08
}

// String returns a string
func (rs RoundStepType) String() string {
	switch rs {
	case RoundStepNewHeight:
		return "RoundStepNewHeight"
	case RoundStepNewRound:
		return "RoundStepNewRound"
	case RoundStepPropose:
		return "RoundStepPropose"
	case RoundStepPrevote:
		return "RoundStepPrevote"
	case RoundStepPrevoteWait:
		return "RoundStepPrevoteWait"
	case RoundStepPrecommit:
		return "RoundStepPrecommit"
	case RoundStepPrecommitWait:
		return "RoundStepPrecommitWait"
	case RoundStepCommit:
		return "RoundStepCommit"
	default:
		return "RoundStepUnknown" // Cannot panic.
	}
}

//-----------------------------------------------------------------------------

// RoundState defines the internal consensus state.
// NOTE: Not thread safe. Should only be manipulated by functions downstream
// of the cs.receiveRoutine
type RoundState struct {
	mtx       sync.RWMutex
	Height    atomic.Int64  `json:"height"` // Height we are working on
	Round     atomic.Int32  `json:"round"`
	Step      RoundStepType `json:"step"`
	StartTime time.Time     `json:"start_time"`

	// Subjective time when +2/3 precommits for Block at Round were found
	CommitTime         time.Time           `json:"commit_time"`
	Validators         *types.ValidatorSet `json:"validators"`
	Proposal           *types.Proposal     `json:"proposal"`
	ProposalBlock      *types.Block        `json:"proposal_block"`
	ProposalBlockParts *types.PartSet      `json:"proposal_block_parts"`
	LockedRound        atomic.Int32        `json:"locked_round"`
	LockedBlock        *types.Block        `json:"locked_block"`
	LockedBlockParts   *types.PartSet      `json:"locked_block_parts"`

	// The variables below starting with "Valid..." derive their name from
	// the algorithm presented in this paper:
	// [The latest gossip on BFT consensus](https://arxiv.org/abs/1807.04938).
	// Therefore, "Valid...":
	//   * means that the block or round that the variable refers to has
	//     received 2/3+ non-`nil` prevotes (a.k.a. a *polka*)
	//   * has nothing to do with whether the Application returned "Accept" in its
	//     response to `ProcessProposal`, or "Reject"

	// Last known round with POL for non-nil valid block.
	ValidRound atomic.Int32 `json:"valid_round"`
	ValidBlock *types.Block `json:"valid_block"` // Last known block of POL mentioned above.

	// Last known block parts of POL mentioned above.
	ValidBlockParts           *types.PartSet      `json:"valid_block_parts"`
	Votes                     *HeightVoteSet      `json:"votes"`
	CommitRound               atomic.Int32        `json:"commit_round"` //
	LastCommit                *types.VoteSet      `json:"last_commit"`  // Last precommits at Height-1
	LastValidators            *types.ValidatorSet `json:"last_validators"`
	TriggeredTimeoutPrecommit atomic.Bool         `json:"triggered_timeout_precommit"`
}

func (rs *RoundState) GetStep() RoundStepType {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.Step
}

func (rs *RoundState) SetStep(step RoundStepType) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.Step = step
}

func (rs *RoundState) GetStartTime() time.Time {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.StartTime
}

func (rs *RoundState) SetStartTime(t time.Time) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.StartTime = t
}

func (rs *RoundState) GetCommitTime() time.Time {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.CommitTime
}

func (rs *RoundState) SetCommitTime(t time.Time) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.CommitTime = t
}

func (rs *RoundState) GetValidators() *types.ValidatorSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.Validators
}

func (rs *RoundState) SetValidators(validators *types.ValidatorSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.Validators = validators
}

func (rs *RoundState) GetProposal() *types.Proposal {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.Proposal
}

func (rs *RoundState) SetProposal(proposal *types.Proposal) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.Proposal = proposal
}

func (rs *RoundState) GetProposalBlock() *types.Block {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.ProposalBlock
}

func (rs *RoundState) SetProposalBlock(block *types.Block) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.ProposalBlock = block
}

func (rs *RoundState) GetProposalBlockParts() *types.PartSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.ProposalBlockParts
}

func (rs *RoundState) SetProposalBlockParts(parts *types.PartSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.ProposalBlockParts = parts
}

func (rs *RoundState) GetLockedBlock() *types.Block {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.LockedBlock
}

func (rs *RoundState) SetLockedBlock(block *types.Block) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.LockedBlock = block
}

func (rs *RoundState) GetLockedBlockParts() *types.PartSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.LockedBlockParts
}

func (rs *RoundState) SetLockedBlockParts(parts *types.PartSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.LockedBlockParts = parts
}

func (rs *RoundState) GetValidBlock() *types.Block {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.ValidBlock
}

func (rs *RoundState) SetValidBlock(block *types.Block) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.ValidBlock = block
}

func (rs *RoundState) GetValidBlockParts() *types.PartSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.ValidBlockParts
}

func (rs *RoundState) SetValidBlockParts(parts *types.PartSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.ValidBlockParts = parts
}

func (rs *RoundState) GetVotes() *HeightVoteSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.Votes
}

func (rs *RoundState) SetVotes(votes *HeightVoteSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.Votes = votes
}

func (rs *RoundState) GetLastCommit() *types.VoteSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.LastCommit
}

func (rs *RoundState) SetLastCommit(commit *types.VoteSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.LastCommit = commit
}

func (rs *RoundState) GetLastValidators() *types.ValidatorSet {
	rs.mtx.RLock()
	defer rs.mtx.RUnlock()
	return rs.LastValidators
}

func (rs *RoundState) SetLastValidators(validators *types.ValidatorSet) {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	rs.LastValidators = validators
}

// Compressed version of the RoundState for use in RPC
type RoundStateSimple struct {
	HeightRoundStep   string              `json:"height/round/step"`
	StartTime         time.Time           `json:"start_time"`
	ProposalBlockHash bytes.HexBytes      `json:"proposal_block_hash"`
	LockedBlockHash   bytes.HexBytes      `json:"locked_block_hash"`
	ValidBlockHash    bytes.HexBytes      `json:"valid_block_hash"`
	Votes             json.RawMessage     `json:"height_vote_set"`
	Proposer          types.ValidatorInfo `json:"proposer"`
}

// Compress the RoundState to RoundStateSimple
func (rs *RoundState) RoundStateSimple() RoundStateSimple {
	votesJSON, err := rs.GetVotes().MarshalJSON()
	if err != nil {
		panic(err)
	}

	addr := rs.GetValidators().GetProposer().Address
	idx, _ := rs.GetValidators().GetByAddress(addr)

	return RoundStateSimple{
		HeightRoundStep:   fmt.Sprintf("%d/%d/%d", rs.Height.Load(), rs.Round.Load(), rs.GetStep()),
		StartTime:         rs.GetStartTime(),
		ProposalBlockHash: rs.GetProposalBlock().Hash(),
		LockedBlockHash:   rs.GetLockedBlock().Hash(),
		ValidBlockHash:    rs.GetValidBlock().Hash(),
		Votes:             votesJSON,
		Proposer: types.ValidatorInfo{
			Address: addr,
			Index:   idx,
		},
	}
}

// NewRoundEvent returns the RoundState with proposer information as an event.
func (rs *RoundState) NewRoundEvent() types.EventDataNewRound {
	addr := rs.GetValidators().GetProposer().Address
	idx, _ := rs.GetValidators().GetByAddress(addr)

	return types.EventDataNewRound{
		Height: rs.Height.Load(),
		Round:  rs.Round.Load(),
		Step:   rs.GetStep().String(),
		Proposer: types.ValidatorInfo{
			Address: addr,
			Index:   idx,
		},
	}
}

// CompleteProposalEvent returns information about a proposed block as an event.
func (rs *RoundState) CompleteProposalEvent() types.EventDataCompleteProposal {
	// We must construct BlockID from ProposalBlock and ProposalBlockParts
	// cs.Proposal is not guaranteed to be set when this function is called
	blockID := types.BlockID{
		Hash:          rs.GetProposalBlock().Hash(),
		PartSetHeader: rs.GetProposalBlockParts().Header(),
	}

	return types.EventDataCompleteProposal{
		Height:  rs.Height.Load(),
		Round:   rs.Round.Load(),
		Step:    rs.GetStep().String(),
		BlockID: blockID,
	}
}

// RoundStateEvent returns the H/R/S of the RoundState as an event.
func (rs *RoundState) RoundStateEvent() types.EventDataRoundState {
	return types.EventDataRoundState{
		Height: rs.Height.Load(),
		Round:  rs.Round.Load(),
		Step:   rs.GetStep().String(),
	}
}

// String returns a string
func (rs *RoundState) String() string {
	return rs.StringIndented("")
}

// StringIndented returns a string
func (rs *RoundState) StringIndented(indent string) string {
	return fmt.Sprintf(`RoundState{
%s  H:%v R:%v S:%v
%s  StartTime:     %v
%s  CommitTime:    %v
%s  Validators:    %v
%s  Proposal:      %v
%s  ProposalBlock: %v %v
%s  LockedRound:   %v
%s  LockedBlock:   %v %v
%s  ValidRound:    %v
%s  ValidBlock:    %v %v
%s  Votes:         %v
%s  LastCommit:    %v
%s  LastValidators:%v
%s}`,
		indent, rs.Height.Load(), rs.Round.Load(), rs.GetStep(),
		indent, rs.GetStartTime(),
		indent, rs.GetCommitTime(),
		indent, rs.GetValidators().StringIndented(indent+"  "),
		indent, rs.GetProposal(),
		indent, rs.GetProposalBlockParts().StringShort(), rs.GetProposalBlock().StringShort(),
		indent, rs.LockedRound.Load(),
		indent, rs.GetLockedBlockParts().StringShort(), rs.GetLockedBlock().StringShort(),
		indent, rs.ValidRound.Load(),
		indent, rs.GetValidBlockParts().StringShort(), rs.GetValidBlock().StringShort(),
		indent, rs.GetVotes().StringIndented(indent+"  "),
		indent, rs.GetLastCommit().StringShort(),
		indent, rs.GetLastValidators().StringIndented(indent+"  "),
		indent)
}

// StringShort returns a string
func (rs *RoundState) StringShort() string {
	return fmt.Sprintf(`RoundState{H:%v R:%v S:%v ST:%v}`,
		rs.Height.Load(), rs.Round.Load(), rs.GetStep(), rs.GetStartTime())
}
