package schema

import (
	"fmt"
	"time"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/types"
)

// ConsensusTables returns the list of tables that are used for consensus
// tracing.
func ConsensusTables() []string {
	return []string{
		RoundStateTable,
		BlockPartsTable,
		BlockTable,
		VoteTable,
		ConsensusStateTable,
		ProposalTable,
		GapTable,
		RetriesTable,
		CatchupRequestsTable,
		MissedProposalsTable,
		SigningLatencyTable,
		FullBlockReceivingTimeTable,
		DebugCatchupTable,
	}
}

// Schema constants for the consensus round state tracing database.
const (
	// RoundStateTable is the name of the table that stores the consensus
	// state traces.
	RoundStateTable = "consensus_round_state"
)

// RoundState describes schema for the "consensus_round_state" table.
type RoundState struct {
	Height int64  `json:"height"`
	Round  int32  `json:"round"`
	Step   string `json:"step"`
}

// Table returns the table name for the RoundState struct.
func (RoundState) Table() string {
	return RoundStateTable
}

// WriteRoundState writes a tracing point for a tx using the predetermined
// schema for consensus state tracing.
func WriteRoundState(client trace.Tracer, height int64, round int32, step string) {
	client.Write(RoundState{Height: height, Round: round, Step: step})
}

// Schema constants for the "consensus_block_parts" table.
const (
	// BlockPartsTable is the name of the table that stores the consensus block
	// parts.
	BlockPartsTable = "consensus_block_parts"
)

// BlockPartMessageType distinguishes between different message types in the BlockPartsTable.
type BlockPartMessageType string

const (
	BlockPartMsgTypePart BlockPartMessageType = "part"
	BlockPartMsgTypeHave BlockPartMessageType = "have"
	BlockPartMsgTypeWant BlockPartMessageType = "want"
)

// BlockPart describes schema for the "consensus_block_parts" table.
type BlockPart struct {
	Height       int64                `json:"height"`
	Round        int32                `json:"round"`
	Index        int32                `json:"index"`
	Catchup      bool                 `json:"catchup"`
	Peer         string               `json:"peer"`
	TransferType TransferType         `json:"transfer_type"`
	MessageType  BlockPartMessageType `json:"message_type"`
}

// Table returns the table name for the BlockPart struct.
func (BlockPart) Table() string {
	return BlockPartsTable
}

// WriteBlockPart writes a tracing point for a BlockPart using the predetermined
// schema for consensus state tracing.
func WriteBlockPart(
	client trace.Tracer,
	height int64,
	round int32,
	index uint32,
	catchup bool,
	peer string,
	transferType TransferType,
) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(BlockPartsTable) {
		return
	}
	client.Write(BlockPart{
		Height: height,
		Round:  round,
		//nolint:gosec
		Index:        int32(index),
		Catchup:      catchup,
		Peer:         peer,
		TransferType: transferType,
		MessageType:  BlockPartMsgTypePart,
	})
}

// WriteHave writes a tracing point for a HaveParts message.
func WriteHave(
	client trace.Tracer,
	height int64,
	round int32,
	index uint32,
	peer string,
	transferType TransferType,
) {
	if !client.IsCollecting(BlockPartsTable) {
		return
	}
	client.Write(BlockPart{
		Height: height,
		Round:  round,
		//nolint:gosec
		Index:        int32(index),
		Peer:         peer,
		TransferType: transferType,
		MessageType:  BlockPartMsgTypeHave,
	})
}

// WriteWant writes a tracing point for a WantParts message.
func WriteWant(
	client trace.Tracer,
	height int64,
	round int32,
	index uint32,
	peer string,
	transferType TransferType,
) {
	if !client.IsCollecting(BlockPartsTable) {
		return
	}
	client.Write(BlockPart{
		Height: height,
		Round:  round,
		//nolint:gosec
		Index:        int32(index),
		Peer:         peer,
		TransferType: transferType,
		MessageType:  BlockPartMsgTypeWant,
	})
}

// Schema constants for the consensus votes tracing database.
const (
	// VoteTable is the name of the table that stores the consensus
	// voting traces.
	VoteTable = "consensus_vote"
)

// Vote describes schema for the "consensus_vote" table.
type Vote struct {
	Height           int64        `json:"height"`
	Round            int32        `json:"round"`
	VoteType         string       `json:"vote_type"`
	VoteHeight       int64        `json:"vote_height"`
	VoteRound        int32        `json:"vote_round"`
	VoteTimestamp    time.Time    `json:"vote_timestamp"`
	ValidatorAddress string       `json:"vote_validator_address"`
	Peer             string       `json:"peer"`
	TransferType     TransferType `json:"transfer_type"`
}

func (Vote) Table() string {
	return VoteTable
}

// WriteVote writes a tracing point for a vote using the predetermined
// schema for consensus vote tracing.
func WriteVote(client trace.Tracer,
	height int64, // height of the current peer when it received/sent the vote
	round int32, // round of the current peer when it received/sent the vote
	vote *types.Vote, // vote received by the current peer
	peer string, // the peer from which it received the vote or the peer to which it sent the vote
	transferType TransferType, // download (received) or upload(sent)
) {
	client.Write(Vote{
		Height:           height,
		Round:            round,
		VoteType:         vote.Type.String(),
		VoteHeight:       vote.Height,
		VoteRound:        vote.Round,
		VoteTimestamp:    vote.Timestamp,
		ValidatorAddress: vote.ValidatorAddress.String(),
		Peer:             peer,
		TransferType:     transferType,
	})
}

const (
	// BlockTable is the name of the table that stores metadata about consensus blocks.
	BlockTable = "consensus_block"
)

// BlockSummary describes schema for the "consensus_block" table.
type BlockSummary struct {
	Height                   int64  `json:"height"`
	UnixMillisecondTimestamp int64  `json:"unix_millisecond_timestamp"`
	TxCount                  int    `json:"tx_count"`
	SquareSize               uint64 `json:"square_size"`
	BlockSize                int    `json:"block_size"`
	Proposer                 string `json:"proposer"`
	LastCommitRound          int32  `json:"last_commit_round"`
}

func (BlockSummary) Table() string {
	return BlockTable
}

// WriteBlockSummary writes a tracing point for a block using the predetermined.
func WriteBlockSummary(client trace.Tracer, block *types.Block, size int) {
	client.Write(BlockSummary{
		Height:                   block.Height,
		UnixMillisecondTimestamp: block.Time.UnixMilli(),
		TxCount:                  len(block.Data.Txs), //nolint:staticcheck
		SquareSize:               block.SquareSize,
		BlockSize:                size,
		Proposer:                 block.ProposerAddress.String(),
		LastCommitRound:          block.LastCommit.Round,
	})
}

const (
	ConsensusStateTable = "consensus_state"
)

type ConsensusStateUpdateType string

const (
	ConsensusNewValidBlock      ConsensusStateUpdateType = "new_valid_block"
	ConsensusNewRoundStep       ConsensusStateUpdateType = "new_round_step"
	ConsensusVoteSetBits        ConsensusStateUpdateType = "vote_set_bits"
	ConsensusVoteSet23Prevote   ConsensusStateUpdateType = "vote_set_23_prevote"
	ConsensusVoteSet23Precommit ConsensusStateUpdateType = "vote_set_23_precommit"
	ConsensusHasVote            ConsensusStateUpdateType = "has_vote"
	ConsensusPOL                ConsensusStateUpdateType = "pol"
)

type ConsensusState struct {
	Height       int64        `json:"height"`
	Round        int32        `json:"round"`
	UpdateType   string       `json:"update_type"`
	Peer         string       `json:"peer"`
	TransferType TransferType `json:"transfer_type"`
	Data         []string     `json:"data,omitempty"`
}

func (ConsensusState) Table() string {
	return ConsensusStateTable
}

func WriteConsensusState(
	client trace.Tracer,
	height int64,
	round int32,
	peer string,
	updateType ConsensusStateUpdateType,
	transferType TransferType,
	data ...string,
) {
	client.Write(ConsensusState{
		Height:       height,
		Round:        round,
		Peer:         peer,
		UpdateType:   string(updateType),
		TransferType: transferType,
		Data:         data,
	})
}

const (
	ProposalTable = "consensus_proposal"
)

type Proposal struct {
	Height       int64        `json:"height"`
	Round        int32        `json:"round"`
	PeerID       string       `json:"peer_id"`
	TransferType TransferType `json:"transfer_type"`
}

func (Proposal) Table() string {
	return ProposalTable
}

func WriteProposal(
	client trace.Tracer,
	height int64,
	round int32,
	peerID string,
	transferType TransferType,
) {
	client.Write(Proposal{
		Height:       height,
		Round:        round,
		PeerID:       peerID,
		TransferType: transferType,
	})
}


const (
	NotesTable = "notes"
)

type Note struct {
	Note     string `json:"note"`
	Height   int64  `json:"height"`
	Round    int32  `json:"round"`
	NoteType string `json:"note_type"`
}

func (p Note) Table() string {
	return NotesTable
}

func WriteNote(
	client trace.Tracer,
	height int64,
	round int32,
	noteType string,
	note string,
	items ...interface{},
) {
	if !client.IsCollecting(NotesTable) {
		return
	}

	client.Write(Note{
		Height:   height,
		Round:    round,
		Note:     fmt.Sprintf(note, items...),
		NoteType: noteType,
	})
}

const (
	CatchupRequestsTable = "catch_reqs"
)

type CatchupRequest struct {
	Height int64  `json:"height"`
	Round  int32  `json:"round"`
	Parts  string `json:"parts"`
	Peer   string `json:"peer"`
}

func (b CatchupRequest) Table() string {
	return CatchupRequestsTable
}

func WriteCatchupRequest(
	client trace.Tracer,
	height int64,
	round int32,
	parts string,
	peer string,
) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(CatchupRequestsTable) {
		return
	}
	client.Write(CatchupRequest{
		Height: height,
		Round:  round,
		Parts:  parts,
		Peer:   peer,
	})
}

const (
	RetriesTable = "retries"
)

type Retries struct {
	Height  int64  `json:"height"`
	Round   int32  `json:"round"`
	Missing string `json:"missing"`
}

func (b Retries) Table() string {
	return RetriesTable
}

func WriteRetries(
	client trace.Tracer,
	height int64,
	round int32,
	missing string,
) {
	client.Write(Retries{
		Height:  height,
		Round:   round,
		Missing: missing,
	})
}

const (
	GapTable = "gap"
)

type Gap struct {
	Height int64 `json:"height"`
	Round  int32 `json:"round"`
}

func (b Gap) Table() string {
	return GapTable
}

func WriteGap(
	client trace.Tracer,
	height int64,
	round int32,
) {
	client.Write(Gap{
		Height: height,
		Round:  round,
	})
}

const (
	MissedProposalsTable = "missed_proposals"
)

type MissedProposal struct {
	Height   int64  `json:"height"`
	Round    int32  `json:"round"`
	Proposer string `json:"proposer"`
}

func (b MissedProposal) Table() string {
	return MissedProposalsTable
}

func WriteMissedProposal(
	client trace.Tracer,
	height int64,
	round int32,
	proposerAddress string,
) {
	client.Write(MissedProposal{
		Height:   height,
		Round:    round,
		Proposer: proposerAddress,
	})
}

const (
	SigningLatencyTable = "signing_latency"
	PrecommitType       = "precommit"
	PrevoteType         = "prevote"
	ProposalType        = "proposal"
)

type SignatureLatency struct {
	Height      int64  `json:"height"`
	Round       int32  `json:"round"`
	Latency     int64  `json:"latency"`
	MessageType string `json:"message_type"`
}

func (b SignatureLatency) Table() string {
	return SigningLatencyTable
}

func WriteSignatureLatency(
	client trace.Tracer,
	height int64,
	round int32,
	latency int64,
	msgType string,
) {
	client.Write(SignatureLatency{
		Height:      height,
		Round:       round,
		Latency:     latency,
		MessageType: msgType,
	})
}

const (
	FullBlockReceivingTimeTable = "full_block_receiving_time"
)

type FullBlockReceivingTime struct {
	Height int64     `json:"height"`
	Round  int32     `json:"round"`
	Time   time.Time `json:"time"`
	// Complete set to false when we receive the proposal. Set to true when the full block is received
	Complete bool `json:"complete"`
}

func (b FullBlockReceivingTime) Table() string {
	return FullBlockReceivingTimeTable
}

func WriteFullBlockReceivingTime(
	client trace.Tracer,
	height int64,
	round int32,
	t time.Time,
	complete bool,
) {
	client.Write(FullBlockReceivingTime{
		Height:   height,
		Round:    round,
		Time:     t,
		Complete: complete,
	})
}

const (
	// DebugCatchupTable stores events related to debug-induced catchup testing.
	DebugCatchupTable = "debug_catchup"
)

// DebugHaltEvent records when consensus is halted for debugging.
type DebugHaltEvent struct {
	// Height is the height at which the halt was initiated
	Height int64 `json:"height"`
	// Round is the round at which the halt was initiated
	Round int32 `json:"round"`
}

func (DebugHaltEvent) Table() string {
	return DebugCatchupTable
}

// WriteDebugHalt writes a trace event when consensus is halted for debugging.
func WriteDebugHalt(client trace.Tracer, height int64, round int32) {
	if !client.IsCollecting(DebugCatchupTable) {
		return
	}
	client.Write(DebugHaltEvent{
		Height: height,
		Round:  round,
	})
}

// DebugUnlockEvent records when consensus is unlocked after a debug halt.
type DebugUnlockEvent struct {
	// HaltHeight is the height at which the node was halted
	HaltHeight int64 `json:"halt_height"`
	// CurrentHeight is the chain height when unlock occurs
	CurrentHeight int64 `json:"current_height"`
	// HaltDurationNs is how long the node was halted in nanoseconds
	HaltDurationNs int64 `json:"halt_duration_ns"`
}

func (DebugUnlockEvent) Table() string {
	return DebugCatchupTable
}

// WriteDebugUnlock writes a trace event when consensus is unlocked.
func WriteDebugUnlock(
	client trace.Tracer,
	haltHeight int64,
	currentHeight int64,
	haltDurationNs int64,
) {
	if !client.IsCollecting(DebugCatchupTable) {
		return
	}
	client.Write(DebugUnlockEvent{
		HaltHeight:     haltHeight,
		CurrentHeight:  currentHeight,
		HaltDurationNs: haltDurationNs,
	})
}

// DebugCatchupComplete records when a node catches up to the tip after a debug halt.
type DebugCatchupComplete struct {
	// HaltHeight is the height at which the node was originally halted
	HaltHeight int64 `json:"halt_height"`
	// CurrentHeight is the height at which catchup completed (the tip)
	CurrentHeight int64 `json:"current_height"`
	// CatchupDurationNs is how long it took to catch up in nanoseconds
	CatchupDurationNs int64 `json:"catchup_duration_ns"`
	// BlocksCaughtUp is the number of blocks that were caught up
	BlocksCaughtUp int64 `json:"blocks_caught_up"`
}

func (DebugCatchupComplete) Table() string {
	return DebugCatchupTable
}

// WriteDebugCatchupComplete writes a trace event when catchup completes.
func WriteDebugCatchupComplete(
	client trace.Tracer,
	haltHeight int64,
	currentHeight int64,
	catchupDurationNs int64,
	blocksCaughtUp int64,
) {
	if !client.IsCollecting(DebugCatchupTable) {
		return
	}
	client.Write(DebugCatchupComplete{
		HaltHeight:        haltHeight,
		CurrentHeight:     currentHeight,
		CatchupDurationNs: catchupDurationNs,
		BlocksCaughtUp:    blocksCaughtUp,
	})
}
