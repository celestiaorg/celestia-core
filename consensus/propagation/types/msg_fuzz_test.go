package types

import (
	"bytes"
	"math"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/fuzzinput"
	cmtbits "github.com/cometbft/cometbft/libs/bits"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	cmttypes "github.com/cometbft/cometbft/types"
)

// propagationSeeds returns one valid wire message of each propagation type.
func propagationSeeds(tb testing.TB) [][]byte {
	tb.Helper()
	hash := func(b byte) []byte { return bytes.Repeat([]byte{b}, tmhash.Size) }

	// Two original parts and two parity parts with real merkle roots, so the
	// compact block seed passes Proofs().
	partHashes := [][]byte{hash(1), hash(2), hash(3), hash(4)}
	root, _ := merkle.ParallelProofsFromLeafHashes(partHashes[:2])
	parityRoot, _ := merkle.ParallelProofsFromLeafHashes(partHashes[2:])
	proposal := cmttypes.NewProposal(10, 1, -1, cmttypes.BlockID{
		Hash:          hash(9),
		PartSetHeader: cmttypes.PartSetHeader{Total: 2, Hash: root},
	})
	proposal.Signature = bytes.Repeat([]byte{7}, 64)
	compact := &CompactBlock{
		BpHash:      parityRoot,
		Blobs:       []TxMetaData{{Hash: hash(5), Start: 0, End: 10}, {Hash: hash(6), Start: 10, End: 40}},
		Signature:   bytes.Repeat([]byte{8}, 64),
		Proposal:    *proposal,
		LastLen:     100,
		PartsHashes: partHashes,
	}
	require.NoError(tb, compact.ValidateBasic())

	haves := &HaveParts{Height: 10, Round: 1, Parts: []PartMetaData{{Index: 0, Hash: hash(1)}, {Index: 3, Hash: hash(4)}}}
	wants := &WantParts{Parts: cmtbits.NewBitArray(4), Height: 10, Round: 1, Prove: true, MissingPartsCount: 2}
	wants.Parts.SetIndex(1, true)
	wants.Parts.SetIndex(2, true)

	data := bytes.Repeat([]byte{0xab}, 64)
	leafRoot, proofs := merkle.ProofsFromByteSlices([][]byte{data, []byte("other")})
	_ = leafRoot
	recovery := &RecoveryPart{Height: 10, Round: 1, Index: 0, Data: data, Proof: proofs[0]}
	require.NoError(tb, recovery.ValidateBasic())

	messages := []*protoprop.Message{
		{Sum: &protoprop.Message_CompactBlock{CompactBlock: compact.ToProto()}},
		{Sum: &protoprop.Message_HaveParts{HaveParts: haves.ToProto()}},
		{Sum: &protoprop.Message_WantParts{WantParts: wants.ToProto()}},
		{Sum: &protoprop.Message_RecoveryPart{RecoveryPart: &protoprop.RecoveryPart{
			Height: recovery.Height, Round: recovery.Round, Index: recovery.Index, Data: recovery.Data, Proof: *recovery.Proof.ToProto(),
		}}},
		{Sum: &protoprop.Message_RecoveryPart{RecoveryPart: &protoprop.RecoveryPart{Height: 10, Round: 1, Index: 1, Data: data}}},
	}
	seeds := make([][]byte, 0, len(messages))
	for _, msg := range messages {
		raw, err := proto.Marshal(msg)
		require.NoError(tb, err)
		seeds = append(seeds, raw)
	}
	return seeds
}

// checkPropagationMessage decodes raw the way the reactor does and exercises
// what the reactor then does with each message. Nothing may panic, and a
// message that decodes must survive a proto round trip unchanged. It returns
// the decoded message, or nil if the reactor would drop it.
func checkPropagationMessage(t *testing.T, raw []byte) Message {
	t.Helper()
	var pb protoprop.Message
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return nil
	}
	msg, err := MsgFromProto(&pb)
	if err != nil {
		return nil
	}
	require.NoError(t, msg.ValidateBasic(), "MsgFromProto accepted a message ValidateBasic rejects")

	switch m := msg.(type) {
	case *CompactBlock:
		_, _ = m.Proofs()
		_ = m.GetProof(0)
		_ = m.GetProof(^uint32(0))
		_, _ = m.SignBytes()
		again, err := CompactBlockFromProto(m.ToProto())
		require.NoError(t, err)
		require.Equal(t, m.ToProto(), again.ToProto())
	case *HaveParts:
		for _, size := range []int{0, 1, 64, 1 << 10} {
			_ = m.BitArray(size)
		}
		_ = m.GetIndex(0)
		_ = m.IsEmpty()
		_ = m.ValidatePartHashes([][]byte{bytes.Repeat([]byte{1}, tmhash.Size)})
		again, err := HavePartFromProto(m.ToProto())
		require.NoError(t, err)
		require.Equal(t, m, again)
	case *WantParts:
		_ = m.Parts.GetTrueIndices()
		_ = m.Parts.Copy()
		again, err := WantPartsFromProto(m.ToProto())
		require.NoError(t, err)
		require.Equal(t, m.ToProto(), again.ToProto())
	case *RecoveryPart:
		if m.Proof != nil {
			// ComputeRootHash is documented to panic on a proof whose index,
			// total and aunts disagree, which ValidateBasic does not rule out.
			func() {
				defer func() { _ = recover() }()
				root := m.Proof.ComputeRootHash()
				require.NoError(t, m.Proof.Verify(root, m.Data))
			}()
		}
	default:
		t.Fatalf("unexpected message type %T", msg)
	}
	return msg
}

func TestPropagationMessageSeeds(t *testing.T) {
	for _, raw := range propagationSeeds(t) {
		var pb protoprop.Message
		require.NoError(t, proto.Unmarshal(raw, &pb))
		_, err := MsgFromProto(&pb)
		require.NoError(t, err, "every seed must decode so the fuzzer starts from accepted messages")
		checkPropagationMessage(t, raw)
	}
}

// maxFuzzEdits bounds the field edits applied to one structured message.
const maxFuzzEdits = 16

// propagationEditSeeds are structured inputs: a fixture kind, an edit count,
// then per edit a field selector and its value.
var propagationEditSeeds = [][]byte{
	{0, 1, 8, 3},                  // CompactBlock: three original parts, roots recomputed
	{0, 1, 8, 0},                  // CompactBlock: zero parts
	{0, 1, 7, 1, 1, 0, 9, 1},      // CompactBlock: part hash corrupted, roots recomputed
	{0, 1, 2, 0, 7, 1, 30, 1, 35}, // CompactBlock: overlapping blob ranges
	{0, 1, 2, 2},                  // CompactBlock: blob range reversed
	{0, 1, 5, 2, 1, 3},            // CompactBlock: part set total no longer matches hashes
	{0, 1, 6, 0},                  // CompactBlock: nil proposal
	{1, 1, 2, 1, 0, 2},            // HaveParts: part index MaxUint32
	{1, 1, 2, 2},                  // HaveParts: last part dropped
	{1, 1, 3, 0, 0},               // HaveParts: part hash cleared
	{2, 1, 4, 3, 5},               // WantParts: missing count over the bit array size
	{2, 1, 2, 0x03, 0x10},         // WantParts: one bit over the maximum
	{2, 1, 4, 0},                  // WantParts: missing count -1
	{3, 1, 4, 1, 0, 0},            // RecoveryPart: data byte flipped
	{3, 1, 6, 0, 2},               // RecoveryPart: proof index 1
	{3, 2, 6, 1, 1, 6, 0, 2},      // RecoveryPart: proof total 0, index 1
	{3, 1, 7, 0},                  // RecoveryPart: proof aunt dropped
	{4, 1, 3, 2},                  // RecoveryPart without proof: index MaxUint32
}

// FuzzPropagationMessage feeds mutated wire bytes through the propagation
// reactor's message decoding and the per-message helpers it calls.
//
// With empty edits, raw is fed to the decoder as wire bytes. Otherwise edits
// selects a valid message, applies field-level edits to its proto form and
// sends the re-encoded message down the same path, so that ValidateBasic,
// Proofs and the proof checks run on near-valid messages.
func FuzzPropagationMessage(f *testing.F) {
	fixtures := propagationSeeds(f)
	for _, raw := range fixtures {
		f.Add(raw, []byte(nil))
	}
	f.Add([]byte{}, []byte(nil))
	for kind := range fixtures {
		f.Add([]byte(nil), []byte{byte(kind), 0})
	}
	for _, edits := range propagationEditSeeds {
		f.Add([]byte(nil), edits)
	}
	f.Fuzz(func(t *testing.T, raw []byte, edits []byte) {
		if len(edits) == 0 {
			if len(raw) > 1<<20 {
				return
			}
			checkPropagationMessage(t, raw)
			return
		}
		r := fuzzinput.New(edits)
		var pb protoprop.Message
		require.NoError(t, proto.Unmarshal(fixtures[r.Intn(len(fixtures))], &pb))
		n := r.Intn(maxFuzzEdits + 1)
		for i := 0; i < n; i++ {
			editPropagationMessage(r, &pb)
		}
		wire, err := proto.Marshal(&pb)
		require.NoError(t, err)
		msg := checkPropagationMessage(t, wire)
		if n > 0 {
			return
		}
		require.NotNil(t, msg, "unedited fixture rejected")
		if cb, ok := msg.(*CompactBlock); ok {
			_, err := cb.Proofs()
			require.NoError(t, err, "unedited compact block has inconsistent roots")
		}
	})
}

// editPropagationMessage applies one field edit to the message in pb.
func editPropagationMessage(r *fuzzinput.Reader, pb *protoprop.Message) {
	switch m := pb.Sum.(type) {
	case *protoprop.Message_CompactBlock:
		editCompactBlock(r, m.CompactBlock)
	case *protoprop.Message_HaveParts:
		msg := m.HaveParts
		switch r.Intn(4) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			switch r.Intn(3) {
			case 0:
				msg.Parts = append(msg.Parts, &protoprop.PartMetaData{Index: pickUint32(r), Hash: bytes.Repeat([]byte{r.Byte()}, tmhash.Size)})
			case 1:
				if len(msg.Parts) > 0 {
					msg.Parts[r.Intn(len(msg.Parts))].Index = pickUint32(r)
				}
			case 2:
				if len(msg.Parts) > 0 {
					msg.Parts = msg.Parts[:len(msg.Parts)-1]
				}
			}
		case 3:
			if len(msg.Parts) > 0 {
				p := msg.Parts[r.Intn(len(msg.Parts))]
				if p == nil {
					return
				}
				p.Hash = editBytes(r, p.Hash)
			}
		}
	case *protoprop.Message_WantParts:
		msg := m.WantParts
		switch r.Intn(6) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			// Resize consistently, around the MaxBlockPartsCount*ParityRatio limit.
			bits := int64(r.Uint16()) % (int64(cmttypes.MaxBlockPartsCount)*ParityRatio + 2)
			msg.Parts.Bits = bits
			msg.Parts.Elems = make([]uint64, (bits+63)/64)
		case 3:
			if r.Bool() {
				msg.Parts.Bits = pickInt64(r)
			} else {
				msg.Parts.Elems = make([]uint64, r.Intn(64))
			}
		case 4:
			msg.MissingPartsCount = pickInt32(r)
		case 5:
			msg.Prove = !msg.Prove
		}
	case *protoprop.Message_RecoveryPart:
		msg := m.RecoveryPart
		switch r.Intn(8) {
		case 0:
			msg.Height = pickInt64(r)
		case 1:
			msg.Round = pickInt32(r)
		case 2:
			msg.Data = r.Bytes(128)
		case 3:
			msg.Index = pickUint32(r)
		case 4:
			msg.Data = editBytes(r, msg.Data)
		case 5:
			msg.Proof.LeafHash = editBytes(r, msg.Proof.LeafHash)
		case 6:
			if r.Bool() {
				msg.Proof.Total = pickInt64(r)
			} else {
				msg.Proof.Index = pickInt64(r)
			}
		case 7:
			switch r.Intn(3) {
			case 0:
				if len(msg.Proof.Aunts) > 0 {
					msg.Proof.Aunts = msg.Proof.Aunts[:len(msg.Proof.Aunts)-1]
				}
			case 1:
				if len(msg.Proof.Aunts) <= merkle.MaxAunts {
					msg.Proof.Aunts = append(msg.Proof.Aunts, bytes.Repeat([]byte{r.Byte()}, tmhash.Size))
				}
			case 2:
				if len(msg.Proof.Aunts) > 0 {
					i := r.Intn(len(msg.Proof.Aunts))
					msg.Proof.Aunts[i] = editBytes(r, msg.Proof.Aunts[i])
				}
			}
		}
	}
}

// editCompactBlock edits one compact block field. Edit 8 resizes the part
// hashes consistently and edit 7 can recompute both roots, so that edited
// blocks still reach Proofs and the overlap check.
func editCompactBlock(r *fuzzinput.Reader, cb *protoprop.CompactBlock) {
	switch r.Intn(9) {
	case 0:
		cb.BpHash = editBytes(r, cb.BpHash)
	case 1:
		cb.Signature = editBytes(r, cb.Signature)
	case 2:
		switch r.Intn(3) {
		case 0:
			if len(cb.Blobs) < 64 {
				cb.Blobs = append(cb.Blobs, &protoprop.TxMetaData{Hash: bytes.Repeat([]byte{r.Byte()}, tmhash.Size), Start: pickUint32(r), End: pickUint32(r)})
			}
		case 1:
			if len(cb.Blobs) > 0 {
				cb.Blobs = cb.Blobs[:len(cb.Blobs)-1]
			}
		case 2:
			if len(cb.Blobs) > 0 && cb.Blobs[0] != nil {
				cb.Blobs[0].Start, cb.Blobs[0].End = cb.Blobs[0].End, cb.Blobs[0].Start
			}
		}
	case 3:
		if len(cb.Blobs) > 0 && cb.Blobs[0] != nil {
			b := cb.Blobs[r.Intn(len(cb.Blobs))]
			b.Hash = editBytes(r, b.Hash)
		}
	case 4:
		cb.LastLength = pickUint32(r)
	case 5:
		if cb.Proposal == nil {
			return
		}
		p := cb.Proposal
		switch r.Intn(5) {
		case 0:
			p.Height = pickInt64(r)
		case 1:
			p.Round = pickInt32(r)
		case 2:
			p.BlockID.PartSetHeader.Total = pickUint32(r)
		case 3:
			p.BlockID.PartSetHeader.Hash = editBytes(r, p.BlockID.PartSetHeader.Hash)
		case 4:
			p.Signature = editBytes(r, p.Signature)
		}
	case 6:
		if r.Byte() == 0 {
			cb.Proposal = nil
		}
	case 7:
		if len(cb.PartsHashes) > 0 {
			i := r.Intn(len(cb.PartsHashes))
			cb.PartsHashes[i] = editBytes(r, cb.PartsHashes[i])
		}
		if r.Byte() != 0 {
			fixCompactBlockRoots(cb)
		}
	case 8:
		total := r.Intn(9)
		cb.PartsHashes = make([][]byte, ParityRatio*total)
		for i := range cb.PartsHashes {
			cb.PartsHashes[i] = tmhash.Sum([]byte{byte(i)})
		}
		if cb.Proposal != nil {
			cb.Proposal.BlockID.PartSetHeader.Total = uint32(total)
		}
		fixCompactBlockRoots(cb)
	}
}

// fixCompactBlockRoots recomputes the part set and parity roots from the part
// hashes, the way an honest proposer derives them.
func fixCompactBlockRoots(cb *protoprop.CompactBlock) {
	if cb.Proposal == nil || len(cb.PartsHashes)%ParityRatio != 0 {
		return
	}
	half := len(cb.PartsHashes) / ParityRatio
	root, _ := merkle.ParallelProofsFromLeafHashes(cb.PartsHashes[:half])
	parityRoot, _ := merkle.ParallelProofsFromLeafHashes(cb.PartsHashes[half:])
	cb.Proposal.BlockID.PartSetHeader.Hash = root
	cb.BpHash = parityRoot
}

// pickInt64 returns a boundary value or one read from r.
func pickInt64(r *fuzzinput.Reader) int64 {
	switch r.Intn(6) {
	case 0:
		return -1
	case 1:
		return 0
	case 2:
		return 1
	case 3:
		return math.MaxInt64
	case 4:
		return int64(r.Byte())
	default:
		return r.Int64()
	}
}

// pickInt32 returns a boundary value or one read from r.
func pickInt32(r *fuzzinput.Reader) int32 {
	switch r.Intn(5) {
	case 0:
		return -1
	case 1:
		return math.MaxInt32
	case 2:
		return math.MinInt32
	case 3:
		return int32(r.Byte())
	default:
		return r.Int32()
	}
}

// pickUint32 returns a small value, a boundary value or one read from r.
func pickUint32(r *fuzzinput.Reader) uint32 {
	switch r.Intn(3) {
	case 0:
		return r.Uint32()
	case 1:
		return uint32(r.Byte())
	default:
		return math.MaxUint32
	}
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
