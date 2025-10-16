package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

// MessageStatsTables returns the list of tables that are used for the message stats
// tracing.
func MessageStatsTables() []string {
	return []string{
		MessageStatsTable,
	}
}

const (
	// MessageStatsTable tracks all the messages received by any reactor and traces their processing time.
	MessageStatsTable = "message_stats"
)

// MessageStat describes schema for the "message_stats" table.
type MessageStat struct {
	Reactor        string `json:"reactor"`
	MessageType    string `json:"message_type"`
	ProcessingTime int64  `json:"processing_time"`
	Details        string `json:"details"`
}

// Table returns the table name for the MessageStat struct.
func (MessageStat) Table() string {
	return MessageStatsTable
}

func WriteMessageStats(client trace.Tracer, reactor string, messageType string, processingTime int64, details string) {
	client.Write(MessageStat{
		Reactor:        reactor,
		MessageType:    messageType,
		ProcessingTime: processingTime,
		Details:        details,
	})
}
