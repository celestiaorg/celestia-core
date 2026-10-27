package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func blockDataField(data []byte) []byte {
	bz := protowire.AppendTag(nil, 2, protowire.BytesType)
	return protowire.AppendBytes(bz, data)
}

func TestBlockUnmarshalWithTxLimit(t *testing.T) {
	for _, tx := range [][]byte{nil, {1}} {
		entry := protowire.AppendBytes([]byte{0x0a}, tx)
		for _, count := range []int{0, 999, 1000, 1001, 100_000} {
			wire := blockDataField(bytes.Repeat(entry, count))
			var block Block
			err := block.UnmarshalWithTxLimit(wire, 1000)
			if count > 1000 {
				require.ErrorIs(t, err, ErrTooManyBlockTxs)
				require.Empty(t, block.Data.Txs, "reject before allocating tx slices")
			} else {
				require.NoError(t, err)
				require.Len(t, block.Data.Txs, count)
			}
		}
	}
}

func TestBlockTxLimitAcrossRepeatedDataFields(t *testing.T) {
	// Singular embedded messages merge on the wire. Splitting the repeated
	// transactions across Data fields must not reset the count.
	part := blockDataField(bytes.Repeat([]byte{0x0a, 0}, 501))
	wire := append(append([]byte{}, part...), part...)
	// A supplied hash and an unknown field must not bypass the check.
	wire = append(wire, blockDataField(protowire.AppendBytes([]byte{0x32}, make([]byte, 32)))...)
	wire = protowire.AppendTag(wire, 100, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 1)
	var block Block
	require.ErrorIs(t, block.UnmarshalWithTxLimit(wire, 1000), ErrTooManyBlockTxs)
	require.Empty(t, block.Data.Txs)

	// The ordinary decoder remains available for historical blocks.
	require.NoError(t, block.Unmarshal(wire))
	require.Len(t, block.Data.Txs, 1002)
}

func TestBlockTxLimitMalformed(t *testing.T) {
	for _, wire := range [][]byte{
		{0x12}, {0x12, 0xff}, {0x12, 2, 0x0a}, {0x10, 0},
		blockDataField([]byte{0x08, 0}), blockDataField([]byte{0x0a, 2, 1}),
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 1},
	} {
		var block Block
		require.Error(t, block.UnmarshalWithTxLimit(wire, 1000), "%x", wire)
	}
}

func TestBlockTxLimitBoundedAllocation(t *testing.T) {
	wire := blockDataField(bytes.Repeat([]byte{0x0a, 0}, 100_000))
	allocs := testing.AllocsPerRun(10, func() {
		var block Block
		if err := block.UnmarshalWithTxLimit(wire, 1000); err != ErrTooManyBlockTxs {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	require.Less(t, allocs, float64(20))
}

func TestBlockOtherCardinalityLimitsBeforeAllocation(t *testing.T) {
	commit := protowire.AppendTag(nil, 4, protowire.BytesType)
	commit = protowire.AppendBytes(commit, bytes.Repeat([]byte{0x22, 0}, MaxBlockCommitSignatures+1))
	evidence := protowire.AppendTag(nil, 3, protowire.BytesType)
	evidence = protowire.AppendBytes(evidence, bytes.Repeat([]byte{0x0a, 0}, MaxBlockEvidenceItems+1))

	for _, tc := range []struct {
		wire []byte
		err  error
	}{
		{commit, ErrTooManyBlockCommitSignatures},
		{evidence, ErrTooManyBlockEvidence},
	} {
		allocs := testing.AllocsPerRun(5, func() {
			var block Block
			if err := block.UnmarshalWithTxLimit(tc.wire, 1000); err != tc.err {
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

func TestBlockEvidenceAcceptedBoundaryIsBounded(t *testing.T) {
	evidence := protowire.AppendTag(nil, 3, protowire.BytesType)
	evidence = protowire.AppendBytes(evidence, bytes.Repeat([]byte{0x0a, 0}, MaxBlockEvidenceItems))
	allocs := testing.AllocsPerRun(1, func() {
		var block Block
		if err := block.UnmarshalWithTxLimit(evidence, 1000); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	require.Less(t, allocs, float64(MaxBlockEvidenceItems*2))
}

func BenchmarkBlockTxLimitReject(b *testing.B) {
	wire := blockDataField(bytes.Repeat([]byte{0x0a, 0}, 100_000))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var block Block
		if err := block.UnmarshalWithTxLimit(wire, 1000); err != ErrTooManyBlockTxs {
			b.Fatal(err)
		}
	}
}
