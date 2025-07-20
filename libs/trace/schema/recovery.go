package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

// RecoveryTables returns the list of tables that are used for recovery
// tracing.
func RecoveryTables() []string {
	return []string{
		ReceivedPartTable,
	}
}

const (
	// ReceivedPartTable tracks all the parts received by propagation reactor
	ReceivedPartTable = "recovery_received_part"
)

// ReceivedPart describes schema for the "recovery_received_part" table.
type ReceivedPart struct {
	Height int64 `json:"height"`
	Round  int32 `json:"round"`
	Index  int   `json:"index"`
}

// Table returns the table name for the ReceivedPart struct.
func (ReceivedPart) Table() string {
	return ReceivedPartTable
}

func WriteReceivedPart(client trace.Tracer, height int64, round int32, index int) {
	client.Write(ReceivedPart{Height: height, Round: round, Index: index})
}
