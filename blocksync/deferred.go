package blocksync

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/protobuf/encoding/protowire"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	"github.com/cometbft/cometbft/types"
)

// syncMessage delays transaction-indexed allocations for oversized responses.
// The next block's commit is needed to apply the current block, but its app
// version may not be known until the current block has been applied.
type syncMessage struct {
	message bcproto.Message
	raw     []byte
}

func (m *syncMessage) Reset()         { *m = syncMessage{} }
func (m *syncMessage) String() string { return m.message.String() }
func (*syncMessage) ProtoMessage()    {}

// Merge supports proto.Clone used by BaseReactor for channel message types.
func (m *syncMessage) Merge(src proto.Message) {
	other := src.(*syncMessage)
	proto.Merge(&m.message, &other.message)
	m.raw = append(m.raw, other.raw...)
}

func (m *syncMessage) Unmarshal(bz []byte) error {
	stub, err := scanBlockSyncBytes(bz)
	if err != nil {
		return err
	}
	if err := validateMaxBlockTxs(stub.BlockResponse, types.BlockTxLimitAppVersion); err == nil {
		return m.message.Unmarshal(bz)
	}
	// Keep wire bytes, rather than one slice entry per transaction. Strip only
	// Data.txs from the decoded view; the pool needs headers and commits for
	// lookahead. Preserve duplicate embedded-message merging semantics.
	filtered, err := omitFields(bz, []protowire.Number{3, 1, 2, 1})
	if err != nil {
		return err
	}
	if err := m.message.Unmarshal(filtered); err != nil {
		return err
	}
	m.raw = bz
	return nil
}

func (m *syncMessage) Unwrap() (proto.Message, error) {
	message, err := m.message.Unwrap()
	if response, ok := message.(*bcproto.BlockResponse); ok && len(m.raw) > 0 {
		return &deferredResponse{BlockResponse: response, raw: m.raw}, err
	}
	return message, err
}

type deferredResponse struct {
	*bcproto.BlockResponse
	raw []byte
}

// omitFields copies the wire message while removing every occurrence at path.
// Recursion over embedded messages is bounded by the fixed path.
func omitFields(bz []byte, path []protowire.Number) ([]byte, error) {
	// Reserve enough space for the input and temporary length prefixes at
	// each fixed nesting level. No allocation is indexed by transaction count
	// or by the number of duplicate embedded Data fields.
	return appendOmittingFields(make([]byte, 0, len(bz)+10*len(path)), bz, path)
}

func appendOmittingFields(out, bz []byte, path []protowire.Number) ([]byte, error) {
	for len(bz) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(bz)
		if tagLen < 0 {
			return nil, protowire.ParseError(tagLen)
		}
		n := protowire.ConsumeFieldValue(num, typ, bz[tagLen:])
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		end := tagLen + n
		if num != path[0] {
			out = append(out, bz[:end]...)
		} else {
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("wrong wire type %d at field %d", typ, num)
			}
			if len(path) > 1 {
				payload, _ := protowire.ConsumeBytes(bz[tagLen:end])
				out = append(out, bz[:tagLen]...)
				lengthAt := len(out)
				out = out[:lengthAt+10]
				var err error
				out, err = appendOmittingFields(out, payload, path[1:])
				if err != nil {
					return nil, err
				}
				payloadLen := len(out) - lengthAt - 10
				var length [10]byte
				prefix := protowire.AppendVarint(length[:0], uint64(payloadLen))
				copy(out[lengthAt:], prefix)
				copy(out[lengthAt+len(prefix):], out[lengthAt+10:])
				out = out[:lengthAt+len(prefix)+payloadLen]
			}
		}
		bz = bz[end:]
	}
	return out, nil
}

// decodeDeferredBlock is called only for the first block in the pool, using the
// state that will validate and apply that height. Never trust the wire version.
func decodeDeferredBlock(raw []byte, appVersion uint64) (*types.Block, error) {
	stub, err := scanBlockSyncBytes(raw)
	if err != nil {
		return nil, err
	}
	if err := validateMaxBlockTxs(stub.BlockResponse, appVersion); err != nil {
		return nil, err
	}
	var message bcproto.Message
	if err := message.Unmarshal(raw); err != nil {
		return nil, err
	}
	response := message.GetBlockResponse()
	if response == nil {
		return nil, fmt.Errorf("deferred message is not a block response")
	}
	return types.BlockFromProto(response.Block)
}
