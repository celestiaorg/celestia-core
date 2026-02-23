package schema

import "github.com/cometbft/cometbft/libs/trace"

const (
	ChannelSizeTable = "channel_size"
)

// ChannelSizeTables returns the list of tables for channel size tracing.
func ChannelSizeTables() []string {
	return []string{ChannelSizeTable}
}

// ChannelSize describes schema for the "channel_size" table.
type ChannelSize struct {
	Channel  string `json:"channel"`
	Size     int    `json:"size"`
	Capacity int    `json:"capacity"`
}

// Table returns the table name for the ChannelSize struct.
func (ChannelSize) Table() string {
	return ChannelSizeTable
}

// WriteChannelSize writes a tracing point for a channel size measurement.
func WriteChannelSize(client trace.Tracer, channel string, size, capacity int) {
	if !client.IsCollecting(ChannelSizeTable) {
		return
	}
	client.Write(ChannelSize{
		Channel:  channel,
		Size:     size,
		Capacity: capacity,
	})
}
