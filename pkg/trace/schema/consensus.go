package schema

import (
	cstypes "github.com/tendermint/tendermint/consensus/types"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/types"
)

// ConsensusTables returns the list of tables that are used for consensus
// tracing.
func ConsensusTables() []string {
	return []string{
		RoundStateTable,
		BlockPartsTable,
		BlockTable,
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

	// BlockPartSizeFieldKey is the name of the field that stores the size of a block
	// part in bytes. The value is an int64.
	// Negative value indicates unknown block part size.
	BlockPartSizeFieldKey = "block_part_size"
)

// WriteBlockPart writes a tracing point for a BlockPart using the predetermined
// schema for consensus state tracing. This is used to create a table in the
// following schema:
//
// | time | height | round | peer | index | transfer type | block_part_size |
// Negative value for `size` i.e., block_part_size indicates unknown block part
// size.
func WriteBlockPart(
	client *trace.Client,
	height int64,
	round int32,
	peer p2p.ID,
	index uint32,
	transferType string,
	size int64,
) {
	// this check is redundant to what is checked during WritePoint, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(BlockPartsTable) {
		return
	}
	client.WritePoint(BlockPartsTable, map[string]interface{}{
		HeightFieldKey:         height,
		RoundFieldKey:          round,
		PeerFieldKey:           peer,
		BlockPartIndexFieldKey: index,
		TransferTypeFieldKey:   transferType,
		BlockPartSizeFieldKey:  size,
	})
}

const (
	// BlockTable is the name of the table that stores metadata about consensus blocks.
	// following schema:
	//
	//  | time  | height | unix_millisecond_timestamp | tx_count | square_size | block_size | proposer | last_commit_round |
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
