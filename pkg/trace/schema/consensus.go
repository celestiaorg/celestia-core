package schema

import (
	"time"

	cstypes "github.com/tendermint/tendermint/consensus/types"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
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
	BlockTable = "consensus_block_time"

	// TimestampFieldKey is the name of the field that stores the time at which
	// the block is proposed.
	TimestampFieldKey = "timestamp"
)

func WriteBlock(client *trace.Client, height int64, blockTimestamp time.Time) {
	client.WritePoint(BlockTable, map[string]interface{}{
		HeightFieldKey:    height,
		TimestampFieldKey: blockTimestamp,
	})
}