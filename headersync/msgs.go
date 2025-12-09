package headersync

import (
	"errors"
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	hsproto "github.com/cometbft/cometbft/proto/tendermint/headersync"
)

const (
	// MaxHeaderBatchSize is the maximum number of headers per request.
	MaxHeaderBatchSize = 50

	// MaxMsgSize is the maximum message size in bytes for headersync messages.
	// This is calculated based on MaxHeaderBatchSize * estimated header+commit size.
	// Headers are roughly 1KB and commits can be larger depending on validator set size.
	// Assuming ~500 validators with ~100 byte sigs = ~50KB per commit.
	// 50 * (1KB + 50KB) = ~2.5MB, rounding up to 4MB for safety.
	MaxMsgSize = 4 * 1024 * 1024
)

// ValidateMsg validates a headersync message.
func ValidateMsg(pb proto.Message) error {
	if pb == nil {
		return errors.New("message cannot be nil")
	}

	switch msg := pb.(type) {
	case *hsproto.StatusResponse:
		if msg.Base < 0 {
			return errors.New("negative base")
		}
		if msg.Height < 0 {
			return errors.New("negative height")
		}
		if msg.Height < msg.Base {
			return errors.New("height less than base")
		}
		return nil
	case *hsproto.GetHeaders:
		if msg.StartHeight < 1 {
			return errors.New("start height must be at least 1")
		}
		if msg.Count < 1 {
			return errors.New("count must be at least 1")
		}
		if msg.Count > MaxHeaderBatchSize {
			return fmt.Errorf("count %d exceeds max batch size %d", msg.Count, MaxHeaderBatchSize)
		}
		return nil
	case *hsproto.HeadersResponse:
		if len(msg.Headers) > MaxHeaderBatchSize {
			return fmt.Errorf("headers count %d exceeds max batch size %d", len(msg.Headers), MaxHeaderBatchSize)
		}
		for i, sh := range msg.Headers {
			if sh == nil {
				return fmt.Errorf("header at index %d is nil", i)
			}
			if sh.Header == nil {
				return fmt.Errorf("header at index %d has nil Header", i)
			}
			if sh.Commit == nil {
				return fmt.Errorf("header at index %d has nil Commit", i)
			}
		}
		return nil
	case *hsproto.Message:
		switch innerMsg := msg.Sum.(type) {
		case *hsproto.Message_StatusResponse:
			return ValidateMsg(innerMsg.StatusResponse)
		case *hsproto.Message_GetHeaders:
			return ValidateMsg(innerMsg.GetHeaders)
		case *hsproto.Message_HeadersResponse:
			return ValidateMsg(innerMsg.HeadersResponse)
		default:
			return fmt.Errorf("unknown message type: %T", msg.Sum)
		}
	default:
		return fmt.Errorf("unknown message type: %T", msg)
	}
}
