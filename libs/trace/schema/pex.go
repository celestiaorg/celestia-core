package schema

import (
	"fmt"
	"time"

	"github.com/cometbft/cometbft/libs/trace"
)

const (
	PexTables = "pex_events"
)

type PexEvent struct {
	EventType       string `json:"event_type"`
	OutboundCount   int    `json:"outbound_count"`
	InboundCount    int    `json:"inbound_count"`
	DialingCount    int    `json:"dialing_count"`
	AddressBookSize int    `json:"address_book_size"`
	RequestInterval string `json:"request_interval,omitempty"`
	Timestamp       string `json:"timestamp"`
}

func (PexEvent) Table() string {
	return PexTables
}

func WritePexEvent(
	client trace.Tracer,
	eventType string,
	outboundCount int,
	inboundCount int,
	dialingCount int,
	addressBookSize int,
	requestInterval string,
) {
	if !client.IsCollecting(PexTables) {
		return
	}

	// Debug: Log that we're writing an event
	fmt.Printf("DEBUG: Writing PEX event: %s, outbound: %d, inbound: %d, dialing: %d, addrbook: %d, interval: %s\n",
		eventType, outboundCount, inboundCount, dialingCount, addressBookSize, requestInterval)

	client.Write(PexEvent{
		EventType:       eventType,
		OutboundCount:   outboundCount,
		InboundCount:    inboundCount,
		DialingCount:    dialingCount,
		AddressBookSize: addressBookSize,
		RequestInterval: requestInterval,
		Timestamp:       time.Now().Format("2006-01-02 15:04:05"),
	})
}
