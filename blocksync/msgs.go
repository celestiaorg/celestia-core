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

// maxEvidenceSigs bounds the number of signatures and validators reachable
// through Block.evidence (commit signatures, validator sets and byzantine
// validators, summed across every evidence item). A single valid commit or
// validator set holds at most types.MaxVotesCount entries and the number of
// evidence items in a block is separately bounded by the evidence params at
// validation time, so this limit is generous relative to any well-formed block
// while still far below what a decoded message would need to matter.
const maxEvidenceSigs = types.MaxVotesCount * 100

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
		if br.Block != nil {
			if br.Block.LastCommit != nil {
				commitSigs = len(br.Block.LastCommit.Signatures)
			}
			if err := validateEvidenceSigs(br.Block.Evidence); err != nil {
				return err
			}
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

// validateEvidenceSigs bounds the signatures and validators nested inside
// Block.evidence. A Commit can be reached through evidence
// (LightClientAttackEvidence -> conflicting_block -> signed_header -> commit),
// alongside the conflicting block's validator set and the byzantine validators,
// none of which are covered by the last_commit / ext_commit checks above.
func validateEvidenceSigs(el *bcproto.SigCountEvidenceList) error {
	if el == nil {
		return nil
	}
	total := len(el.Evidence)
	for i := range el.Evidence {
		lcae := el.Evidence[i].LightClientAttackEvidence
		if lcae == nil {
			continue
		}
		total += len(lcae.ByzantineValidators)
		if cb := lcae.ConflictingBlock; cb != nil {
			if cb.SignedHeader != nil && cb.SignedHeader.Commit != nil {
				total += len(cb.SignedHeader.Commit.Signatures)
			}
			if cb.ValidatorSet != nil {
				total += len(cb.ValidatorSet.Validators)
			}
		}
		if total > maxEvidenceSigs {
			return fmt.Errorf("%w (evidence got %d)", errTooManySigs, total)
		}
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
