package schema

import (
	cstypes "github.com/tendermint/tendermint/consensus/types"
	"github.com/tendermint/tendermint/pkg/trace"
)

// Table names for the consensus round state tracing database.
const (
	// RoundStateTable is the name of the table that stores the consensus
	// state traces.
	RoundStateTable = "consensus_round_state"

	// Field names for the consensus state table.

	// StepFieldKey is the name of the field that stores the consensus step. The
	// value is a string.
	StepFieldKey = "step"

	// RoundFieldKey is the name of the field that stores the consensus round.
	// The value is an int32.
	RoundFieldKey = "round"

	// HeightFieldKey is the name of the field that stores the consensus height.
	// The value is an int64.
	HeightFieldKey = "height"
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

// ConsensusTables returns the list of tables that are used for consensus
// tracing.
func ConsensusTables() []string {
	return []string{
		RoundStateTable,
	}
}
