package blocksync

import (
	"errors"
	"fmt"
	"io"

	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/protobuf/encoding/protowire"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

const (
	// NOTE: keep up to date with bcproto.BlockResponse
	BlockResponseMessagePrefixSize   = 4
	BlockResponseMessageFieldKeySize = 1
)

// maxEvidenceSigs bounds the aggregate number of signatures, validators and
// evidence items reachable through Block.evidence. Each individual commit,
// validator set and byzantine-validator list is separately capped at
// types.MaxVotesCount (the same invariant last_commit enforces); this aggregate
// guards the sum across evidence items, which is otherwise unbounded before
// decoding. It is generous relative to any block whose evidence stays within the
// default evidence params, while remaining far below what would exhaust memory.
const maxEvidenceSigs = types.MaxVotesCount * 100

// errTooManySignatures is returned when an incoming block response encodes more
// commit (or extended commit) signatures than types.MaxVotesCount.
var errTooManySigs = fmt.Errorf("too many signatures (max: %d)", types.MaxVotesCount)
var errTooManyExtendedsigs = fmt.Errorf("too many extended signatures (max: %d)", types.MaxVotesCount)
var errTooManyEvidenceSigs = fmt.Errorf("too many evidence signatures (max: %d)", maxEvidenceSigs)
var errTooManyTxs = errors.New("too many transactions")

// validateBlockSyncBytes rejects block responses that encode too many commit
// signatures before protobuf unmarshalling can allocate one object per signature.
func validateBlockSyncBytes(msgBytes []byte) error {
	stub, err := scanBlockSyncBytes(msgBytes)
	if err != nil {
		return err
	}
	if stub.BlockResponse == nil {
		return nil
	}
	return validateMaxVotes(stub.BlockResponse)
}

func scanBlockSyncBytes(msgBytes []byte) (*bcproto.SigCountMessage, error) {
	if err := validateEvidenceItemCount(msgBytes); err != nil {
		return nil, err
	}
	blockScanner := cmtproto.NewBlockCardinalityScanner(-1)
	if err := visitProtoPath(msgBytes, []protowire.Number{3, 1}, func(block []byte) error {
		err := blockScanner.Scan(block)
		switch {
		case errors.Is(err, cmtproto.ErrTooManyBlockCommitSignatures):
			return fmt.Errorf("%w: %v", errTooManySigs, err)
		case errors.Is(err, cmtproto.ErrTooManyBlockEvidence),
			errors.Is(err, cmtproto.ErrTooManyBlockEvidenceElements):
			return fmt.Errorf("%w: %v", errTooManyEvidenceSigs, err)
		default:
			return err
		}
	}); err != nil {
		return nil, err
	}
	// unmarshal into custom stub struct that will do no allocations so we can
	// quickly and cheaply check the validity of BlockResponse message
	var stub bcproto.SigCountMessage
	if err := stub.Unmarshal(msgBytes); err != nil {
		return nil, fmt.Errorf("malformed blocksync message %w", err)
	}
	return &stub, nil
}

// validateEvidenceItemCount rejects an oversized EvidenceList before the
// generated stub decoder appends one entry per evidence item.
func validateEvidenceItemCount(msgBytes []byte) error {
	count := 0
	path := []protowire.Number{3, 1, 3, 1} // Message.response.block.evidence.evidence
	return visitProtoPath(msgBytes, path, func([]byte) error {
		count++
		if count > maxEvidenceSigs {
			return fmt.Errorf("%w (evidence got %d)", errTooManyEvidenceSigs, count)
		}
		return nil
	})
}

// visitProtoPath calls visit for every length-delimited field at path.
func visitProtoPath(bz []byte, path []protowire.Number, visit func([]byte) error) error {
	for len(bz) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(bz)
		if tagLen < 0 {
			return protowire.ParseError(tagLen)
		}
		end, err := skipProtoField(bz)
		if err != nil {
			return err
		}
		if num == path[0] {
			if typ != protowire.BytesType {
				return fmt.Errorf("wrong wire type %d for field %d", typ, num)
			}
			payload, size := protowire.ConsumeBytes(bz[tagLen:end])
			if size < 0 {
				return protowire.ParseError(size)
			}
			if len(path) == 1 {
				if err := visit(payload); err != nil {
					return err
				}
			} else if err := visitProtoPath(payload, path[1:], visit); err != nil {
				return err
			}
		}
		bz = bz[end:]
	}
	return nil
}

// skipProtoField consumes one field, including groups, without recursion.
func skipProtoField(bz []byte) (int, error) {
	i, depth := 0, 0
	for i < len(bz) {
		_, typ, tagLen := protowire.ConsumeTag(bz[i:])
		if tagLen < 0 {
			return 0, protowire.ParseError(tagLen)
		}
		i += tagLen
		switch typ {
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(bz[i:])
			if n < 0 {
				return 0, protowire.ParseError(n)
			}
			i += n
		case protowire.Fixed64Type:
			i += 8
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(bz[i:])
			if n < 0 {
				return 0, protowire.ParseError(n)
			}
			i += n
		case protowire.StartGroupType:
			depth++
		case protowire.EndGroupType:
			if depth == 0 {
				return 0, fmt.Errorf("unexpected end group")
			}
			depth--
		case protowire.Fixed32Type:
			i += 4
		default:
			return 0, fmt.Errorf("illegal wire type %d", typ)
		}
		if i < 0 || i > len(bz) {
			return 0, io.ErrUnexpectedEOF
		}
		if depth == 0 {
			return i, nil
		}
	}
	return 0, io.ErrUnexpectedEOF
}

func validateMaxBlockTxs(br *bcproto.SigCountBlockResponse, appVersion uint64) error {
	maxTxs := types.MaxBlockTxsForAppVersion(appVersion)
	if maxTxs == 0 || br == nil || br.Block == nil || br.Block.Data == nil {
		return nil
	}
	if count := len(br.Block.Data.Txs); count > maxTxs {
		return fmt.Errorf("%w (max: %d, got %d)", errTooManyTxs, maxTxs, count)
	}
	return nil
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
	// The evidence items themselves count toward the aggregate bound, so an
	// oversized list of items that carry no nested lists (e.g. duplicate-vote or
	// empty evidence) is rejected here before the per-item accounting below.
	total := len(el.Evidence)
	if total > maxEvidenceSigs {
		return fmt.Errorf("%w (evidence got %d)", errTooManyEvidenceSigs, total)
	}
	for i := range el.Evidence {
		lcae := el.Evidence[i].LightClientAttackEvidence
		if lcae == nil {
			continue
		}
		// A conflicting-block commit, its validator set, and the byzantine
		// validators are each bounded by MaxVotesCount exactly as last_commit is,
		// so enforce that per-list invariant directly before summing.
		if n := len(lcae.ByzantineValidators); n > types.MaxVotesCount {
			return fmt.Errorf("%w (byzantine validators got %d)", errTooManySigs, n)
		}
		total += len(lcae.ByzantineValidators)
		if cb := lcae.ConflictingBlock; cb != nil {
			if cb.SignedHeader != nil && cb.SignedHeader.Commit != nil {
				if n := len(cb.SignedHeader.Commit.Signatures); n > types.MaxVotesCount {
					return fmt.Errorf("%w (evidence commit got %d)", errTooManySigs, n)
				}
				total += len(cb.SignedHeader.Commit.Signatures)
			}
			if cb.ValidatorSet != nil {
				if n := len(cb.ValidatorSet.Validators); n > types.MaxVotesCount {
					return fmt.Errorf("%w (validator set got %d)", errTooManySigs, n)
				}
				total += len(cb.ValidatorSet.Validators)
			}
		}
		if total > maxEvidenceSigs {
			return fmt.Errorf("%w (evidence got %d)", errTooManyEvidenceSigs, total)
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
