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
	// state traces. Follows this schema:
	//
	// | time | height | round | step |
	RoundStateTable = "consensus_round_state"

	// StepFieldKey is the name of the field that stores the consensus step. The
	// value is a string.
	StepFieldKey = "step"
)

// WriteRoundState writes a tracing point for a tx using the predetermined
// schema for consensus state tracing. This is used to create a table in the following
// schema:
//
// | time | height | round | step |
func WriteRoundState(client *trace.Client, height int64, round int32, step cstypes.RoundStepType) {
	client.WritePoint(RoundStateTable, map[string]interface{}{
		HeightFieldKey: height,
		RoundFieldKey:  round,
		StepFieldKey:   step.String(),
	})
}

// Schema constants for the "consensus_block_parts" table.
const (
	// BlockPartsTable is the name of the table that stores the consensus block
	// parts.
	// following schema:
	//
	// | time | height | round | index | peer | transfer type |
	BlockPartsTable = "consensus_block_parts"

	// BlockPartIndexFieldKey is the name of the field that stores the block
	// part
	BlockPartIndexFieldKey = "index"
)

// WriteBlockPart writes a tracing point for a BlockPart using the predetermined
// schema for consensus state tracing. This is used to create a table in the
// following schema:
//
// | time | height | round | index | peer | transfer type |
func WriteBlockPart(
	client *trace.Client,
	height int64,
	round int32,
	peer p2p.ID,
	index uint32,
	transferType string,
) {
	// this check is redundant to what is checked during WritePoint, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(BlockPartsTable) {
		return
	}
	client.WritePoint(BlockPartsTable, map[string]interface{}{
		HeightFieldKey:         height,
		RoundFieldKey:          round,
		BlockPartIndexFieldKey: index,
		PeerFieldKey:           peer,
		TransferTypeFieldKey:   transferType,
	})
}

const (
	// BlockTable is the name of the table that stores metadata about consensus blocks.
	// following schema:
	//
	//  | time  | height | timestamp |
	BlockTable = "consensus_block"

	// UnixMillisecondTimestampFieldKey is the name of the field that stores the timestamp in
	// the last commit in unix milliseconds.
	UnixMillisecondTimestampFieldKey = "unix_millisecond_timestamp"

	// TxCountFieldKey is the name of the field that stores the number of
	// transactions in the block.
	TxCountFieldKey = "tx_count"

	// SquareSizeFieldKey is the name of the field that stores the square size
	// of the block. SquareSize is the number of shares in a single row or
	// column of the original data square.
	SquareSizeFieldKey = "square_size"

	// BlockSizeFieldKey is the name of the field that stores the size of
	// the block data in bytes.
	BlockSizeFieldKey = "block_size"

	// ProposerFieldKey is the name of the field that stores the proposer of
	// the block.
	ProposerFieldKey = "proposer"

	// LastCommitRoundFieldKey is the name of the field that stores the round
	// of the last commit.
	LastCommitRoundFieldKey = "last_commit_round"
)

func WriteBlock(client *trace.Client, block *types.Block, size int) {
	client.WritePoint(BlockTable, map[string]interface{}{
		HeightFieldKey:                   block.Height,
		UnixMillisecondTimestampFieldKey: block.Time.UnixMilli(),
		TxCountFieldKey:                  len(block.Data.Txs),
		SquareSizeFieldKey:               block.SquareSize,
		BlockSizeFieldKey:                size,
		ProposerFieldKey:                 block.ProposerAddress.String(),
		LastCommitRoundFieldKey:          block.LastCommit.Round,
	})
}

// Schema constants for the consensus votes tracing database.
const (
	// VoteTable is the name of the table that stores the consensus
	// voting traces. Follows this schema:
	//
	// | time | height | round | vote_type | vote_height | vote_round
	// | vote_block_id| vote_timestamp
	// | vote_validator_address | vote_validator_index | peer
	// | transfer_type |
	VoteTable = "consensus_vote"

	VoteTypeFieldKey         = "vote_type"
	VoteHeightFieldKey       = "vote_height"
	VoteRoundFieldKey        = "vote_round"
	VoteBlockIDFieldKey      = "vote_block_id"
	VoteTimestampFieldKey    = "vote_timestamp"
	ValidatorAddressFieldKey = "vote_validator_address"
	ValidatorIndexFieldKey   = "vote_validator_index"

	//	Type             cmtproto.SignedMsgType `json:"type"`
	//	Height           int64                  `json:"height"`
	//	Round            int32                  `json:"round"`    // assume there will not be greater than 2_147_483_647 rounds
	//	BlockID          BlockID                `json:"block_id"` // zero if vote is nil.
	//	Timestamp        time.Time              `json:"timestamp"`
	//	ValidatorAddress Address                `json:"validator_address"`
	//	ValidatorIndex
)

// WriteVote writes a tracing point for a vote using the predetermined
// schema for consensus vote tracing.
// This is used to create a table in the following
// schema:
//
// | time | height | round | vote_type | vote_height | vote_round
// | vote_block_id| vote_timestamp
// | vote_validator_address | vote_validator_index | peer
// | transfer_type |
func WriteVote(client *trace.Client,
	height int64, // height of the current peer when it received the vote
	round int32, // round of the current peer when it received the vote
	vote *types.Vote, // vote received by the current peer
	peer p2p.ID, // the peer from which it received the vote or the peer to which it sent the vote
	transferType string, // download (received) or upload(sent)
) {
	client.WritePoint(VoteTable, map[string]interface{}{
		HeightFieldKey:           height,
		RoundFieldKey:            round,
		VoteTypeFieldKey:         vote.Type.String(),
		VoteHeightFieldKey:       vote.Height,
		VoteRoundFieldKey:        vote.Round,
		VoteBlockIDFieldKey:      vote.BlockID.Hash.String(),
		VoteTimestampFieldKey:    vote.Timestamp.UnixMilli(),
		ValidatorAddressFieldKey: vote.ValidatorAddress.String(),
		ValidatorIndexFieldKey:   vote.ValidatorIndex,
		PeerFieldKey:             peer,
		TransferTypeFieldKey:     transferType,
	})
}
