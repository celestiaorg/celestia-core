package consensus

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/fuzzinput"
	cmtbits "github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtprotobits "github.com/cometbft/cometbft/proto/tendermint/libs/bits"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// maxFuzzEdits bounds the field edits applied to one structured message.
const maxFuzzEdits = 16

// consensusFuzzSeeds returns one valid message of each consensus type, plus a
// block part and valid-block message from a three-part set, so proofs carry
// aunts and the bit arrays more than one bit.
func consensusFuzzSeeds(tb testing.TB) (seeds, extra []Message) {
	tb.Helper()
	parts, err := cmttypes.NewPartSetFromData([]byte("block part"), cmttypes.BlockPartSizeBytes)
	require.NoError(tb, err)
	header := parts.Header()
	blockID := cmttypes.BlockID{Hash: bytes.Repeat([]byte{1}, 32), PartSetHeader: header}
	proposal := cmttypes.NewProposal(1, 0, -1, blockID)
	proposal.Timestamp = time.Unix(1704067200, 0).UTC()
	proposal.Signature = bytes.Repeat([]byte{2}, 64)
	vote := &cmttypes.Vote{
		Type: cmtproto.PrecommitType, Height: 1, Round: 0, BlockID: blockID,
		Timestamp: proposal.Timestamp, ValidatorAddress: bytes.Repeat([]byte{3}, 20),
		ValidatorIndex: 0, Signature: bytes.Repeat([]byte{4}, 64),
	}
	seeds = []Message{
		&NewRoundStepMessage{Height: 1, Round: 0, Step: 1, LastCommitRound: -1},
		&NewValidBlockMessage{Height: 1, Round: 0, BlockPartSetHeader: header, BlockParts: parts.BitArray()},
		&ProposalMessage{Proposal: proposal},
		&ProposalPOLMessage{Height: 1, ProposalPOLRound: 0, ProposalPOL: cmtbits.NewBitArray(1)},
		&BlockPartMessage{Height: 1, Round: 0, Part: parts.GetPart(0)},
		&VoteMessage{Vote: vote},
		&HasVoteMessage{Height: 1, Round: 0, Type: cmtproto.PrecommitType, Index: 0},
		&VoteSetMaj23Message{Height: 1, Round: 0, Type: cmtproto.PrecommitType, BlockID: blockID},
		&VoteSetBitsMessage{Height: 1, Round: 0, Type: cmtproto.PrecommitType, BlockID: blockID, Votes: cmtbits.NewBitArray(1)},
	}
	// The last part of a three-part set is short, which keeps structured
	// iterations cheap while its proof still has two aunts.
	big, err := cmttypes.NewPartSetFromData(bytes.Repeat([]byte{5}, 2*int(cmttypes.BlockPartSizeBytes)+16), cmttypes.BlockPartSizeBytes)
	require.NoError(tb, err)
	extra = []Message{
		&BlockPartMessage{Height: 2, Round: 1, Part: big.GetPart(2)},
		&NewValidBlockMessage{Height: 2, Round: 1, BlockPartSetHeader: big.Header(), BlockParts: big.BitArray(), IsCommit: true},
	}
	return seeds, extra
}

// consensusWire marshals msg in the envelope peers send.
func consensusWire(tb testing.TB, msg Message) []byte {
	tb.Helper()
	pb, err := MsgToProto(msg)
	require.NoError(tb, err)
	raw, err := proto.Marshal(pb.(p2p.Wrapper).Wrap())
	require.NoError(tb, err)
	return raw
}

// checkConsensusMessage follows the reactor's decode, unwrap and MsgFromProto
// path and reports whether the message was accepted. An accepted message must
// survive a proto round trip unchanged.
func checkConsensusMessage(t *testing.T, raw []byte) bool {
	t.Helper()
	var envelope cmtcons.Message
	if err := proto.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	pb, err := envelope.Unwrap()
	if err != nil {
		return false
	}
	msg, err := MsgFromProto(pb)
	if err != nil {
		return false
	}
	require.NoError(t, msg.ValidateBasic())
	canonical, err := MsgToProto(msg)
	require.NoError(t, err)
	encoded, err := proto.Marshal(canonical.(p2p.Wrapper).Wrap())
	require.NoError(t, err)
	var decoded cmtcons.Message
	require.NoError(t, proto.Unmarshal(encoded, &decoded))
	inner, err := decoded.Unwrap()
	require.NoError(t, err)
	back, err := MsgFromProto(inner)
	require.NoError(t, err)
	again, err := MsgToProto(back)
	require.NoError(t, err)
	require.True(t, proto.Equal(canonical, again), "message changed across proto round trip")
	return true
}

// consensusEditSeeds are structured inputs: a fixture kind, an edit count,
// then per edit a field selector and its value.
var consensusEditSeeds = [][]byte{
	{0, 2, 2, 0, 9, 4, 0},                                 // NewRoundStep: step 9 is undefined
	{1, 2, 2, 1, 0, 2, 3, 0, 3, 2, 0},                     // NewValidBlock: part set total and bit array resized to 2
	{2, 2, 3, 6, 0xfe, 0xff, 0xff, 0xff, 4, 2, 0},         // Proposal: PolRound -2, block hash cleared
	{3, 1, 2, 3, 0x11, 0x27},                              // ProposalPOL: one bit over MaxVotesCount
	{4, 2, 2, 0, 1, 6, 2},                                 // BlockPart: part and proof index past the total
	{9, 1, 7, 0},                                          // three-part BlockPart: one aunt dropped
	{9, 2, 2, 0, 0, 6, 1},                                 // three-part BlockPart: short part moved to index 0
	{9, 1, 7, 3},                                          // three-part BlockPart: one aunt over MaxAunts
	{5, 2, 9, 4, 'e', 'x', 't', '!', 8, 3, 4, 1, 2, 3, 4}, // Vote: extension with its signature
	{5, 2, 4, 3, 9, 1, 'x'},                               // Vote: extension on a nil vote
	{6, 1, 3, 5, 5},                                       // HasVote: undefined type
	{7, 1, 3, 0, 0, 1, 5, 0x80},                           // VoteSetMaj23: part set hash corrupted
	{8, 2, 4, 3, 0x10, 0x27, 1, 0},                        // VoteSetBits: MaxVotesCount bits, round -1
	{10, 1, 2, 1, 2, 0xe9, 0x03},                          // three-part NewValidBlock: total disagrees with bits
}

// FuzzConsensusMessage follows the reactor's wire decode, unwrap and
// MsgFromProto path, including the block-part envelope absent from type fuzzing.
//
// With empty edits, raw is fed to the decoder as wire bytes. Otherwise edits
// selects a valid message, applies field-level edits to its proto form and
// sends the re-encoded envelope down the same path, so that the validation
// past a successful unmarshal is reached far more often.
func FuzzConsensusMessage(f *testing.F) {
	seeds, extra := consensusFuzzSeeds(f)
	fixtures := make([][]byte, 0, len(seeds)+len(extra))
	for _, seed := range append(append([]Message{}, seeds...), extra...) {
		require.NoError(f, seed.ValidateBasic())
		raw := consensusWire(f, seed)
		// Verify seeds reach conversion after the same envelope decode as peers.
		var envelope cmtcons.Message
		require.NoError(f, proto.Unmarshal(raw, &envelope))
		inner, err := envelope.Unwrap()
		require.NoError(f, err)
		_, err = MsgFromProto(inner)
		require.NoError(f, err)
		fixtures = append(fixtures, raw)
	}
	for _, raw := range fixtures[:len(seeds)] {
		f.Add(raw, []byte(nil))
	}
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{0xff}, []byte(nil))
	// Structured seeds: kind, edit count, then per edit a field selector and
	// its value. Each kind once unedited, then edits near validation limits.
	for kind := range fixtures {
		f.Add([]byte(nil), []byte{byte(kind), 0})
	}
	for _, edits := range consensusEditSeeds {
		f.Add([]byte(nil), edits)
	}
	f.Fuzz(func(t *testing.T, raw []byte, edits []byte) {
		if len(edits) == 0 {
			if len(raw) > maxMsgSize {
				return
			}
			checkConsensusMessage(t, raw)
			return
		}
		r := fuzzinput.New(edits)
		var envelope cmtcons.Message
		require.NoError(t, proto.Unmarshal(fixtures[r.Intn(len(fixtures))], &envelope))
		n := r.Intn(maxFuzzEdits + 1)
		for i := 0; i < n; i++ {
			editConsensusMessage(r, &envelope)
		}
		wire, err := proto.Marshal(&envelope)
		require.NoError(t, err)
		accepted := checkConsensusMessage(t, wire)
		if n == 0 {
			require.True(t, accepted, "unedited fixture rejected")
		}
	})
}

// editConsensusMessage applies one field edit to the message in envelope.
func editConsensusMessage(r *fuzzinput.Reader, envelope *cmtcons.Message) {
	switch m := envelope.Sum.(type) {
	case *cmtcons.Message_NewRoundStep:
		msg := m.NewRoundStep
		switch r.Intn(5) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			msg.Step = pickUint32(r)
		case 3:
			msg.SecondsSinceStartTime = pickInt64(r)
		case 4:
			msg.LastCommitRound = pickInt32(r)
		}
	case *cmtcons.Message_NewValidBlock:
		msg := m.NewValidBlock
		switch r.Intn(5) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			editPartSetHeader(r, &msg.BlockPartSetHeader)
		case 3:
			switch {
			case r.Byte() == 0xff:
				msg.BlockParts = nil
			case msg.BlockParts == nil:
				msg.BlockParts = &cmtprotobits.BitArray{}
			default:
				editBitArray(r, msg.BlockParts)
			}
		case 4:
			msg.IsCommit = !msg.IsCommit
		}
	case *cmtcons.Message_Proposal:
		p := &m.Proposal.Proposal
		switch r.Intn(7) {
		case 0:
			p.Type = cmtproto.SignedMsgType(pickInt32(r))
		case 1:
			p.Height = pickInt64(r)
		case 2:
			p.Round = pickInt32(r)
		case 3:
			p.PolRound = pickInt32(r)
		case 4:
			editBlockID(r, &p.BlockID)
		case 5:
			p.Timestamp = pickTime(r)
		case 6:
			p.Signature = editBytes(r, p.Signature)
		}
	case *cmtcons.Message_ProposalPol:
		msg := m.ProposalPol
		switch r.Intn(3) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.ProposalPolRound = pickInt32(r)
		case 2:
			editBitArray(r, &msg.ProposalPol)
		}
	case *cmtcons.Message_BlockPart:
		msg := m.BlockPart
		part := &msg.Part
		switch r.Intn(9) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			part.Index = pickUint32(r)
		case 3:
			part.Bytes = editBytes(r, part.Bytes)
		case 4:
			// Resize to around the fixed part size, where the size rule flips.
			n := int(cmttypes.BlockPartSizeBytes) - 2 + r.Intn(5)
			part.Bytes = bytes.Repeat([]byte{r.Byte()}, n)
		case 5:
			part.Proof.Total = pickInt64(r)
		case 6:
			part.Proof.Index = pickInt64(r)
		case 7:
			part.Proof.Aunts = editAunts(r, part.Proof.Aunts)
		case 8:
			part.Proof.LeafHash = editBytes(r, part.Proof.LeafHash)
		}
	case *cmtcons.Message_Vote:
		msg := m.Vote
		if msg.Vote == nil {
			return
		}
		v := msg.Vote
		switch r.Intn(12) {
		case 0:
			v.Type = cmtproto.SignedMsgType(pickInt32(r))
		case 1:
			v.Height = pickInt64(r)
		case 2:
			v.Round = pickInt32(r)
		case 3:
			v.ValidatorIndex = pickInt32(r)
		case 4:
			editBlockID(r, &v.BlockID)
		case 5:
			v.Timestamp = pickTime(r)
		case 6:
			v.ValidatorAddress = editBytes(r, v.ValidatorAddress)
		case 7:
			v.Signature = editBytes(r, v.Signature)
		case 8:
			v.ExtensionSignature = editBytes(r, v.ExtensionSignature)
		case 9:
			v.Extension = r.Bytes(64)
		case 10:
			// A signature just over the size limit.
			v.Signature = bytes.Repeat([]byte{r.Byte()}, cmttypes.MaxSignatureSize+r.Intn(2))
		case 11:
			if r.Byte() == 0xff {
				msg.Vote = nil
			}
		}
	case *cmtcons.Message_HasVote:
		msg := m.HasVote
		switch r.Intn(4) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			msg.Index = pickInt32(r)
		case 3:
			msg.Type = cmtproto.SignedMsgType(pickInt32(r))
		}
	case *cmtcons.Message_VoteSetMaj23:
		msg := m.VoteSetMaj23
		switch r.Intn(4) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			msg.Type = cmtproto.SignedMsgType(pickInt32(r))
		case 3:
			editBlockID(r, &msg.BlockID)
		}
	case *cmtcons.Message_VoteSetBits:
		msg := m.VoteSetBits
		switch r.Intn(5) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			msg.Type = cmtproto.SignedMsgType(pickInt32(r))
		case 3:
			editBlockID(r, &msg.BlockID)
		case 4:
			editBitArray(r, &msg.Votes)
		}
	}
}

// pickInt64 returns a boundary value or one read from r.
func pickInt64(r *fuzzinput.Reader) int64 {
	switch r.Intn(7) {
	case 0:
		return -1
	case 1:
		return 0
	case 2:
		return 1
	case 3:
		return math.MaxInt64
	case 4:
		return math.MinInt64
	case 5:
		return int64(r.Byte())
	default:
		return r.Int64()
	}
}

// pickInt32 returns a boundary value or one read from r.
func pickInt32(r *fuzzinput.Reader) int32 {
	switch r.Intn(7) {
	case 0:
		return -1
	case 1:
		return 0
	case 2:
		return 1
	case 3:
		return math.MaxInt32
	case 4:
		return math.MinInt32
	case 5:
		return int32(r.Byte())
	default:
		return r.Int32()
	}
}

// pickUint32 returns a small value, a boundary value or one read from r.
func pickUint32(r *fuzzinput.Reader) uint32 {
	switch r.Intn(4) {
	case 0:
		return uint32(r.Byte())
	case 1:
		return math.MaxUint32
	case 2:
		return uint32(r.Uint16())
	default:
		return r.Uint32()
	}
}

// pickTime returns a timestamp within the range protobuf can encode.
func pickTime(r *fuzzinput.Reader) time.Time {
	return time.Unix(int64(r.Int32())*16, int64(r.Uint32()%1e9)).UTC()
}

// editBytes returns cur with one byte flipped, truncated by one, cleared, or
// replaced by up to 128 bytes from r.
func editBytes(r *fuzzinput.Reader, cur []byte) []byte {
	switch r.Intn(4) {
	case 0:
		return nil
	case 1:
		if len(cur) == 0 {
			return nil
		}
		out := append([]byte(nil), cur...)
		out[r.Intn(len(out))] ^= 1 | r.Byte()
		return out
	case 2:
		if len(cur) == 0 {
			return nil
		}
		return append([]byte(nil), cur[:len(cur)-1]...)
	default:
		return r.Bytes(128)
	}
}

func editPartSetHeader(r *fuzzinput.Reader, psh *cmtproto.PartSetHeader) {
	if r.Bool() {
		psh.Total = pickUint32(r)
		return
	}
	psh.Hash = editBytes(r, psh.Hash)
}

func editBlockID(r *fuzzinput.Reader, id *cmtproto.BlockID) {
	switch r.Intn(4) {
	case 0, 1:
		editPartSetHeader(r, &id.PartSetHeader)
	case 2:
		id.Hash = editBytes(r, id.Hash)
	case 3:
		*id = cmtproto.BlockID{}
	}
}

// editBitArray changes the declared size, the element count, or one bit. Sizes
// stay below 160 elements, just above what MaxVotesCount bits need.
func editBitArray(r *fuzzinput.Reader, ba *cmtprotobits.BitArray) {
	switch r.Intn(5) {
	case 0:
		ba.Bits = pickInt64(r)
	case 1:
		ba.Elems = make([]uint64, r.Intn(160))
	case 2:
		if len(ba.Elems) > 0 {
			i := r.Intn(len(ba.Elems) * 64)
			ba.Elems[i/64] ^= 1 << (i % 64)
		}
	case 3:
		// Resize consistently, so validation moves on to the size limits.
		ba.Bits = int64(r.Uint16()) % (cmttypes.MaxVotesCount + 2)
		ba.Elems = make([]uint64, (ba.Bits+63)/64)
	case 4:
		ba.Bits = int64(r.Intn(1 << 8))
		ba.Elems = make([]uint64, (ba.Bits+63)/64)
		if len(ba.Elems) > 0 {
			ba.Elems[0] = r.Uint64()
		}
	}
}

// editAunts drops, appends, resizes or corrupts one merkle proof aunt, keeping
// the count within one of merkle.MaxAunts.
func editAunts(r *fuzzinput.Reader, aunts [][]byte) [][]byte {
	switch r.Intn(4) {
	case 0:
		if len(aunts) > 0 {
			return aunts[:len(aunts)-1]
		}
	case 1:
		if len(aunts) <= 100 {
			return append(aunts, bytes.Repeat([]byte{r.Byte()}, 32))
		}
	case 2:
		if len(aunts) > 0 {
			i := r.Intn(len(aunts))
			aunts[i] = editBytes(r, aunts[i])
		}
	case 3:
		// A full list one past the limit.
		out := make([][]byte, 101)
		for i := range out {
			out[i] = bytes.Repeat([]byte{byte(i)}, 32)
		}
		return out
	}
	return aunts
}
