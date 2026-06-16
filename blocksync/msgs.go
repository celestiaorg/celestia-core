package blocksync

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	"github.com/cometbft/cometbft/types"
)

const (
	// NOTE: keep up to date with bcproto.BlockResponse
	BlockResponseMessagePrefixSize   = 4
	BlockResponseMessageFieldKeySize = 1
)

// errTooManySignatures is returned when an incoming block response encodes more
// commit (or extended commit) signatures than types.MaxVotesCount.
var errTooManySigs = fmt.Errorf("too many signatures (max: %d)", types.MaxVotesCount)
var errTooManyExtendedsigs = fmt.Errorf("too many extended signatures (max: %d)", types.MaxVotesCount)

// validateBlockSyncBytes rejects block responses that encode too many commit
// signatures before protobuf unmarshalling can allocate one object per signature.
func validateBlockSyncBytes(msgBytes []byte) error {
	// unmarshal into custom stub struct that will do no allocations so we can
	// quickly and cheaply check the validity of BlockResponse message
	var stub bcproto.SigCountMessage
	if err := stub.Unmarshal(msgBytes); err != nil {
		return fmt.Errorf("malformed blocksync message %w", err)
	}
	if stub.BlockResponse == nil {
		// Not a BlockResponse oneof case, no extra validation to do in this
		// case
		return nil
	}
	return validateMaxVotes(stub.BlockResponse)
}

// validateMaxVotes validates that the number of commit signatures and extended
// commit signatures are both less than the MaxVotesCount, returns an error if
// not.
func validateMaxVotes(br *bcproto.SigCountBlockResponse) error {
	commitSigs, extSigs := 0, 0
	if br != nil {
		if br.Block != nil && br.Block.LastCommit != nil {
			commitSigs = len(br.Block.LastCommit.Signatures)
		}
		if br.ExtCommit != nil {
			extSigs = len(br.ExtCommit.ExtendedSignatures)
		}
	}

	if commitSigs > types.MaxVotesCount {
		return fmt.Errorf("%w (got %d)", errTooManySigs, commitSigs)
	}
	if extSigs > types.MaxVotesCount {
		return fmt.Errorf("%w (got %d)", errTooManyExtendedsigs, extSigs)
	}

	return nil
}

var (
	MaxMsgSize = types.MaxBlockSizeBytes +
		BlockResponseMessagePrefixSize +
		BlockResponseMessageFieldKeySize
)

// ValidateMsg validates a message.
func ValidateMsg(pb proto.Message) error {
	if pb == nil {
		return ErrNilMessage
	}

	switch msg := pb.(type) {
	case *bcproto.BlockRequest:
		if msg.Height < 0 {
			return ErrInvalidHeight{Height: msg.Height, Reason: "negative height"}
		}
	case *bcproto.BlockResponse:
		// Avoid double-calling `types.BlockFromProto` for performance reasons.
		// See https://github.com/cometbft/cometbft/issues/1964
		return nil
	case *bcproto.NoBlockResponse:
		if msg.Height < 0 {
			return ErrInvalidHeight{Height: msg.Height, Reason: "negative height"}
		}
	case *bcproto.StatusResponse:
		if msg.Base < 0 {
			return ErrInvalidBase{Base: msg.Base, Reason: "negative base"}
		}
		if msg.Height < 0 {
			return ErrInvalidHeight{Height: msg.Height, Reason: "negative height"}
		}
		if msg.Base > msg.Height {
			return ErrInvalidHeight{Height: msg.Height, Reason: fmt.Sprintf("base %v cannot be greater than height", msg.Base)}
		}
	case *bcproto.StatusRequest:
		return nil
	default:
		return ErrUnknownMessageType{Msg: msg}
	}
	return nil
}
