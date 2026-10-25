package types

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/libs/bits"
	protoprop "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
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
	proposal := types.NewProposal(10, 1, -1, types.BlockID{
		Hash:          hash(9),
		PartSetHeader: types.PartSetHeader{Total: 2, Hash: root},
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
	wants := &WantParts{Parts: bits.NewBitArray(4), Height: 10, Round: 1, Prove: true, MissingPartsCount: 2}
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
// message that decodes must survive a proto round trip unchanged.
func checkPropagationMessage(t *testing.T, raw []byte) {
	t.Helper()
	var pb protoprop.Message
	if err := proto.Unmarshal(raw, &pb); err != nil {
		return
	}
	msg, err := MsgFromProto(&pb)
	if err != nil {
		return
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
		require.Equal(t, m.Parts.String(), again.Parts.String())
	case *RecoveryPart:
		if m.Proof != nil {
			if root := m.Proof.ComputeRootHash(); root != nil {
				require.NoError(t, m.Proof.Verify(root, m.Data))
			}
		}
	default:
		t.Fatalf("unexpected message type %T", msg)
	}
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

// FuzzPropagationMessage feeds mutated wire bytes through the propagation
// reactor's message decoding and the per-message helpers it calls.
func FuzzPropagationMessage(f *testing.F) {
	for _, raw := range propagationSeeds(f) {
		f.Add(raw)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		checkPropagationMessage(t, raw)
	})
}
