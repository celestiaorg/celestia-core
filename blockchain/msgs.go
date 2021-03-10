package blockchain

import (
	"errors"
	"fmt"

	"github.com/gogo/protobuf/proto"

	bcproto "github.com/lazyledger/lazyledger-core/proto/tendermint/blockchain"
	"github.com/lazyledger/lazyledger-core/types"
)

const (
	// NOTE: keep up to date with bcproto.BlockResponse
	BlockResponseMessagePrefixSize   = 4
	BlockResponseMessageFieldKeySize = 1
	MaxMsgSize                       = types.MaxBlockSizeBytes +
		BlockResponseMessagePrefixSize +
		BlockResponseMessageFieldKeySize
)

// EncodeMsg encodes a Protobuf message
func EncodeMsg(pb proto.Message) ([]byte, error) {
	msg := bcproto.Message{}

	switch pb := pb.(type) {
	case *bcproto.HeaderRequest:
		msg.Sum = &bcproto.Message_HeaderRequest{HeaderRequest: pb}
	case *bcproto.HeaderResponse:
		msg.Sum = &bcproto.Message_HeaderResponse{HeaderResponse: pb}
	case *bcproto.NoHeaderResponse:
		msg.Sum = &bcproto.Message_NoHeaderResponse{NoHeaderResponse: pb}
	case *bcproto.StatusRequest:
		msg.Sum = &bcproto.Message_StatusRequest{StatusRequest: pb}
	case *bcproto.StatusResponse:
		msg.Sum = &bcproto.Message_StatusResponse{StatusResponse: pb}
	default:
		return nil, fmt.Errorf("unknown message type %T", pb)
	}

	bz, err := proto.Marshal(&msg)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal %T: %w", pb, err)
	}

	return bz, nil
}

// DecodeMsg decodes a Protobuf message.
func DecodeMsg(bz []byte) (proto.Message, error) {
	pb := &bcproto.Message{}

	err := proto.Unmarshal(bz, pb)
	if err != nil {
		return nil, err
	}

	switch msg := pb.Sum.(type) {
	case *bcproto.Message_HeaderRequest:
		return msg.HeaderRequest, nil
	case *bcproto.Message_HeaderResponse:
		return msg.HeaderResponse, nil
	case *bcproto.Message_NoHeaderResponse:
		return msg.NoHeaderResponse, nil
	case *bcproto.Message_StatusRequest:
		return msg.StatusRequest, nil
	case *bcproto.Message_StatusResponse:
		return msg.StatusResponse, nil
	default:
		return nil, fmt.Errorf("unknown message type %T", msg)
	}
}

// ValidateMsg validates a message.
func ValidateMsg(pb proto.Message) error {
	if pb == nil {
		return errors.New("message cannot be nil")
	}

	switch msg := pb.(type) {
	case *bcproto.HeaderRequest:
		if msg.Height < 0 {
			return errors.New("negative Height")
		}
	case *bcproto.HeaderResponse:
		// validate basic is called later when converting from proto
		return nil
	case *bcproto.NoHeaderResponse:
		if msg.Height < 0 {
			return errors.New("negative Height")
		}
	case *bcproto.StatusResponse:
		if msg.Base < 0 {
			return errors.New("negative Base")
		}
		if msg.Height < 0 {
			return errors.New("negative Height")
		}
		if msg.Base > msg.Height {
			return fmt.Errorf("base %v cannot be greater than height %v", msg.Base, msg.Height)
		}
	case *bcproto.StatusRequest:
		return nil
	default:
		return fmt.Errorf("unknown message type %T", msg)
	}
	return nil
}
