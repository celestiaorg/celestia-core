package schema

import (
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/pkg/trace"
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
	Height int64                 `json:"height"`
	Round  int32                 `json:"round"`
	Step   cstypes.RoundStepType `json:"step"`
}

// Table returns the table name for the RoundState struct.
func (r RoundState) Table() string {
	return RoundStateTable
}

// InfluxRepr returns the InfluxDB representation of the RoundState struct.
func (r RoundState) InfluxRepr() (map[string]interface{}, error) {
	return toMap(r)
}

// WriteRoundState writes a tracing point for a tx using the predetermined
// schema for consensus state tracing.
func WriteRoundState(client trace.Tracer, height int64, round int32, step cstypes.RoundStepType) {
	client.Write(RoundState{Height: height, Round: round, Step: step})
}

// Schema constants for the "consensus_block_parts" table.
const (
	// BlockPartsTable is the name of the table that stores the consensus block
	// parts.
	BlockPartsTable = "consensus_block_parts"
)

// BlockPart describes schema for the "consensus_block_parts" table.
type BlockPart struct {
	Height       int64        `json:"height"`
	Round        int32        `json:"round"`
	Index        int32        `json:"index"`
	Peer         p2p.ID       `json:"peer"`
	TransferType TransferType `json:"transfer_type"`
}

// Table returns the table name for the BlockPart struct.
func (b BlockPart) Table() string {
	return BlockPartsTable
}

// InfluxRepr returns the InfluxDB representation of the BlockPart struct.
func (b BlockPart) InfluxRepr() (map[string]interface{}, error) {
	return toMap(b)
}

// WriteBlockPart writes a tracing point for a BlockPart using the predetermined
// schema for consensus state tracing.
func WriteBlockPart(
	client trace.Tracer,
	height int64,
	round int32,
	peer p2p.ID,
	index uint32,
	transferType TransferType,
) {
	// this check is redundant to what is checked during WritePoint, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(BlockPartsTable) {
		return
	}
	client.Write(BlockPart{
		Height:       height,
		Round:        round,
		Index:        int32(index),
		Peer:         peer,
		TransferType: transferType,
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
	Height                   int64        `json:"height"`
	Round                    int32        `json:"round"`
	VoteType                 string       `json:"vote_type"`
	VoteHeight               int64        `json:"vote_height"`
	VoteRound                int32        `json:"vote_round"`
	VoteMillisecondTimestamp int64        `json:"vote_unix_millisecond_timestamp"`
	ValidatorAddress         string       `json:"vote_validator_address"`
	Peer                     p2p.ID       `json:"peer"`
	TransferType             TransferType `json:"transfer_type"`
}

func (v Vote) Table() string {
	return VoteTable
}

func (v Vote) InfluxRepr() (map[string]interface{}, error) {
	return toMap(v)
}

// WriteVote writes a tracing point for a vote using the predetermined
// schema for consensus vote tracing.
func WriteVote(client trace.Tracer,
	height int64, // height of the current peer when it received/sent the vote
	round int32, // round of the current peer when it received/sent the vote
	vote *types.Vote, // vote received by the current peer
	peer p2p.ID, // the peer from which it received the vote or the peer to which it sent the vote
	transferType TransferType, // download (received) or upload(sent)
) {
	client.Write(Vote{
		Height:                   height,
		Round:                    round,
		VoteType:                 vote.Type.String(),
		VoteHeight:               vote.Height,
		VoteRound:                vote.Round,
		VoteMillisecondTimestamp: vote.Timestamp.UnixMilli(),
		ValidatorAddress:         vote.ValidatorAddress.String(),
		Peer:                     peer,
		TransferType:             transferType,
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

func (b BlockSummary) Table() string {
	return BlockTable
}

func (b BlockSummary) InfluxRepr() (map[string]interface{}, error) {
	return toMap(b)
}

// WriteBlockSummary writes a tracing point for a block using the predetermined
func WriteBlockSummary(client trace.Tracer, block *types.Block, size int) {
	client.Write(BlockSummary{
		Height:                   block.Height,
		UnixMillisecondTimestamp: block.Time.UnixMilli(),
		TxCount:                  len(block.Data.Txs),
		SquareSize:               block.SquareSize,
		BlockSize:                size,
		Proposer:                 block.ProposerAddress.String(),
		LastCommitRound:          block.LastCommit.Round,
	})
}
