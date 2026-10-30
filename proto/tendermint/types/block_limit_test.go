package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func blockField(num protowire.Number, payload []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), payload)
}

func TestCheckBlockCardinalityTxs(t *testing.T) {
	for _, tx := range [][]byte{nil, {1}} {
		entry := protowire.AppendBytes([]byte{0x0a}, tx)
		for _, count := range []int{0, 999, 1000, 1001, 100_000} {
			err := CheckBlockCardinality(blockField(2, bytes.Repeat(entry, count)), 1000, true)
			if count > 1000 {
				require.ErrorIs(t, err, ErrTooManyBlockTxs)
			} else {
				require.NoError(t, err)
			}
		}
	}
}

func TestCheckBlockCardinalityRepeatedDataFields(t *testing.T) {
	// Unmarshal merges repeated Data fields, so their txs add up.
	part := blockField(2, bytes.Repeat([]byte{0x0a, 0}, 501))
	wire := append(append([]byte{}, part...), part...)
	wire = append(wire, blockField(2, protowire.AppendBytes([]byte{0x32}, make([]byte, 32)))...)
	wire = protowire.AppendVarint(protowire.AppendTag(wire, 100, protowire.VarintType), 1)
	require.ErrorIs(t, CheckBlockCardinality(wire, 1000, true), ErrTooManyBlockTxs)

	var block Block
	require.NoError(t, block.Unmarshal(wire))
	require.Len(t, block.Data.Txs, 1002)
}

func TestCheckBlockCardinalityMalformed(t *testing.T) {
	for _, wire := range [][]byte{
		{0x12}, {0x12, 0xff}, {0x12, 2, 0x0a}, blockField(2, []byte{0x0a, 2, 1}),
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 1},
	} {
		require.Error(t, CheckBlockCardinality(wire, 1000, true), "%x", wire)
		require.Error(t, new(Block).Unmarshal(wire), "%x", wire)
	}
}

// Unmarshal drops varint bits past 64; rejecting such bytes would split v10
// nodes from nodes without the check.
func TestCheckBlockCardinalityAcceptsWhatUnmarshalAccepts(t *testing.T) {
	block, err := (&Block{Data: Data{Txs: [][]byte{{1}}}}).Marshal()
	require.NoError(t, err)
	overlongTag := append(append([]byte{}, block...), 0xa0, 0x86, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02, 0x01)
	overlongLen := append(append([]byte{}, block...), 0x12, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02)
	for _, wire := range [][]byte{overlongTag, overlongLen} {
		require.NoError(t, new(Block).Unmarshal(wire))
		require.NoError(t, CheckBlockCardinality(wire, 1000, false))
	}
}

// Evidence.sum is a oneof: Unmarshal keeps only the last value, so before v11
// a replaced light client attack must not count.
func TestCheckBlockCardinalityEvidenceOneof(t *testing.T) {
	junk := blockField(2, bytes.Repeat([]byte{0x1a, 0}, MaxBlockEvidenceElements+1))
	item := append(junk, blockField(1, nil)...) // then duplicate_vote_evidence
	wire := blockField(3, blockField(1, item))

	var block Block
	require.NoError(t, block.Unmarshal(wire))
	require.NotNil(t, block.Evidence.Evidence[0].GetDuplicateVoteEvidence())
	require.NoError(t, CheckBlockCardinality(wire, 1000, false))
	require.ErrorIs(t, CheckBlockCardinality(wire, 1000, true), ErrTooManyBlockEvidenceElements)

	// The last value still counts in both modes.
	last := blockField(3, blockField(1, junk))
	require.ErrorIs(t, CheckBlockCardinality(last, 1000, false), ErrTooManyBlockEvidenceElements)
}

func TestCheckBlockCardinalityOtherLimitsBeforeAllocation(t *testing.T) {
	sigs := blockField(4, bytes.Repeat([]byte{0x22, 0}, MaxBlockCommitSignatures+1))
	evidence := blockField(3, bytes.Repeat([]byte{0x0a, 0}, MaxBlockEvidenceItems+1))
	conflict := blockField(2, blockField(1, blockField(1, blockField(2, bytes.Repeat([]byte{0x22, 0}, MaxBlockEvidenceElements+1)))))
	valSet := blockField(2, blockField(1, blockField(2, bytes.Repeat([]byte{0x0a, 0}, MaxBlockEvidenceElements+1))))
	for _, tc := range []struct {
		wire []byte
		err  error
	}{
		{sigs, ErrTooManyBlockCommitSignatures},
		{evidence, ErrTooManyBlockEvidence},
		{blockField(3, blockField(1, conflict)), ErrTooManyBlockEvidenceElements},
		{blockField(3, blockField(1, valSet)), ErrTooManyBlockEvidenceElements},
	} {
		allocs := testing.AllocsPerRun(5, func() {
			if err := CheckBlockCardinality(tc.wire, 1000, false); err != tc.err {
				t.Fatalf("unexpected error: %v", err)
			}
		})
		require.Less(t, allocs, float64(20))
	}
}

func TestBlockEvidenceLimitsExceedValidCelestiaCapacity(t *testing.T) {
	require.Greater(t, MaxBlockEvidenceItems*64, 1_048_576)
	require.Greater(t, MaxBlockEvidenceElements*32, 1_048_576)
}

func BenchmarkCheckBlockCardinalityReject(b *testing.B) {
	wire := blockField(2, bytes.Repeat([]byte{0x0a, 0}, 100_000))
	b.ReportAllocs()
	for range b.N {
		if err := CheckBlockCardinality(wire, 1000, true); err != ErrTooManyBlockTxs {
			b.Fatal(err)
		}
	}
}
