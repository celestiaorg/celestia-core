package types

import (
	"fmt"
	"io"
	"testing"

	"github.com/cometbft/cometbft/crypto"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/gogoproto/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
)

const (
	testPartSize = 65536 // 64KB ...  4096 // 4KB
)

func TestBasicPartSet(t *testing.T) {
	// Construct random data of size partSize * 100
	nParts := 100
	data := cmtrand.Bytes(testPartSize * nParts)
	partSet, err := NewPartSetFromData(data, testPartSize)
	require.NoError(t, err)

	assert.NotEmpty(t, partSet.Hash())
	assert.EqualValues(t, nParts, partSet.Total())
	assert.Equal(t, nParts, partSet.BitArray().Size())
	assert.True(t, partSet.HashesTo(partSet.Hash()))
	assert.True(t, partSet.IsComplete())
	assert.EqualValues(t, nParts, partSet.Count())
	assert.EqualValues(t, testPartSize*nParts, partSet.ByteSize())

	// Test adding parts to a new partSet.
	partSet2 := NewPartSetFromHeader(partSet.Header(), testPartSize)

	assert.True(t, partSet2.HasHeader(partSet.Header()))
	for i := 0; i < int(partSet.Total()); i++ {
		part := partSet.GetPart(i)
		// t.Logf("\n%v", part)
		added, err := partSet2.AddPart(part)
		if !added || err != nil {
			t.Errorf("failed to add part %v, error: %v", i, err)
		}
	}
	// adding part with invalid index
	added, err := partSet2.AddPart(&Part{Index: 10000})
	assert.False(t, added)
	assert.Error(t, err)
	// adding existing part
	added, err = partSet2.AddPart(partSet2.GetPart(0))
	assert.False(t, added)
	assert.Nil(t, err)

	assert.Equal(t, partSet.Hash(), partSet2.Hash())
	assert.EqualValues(t, nParts, partSet2.Total())
	assert.EqualValues(t, nParts*testPartSize, partSet.ByteSize())
	assert.True(t, partSet2.IsComplete())

	// Reconstruct data, assert that they are equal.
	data2Reader := partSet2.GetReader()
	data2, err := io.ReadAll(data2Reader)
	require.NoError(t, err)

	assert.Equal(t, data, data2)
}

func TestEncodingDecodingRoundTrip(t *testing.T) {
	type test struct {
		dataSize int
	}

	tests := []test{
		{
			1000, // test single part
		},
		{
			int(BlockPartSizeBytes), // exactly 1 part
		},
		{
			int(BlockPartSizeBytes) - 1,
		},
		{
			int(BlockPartSizeBytes) + 1,
		},
		{
			100000, // > 1 part
		},
		{
			1000000, // many parts
		},
		{
			int(BlockPartSizeBytes) * 100, // exactly 100 parts
		},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("dataSize=%d", tt.dataSize), func(t *testing.T) {
			b1 := MakeBlock(1, MakeData([]Tx{Tx(cmtrand.Bytes(tt.dataSize))}), &Commit{Signatures: []CommitSig{}}, []Evidence{})
			b1.ProposerAddress = cmtrand.Bytes(crypto.AddressSize)

			bp, err := b1.ToProto()
			require.NoError(t, err)

			bz, err := bp.Marshal()
			require.NoError(t, err)

			ops, err := NewPartSetFromData(bz, BlockPartSizeBytes)
			require.NoError(t, err)

			eps, lastPartLen, err := Encode(ops, BlockPartSizeBytes)
			require.NoError(t, err)

			// Remove part 0 to test decode functionality
			ops.mtx.Lock()
			ops.count--
			ops.partsBitArray.SetIndex(0, false)
			// Clear the buffer area for part 0
			start := 0 * int(BlockPartSizeBytes)
			end := start + int(BlockPartSizeBytes)
			for i := start; i < end && i < len(ops.buffer); i++ {
				ops.buffer[i] = 0
			}
			ops.mtx.Unlock()

			ops, _, err = Decode(ops, eps, lastPartLen)
			require.NoError(t, err)

			bz2, err := io.ReadAll(ops.GetReader())
			require.NoError(t, err)

			pbb := new(cmtproto.Block)
			err = proto.Unmarshal(bz2, pbb)
			require.NoError(t, err)

			b2, err := BlockFromProto(pbb)
			require.NoError(t, err)

			require.Equal(t, b1, b2)
		})
	}
}

func TestEncoding(t *testing.T) {
	data := cmtrand.Bytes(testPartSize * 100)
	partSet, err := NewPartSetFromData(data, testPartSize)
	require.NoError(t, err)
	_, _, err = Encode(partSet, BlockPartSizeBytes)
	require.NoError(t, err)
}

func TestWrongProof(t *testing.T) {
	// Construct random data of size partSize * 100
	data := cmtrand.Bytes(testPartSize * 100)
	partSet, err := NewPartSetFromData(data, testPartSize)
	require.NoError(t, err)

	// Test adding a part with wrong data.
	partSet2 := NewPartSetFromHeader(partSet.Header(), testPartSize)

	// Test adding a part with wrong trail.
	part := partSet.GetPart(0)
	part.Proof.Aunts[0][0] += byte(0x01)
	added, err := partSet2.AddPart(part)
	if added || err == nil {
		t.Errorf("expected to fail adding a part with bad trail.")
	}

	// Test adding a part with wrong bytes.
	part = partSet.GetPart(1)
	part.Bytes[0] += byte(0x01)
	added, err = partSet2.AddPart(part)
	if added || err == nil {
		t.Errorf("expected to fail adding a part with bad bytes.")
	}

	// Test adding a part with wrong proof index.
	part = partSet.GetPart(2)
	part.Proof.Index = 1
	added, err = partSet2.AddPart(part)
	if added || err == nil {
		t.Errorf("expected to fail adding a part with bad proof index.")
	}

	// Test adding a part with wrong proof total.
	part = partSet.GetPart(3)
	part.Proof.Total = int64(partSet.Total() - 1)
	added, err = partSet2.AddPart(part)
	if added || err == nil {
		t.Errorf("expected to fail adding a part with bad proof total.")
	}
}

func TestPartSetHeaderValidateBasic(t *testing.T) {
	testCases := []struct {
		testName              string
		malleatePartSetHeader func(*PartSetHeader)
		expectErr             bool
	}{
		{"Good PartSet", func(psHeader *PartSetHeader) {}, false},
		{"Invalid Hash", func(psHeader *PartSetHeader) { psHeader.Hash = make([]byte, 1) }, true},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			data := cmtrand.Bytes(testPartSize * 100)
			ps, err := NewPartSetFromData(data, testPartSize)
			require.NoError(t, err)
			psHeader := ps.Header()
			tc.malleatePartSetHeader(&psHeader)
			assert.Equal(t, tc.expectErr, psHeader.ValidateBasic() != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestPart_ValidateBasic(t *testing.T) {
	testCases := []struct {
		testName     string
		malleatePart func(*Part)
		expectErr    bool
	}{
		{"Good Part", func(pt *Part) {}, false},
		{"Too big part", func(pt *Part) { pt.Bytes = make([]byte, BlockPartSizeBytes+1) }, true},
		{"Good small last part", func(pt *Part) {
			pt.Index = 1
			pt.Bytes = make([]byte, BlockPartSizeBytes-1)
			pt.Proof.Total = 2
			pt.Proof.Index = 1
		}, false},
		{"Too small inner part", func(pt *Part) {
			pt.Index = 0
			pt.Bytes = make([]byte, BlockPartSizeBytes-1)
			pt.Proof.Total = 2
		}, true},
		{"Too big proof", func(pt *Part) {
			pt.Proof = merkle.Proof{
				Total:    2,
				Index:    1,
				LeafHash: make([]byte, 1024*1024),
			}
			pt.Index = 1
		}, true},
		{"Index mismatch", func(pt *Part) {
			pt.Index = 1
			pt.Proof.Index = 0
		}, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			data := cmtrand.Bytes(testPartSize * 100)
			ps, err := NewPartSetFromData(data, testPartSize)
			require.NoError(t, err)
			part := ps.GetPart(0)
			tc.malleatePart(part)
			assert.Equal(t, tc.expectErr, part.ValidateBasic() != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestParSetHeaderProtoBuf(t *testing.T) {
	testCases := []struct {
		msg     string
		ps1     *PartSetHeader
		expPass bool
	}{
		{"success empty", &PartSetHeader{}, true},
		{
			"success",
			&PartSetHeader{Total: 1, Hash: []byte("hash")}, true,
		},
	}

	for _, tc := range testCases {
		protoBlockID := tc.ps1.ToProto()

		psh, err := PartSetHeaderFromProto(&protoBlockID)
		if tc.expPass {
			require.Equal(t, tc.ps1, psh, tc.msg)
		} else {
			require.Error(t, err, tc.msg)
		}
	}
}

func TestPartProtoBuf(t *testing.T) {
	proof := merkle.Proof{
		Total:    1,
		Index:    1,
		LeafHash: cmtrand.Bytes(32),
	}
	testCases := []struct {
		msg     string
		ps1     *Part
		expPass bool
	}{
		{"failure empty", &Part{}, false},
		{"failure nil", nil, false},
		{
			"success",
			&Part{Index: 1, Bytes: cmtrand.Bytes(32), Proof: proof}, true,
		},
	}

	for _, tc := range testCases {
		proto, err := tc.ps1.ToProto()
		if tc.expPass {
			require.NoError(t, err, tc.msg)
		}

		p, err := PartFromProto(proto)
		if tc.expPass {
			require.NoError(t, err)
			require.Equal(t, tc.ps1, p, tc.msg)
		}
	}
}

func BenchmarkPartSetRoundTrip(b *testing.B) {
	write := func(b *testing.B, dataSize int, partSize uint32) *PartSet {
		b.ReportAllocs()
		b.StopTimer()
		data := cmtrand.Bytes(dataSize)
		b.StartTimer()
		partSet, err := benchPartSetFromData(b, data, partSize)
		if err != nil {
			b.Error(err)
		}
		return partSet
	}
	read := func(b *testing.B, partSet *PartSet, expectedSize int) {
		b.ReportAllocs()
		all, err := io.ReadAll(partSet.GetReader())
		if err != nil {
			b.Error(err)
		}
		if len(all) != expectedSize {
			b.Errorf("expected %d bytes, got %d", expectedSize, len(all))
		}
	}

	cases := []struct {
		dataSize int
		partSize uint32
	}{
		{1000, 256},
		{MaxBlockSizeBytes, BlockPartSizeBytes},
		{4 * MaxBlockSizeBytes, BlockPartSizeBytes},
	}

	for c := range cases {
		b.Run(fmt.Sprintf("dataSize=%d,partSize=%d", cases[c].dataSize, cases[c].partSize), func(b *testing.B) {
			partSets := make([]*PartSet, 0, 10)
			b.Run("write", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					partSet := write(b, cases[c].dataSize, cases[c].partSize)
					partSets = append(partSets, partSet)
				}
			})
			b.Run("read", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					read(b, partSets[i%len(partSets)], cases[c].dataSize)
				}
			})
		})
	}
}

func benchPartSetFromData(b *testing.B, data []byte, partSize uint32) (ops *PartSet, err error) {
	b.StopTimer()
	total := (uint32(len(data)) + partSize - 1) / partSize
	chunks := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		chunk := data[i*partSize : cmtmath.MinInt(len(data), int((i+1)*partSize))]
		chunks[i] = chunk
	}

	// Compute merkle proofs
	root, proofs := merkle.ProofsFromByteSlices(chunks)

	ops = NewPartSetFromHeader(PartSetHeader{
		Total: total,
		Hash:  root,
	}, partSize)

	b.StartTimer()
	for index, chunk := range chunks {
		added, err := ops.AddPart(&Part{
			Index: uint32(index),
			Bytes: chunk,
			Proof: *proofs[index],
		})
		if err != nil {
			return nil, err
		}
		if !added {
			return nil, fmt.Errorf("couldn't add part %d when creating ops", index)
		}
	}

	if len(chunks[len(chunks)-1]) < int(partSize) {
		padded := make([]byte, partSize)
		copy(padded, chunks[len(chunks)-1])
		chunks[len(chunks)-1] = padded
	}

	return ops, nil
}
