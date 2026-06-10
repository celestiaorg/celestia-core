package blocksync

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/protobuf/encoding/protowire"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	"github.com/cometbft/cometbft/types"
)

const (
	// NOTE: keep up to date with bcproto.BlockResponse
	BlockResponseMessagePrefixSize   = 4
	BlockResponseMessageFieldKeySize = 1
)

// Protobuf field numbers used to locate the repeated signature fields inside a
// blocksync Message without fully unmarshalling it. They must be kept in sync
// with proto/tendermint/blocksync/types.proto and proto/tendermint/types/*.
const (
	blockResponseBlockField     protowire.Number = 1 // blocksync.BlockResponse.block
	blockResponseExtCommitField protowire.Number = 2 // blocksync.BlockResponse.ext_commit
	msgBlockResponseField       protowire.Number = 3 // blocksync.Message.block_response
	blockLastCommitField        protowire.Number = 4 // types.Block.last_commit
	commitSignaturesField       protowire.Number = 4 // types.Commit.signatures
	extCommitSignaturesField    protowire.Number = 4 // types.ExtendedCommit.extended_signatures
)

// lastCommitSigPath and extendedCommitSigPath are the field-number paths to descend
// from a blocksync Message to reach, respectively, last_commit.signatures and
// ext_commit.extended_signatures.
var (
	lastCommitSigPath     = []protowire.Number{msgBlockResponseField, blockResponseBlockField, blockLastCommitField}
	extendedCommitSigPath = []protowire.Number{msgBlockResponseField, blockResponseExtCommitField}
)

// errTooManySignatures is returned when an incoming block response encodes more
// commit (or extended commit) signatures than types.MaxVotesCount.
var errTooManySignatures = fmt.Errorf("too many signatures (max: %d)", types.MaxVotesCount)

// validateBlockSyncBytes rejects block responses that encode too many commit
// signatures before protobuf unmarshalling can allocate one object per signature.
func validateBlockSyncBytes(bz []byte) error {
	if err := checkSignatureCount(bz, lastCommitSigPath, commitSignaturesField, types.MaxVotesCount); err != nil {
		return fmt.Errorf("invalid block response commit: %w", err)
	}
	if err := checkSignatureCount(bz, extendedCommitSigPath, extCommitSignaturesField, types.MaxVotesCount); err != nil {
		return fmt.Errorf("invalid block response extended commit: %w", err)
	}
	return nil
}

// countNestedField follows a schema-known protobuf field path and counts how
// many times target appears there, without unmarshalling.
//
// It only descends through fields listed in path. Other fields are skipped.
// This avoids mistaking bytes/string fields for nested messages.
func checkSignatureCount(data []byte, path []protowire.Number, target protowire.Number, limit int) error {
	count := 0

	// walk scans one message. depth = how far down the path we are (and which
	// field number we're hunting for: path[depth]).
	var walk func(remaining []byte, depth int) error
	walk = func(remaining []byte, depth int) error {
		// A message is just a list of fields, each one a tag (which field + what
		// kind) followed by its value. Read them one at a time, chopping each off
		// the front as we go.
		for len(remaining) > 0 {
			// What field is this, and what kind of value does it hold?
			fieldNum, wireType, consumed := protowire.ConsumeTag(remaining)
			if consumed < 0 {
				return protowire.ParseError(consumed)
			}
			remaining = remaining[consumed:]

			// Plain numbers can't be a nested message or a signature — skip it.
			if wireType != protowire.BytesType {
				if consumed = protowire.ConsumeFieldValue(fieldNum, wireType, remaining); consumed < 0 {
					return protowire.ParseError(consumed)
				}
				remaining = remaining[consumed:]
				continue
			}

			// Otherwise it's a chunk of bytes: a nested message or a signature.
			payload, consumed := protowire.ConsumeBytes(remaining)
			if consumed < 0 {
				return protowire.ParseError(consumed)
			}
			remaining = remaining[consumed:]

			switch {
			// Not deep enough yet: if this is the field our path wants, go in.
			case depth < len(path):
				if fieldNum == path[depth] {
					if err := walk(payload, depth+1); err != nil {
						return err
					}
				}
			// We're in the right message: count signatures, bail if too many.
			case fieldNum == target:
				if count++; count > limit {
					return errTooManySignatures
				}
			}
		}
		return nil
	}

	return walk(data, 0)
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
