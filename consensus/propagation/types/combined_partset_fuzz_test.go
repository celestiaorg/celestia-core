package types

import (
	"testing"

	"github.com/stretchr/testify/require"

	cmttypes "github.com/cometbft/cometbft/types"
)

const fuzzMaxParts = 6

// FuzzCombinedPartSetDecode adds a fuzzed selection of original and parity
// parts (genuine, corrupted, truncated or with a mismatched proof) to a
// CombinedPartSet built from a compact block, then decodes it. Only parts that
// verify against the committed roots may be stored, and a successful decode
// must reproduce the original block exactly. With honest parity data and the
// right last-part length, decoding enough genuine parts must succeed.
func FuzzCombinedPartSetDecode(f *testing.F) {
	f.Add(uint32(1000), uint16(0x0003), false, uint32(0), []byte{})
	f.Add(uint32(200_000), uint16(0x00f0), false, uint32(0), []byte{})
	f.Add(uint32(200_000), uint16(0x00f5), true, uint32(0), []byte{0, 1, 2, 3, 0, 0, 0, 0})
	f.Add(uint32(70_000), uint16(0x000c), false, uint32(1<<31|5), []byte{})
	f.Fuzz(func(t *testing.T, size uint32, present uint16, byzantineParity bool, lastLenOverride uint32, variants []byte) {
		dataLen := 1 + int(size%(fuzzMaxParts*cmttypes.BlockPartSizeBytes))
		data := make([]byte, dataLen)
		for i := range data {
			data[i] = byte(i*31 + int(size))
		}
		ops, err := cmttypes.NewPartSetFromData(data, cmttypes.BlockPartSizeBytes)
		require.NoError(t, err)
		eps, lastLen, err := cmttypes.Encode(ops, cmttypes.BlockPartSizeBytes)
		require.NoError(t, err)
		total := ops.Total()

		if byzantineParity {
			// A byzantine proposer commits to parity data that is not the
			// erasure coding of the block.
			parity := make([]byte, 0, int(total)*int(cmttypes.BlockPartSizeBytes))
			for i := 0; i < int(total); i++ {
				parity = append(parity, eps.GetPart(i).Bytes...)
			}
			parity[int(size)%len(parity)] ^= 0xff
			eps, err = cmttypes.NewPartSetFromData(parity, cmttypes.BlockPartSizeBytes)
			require.NoError(t, err)
		}

		cbLastLen := uint32(lastLen)
		if lastLenOverride>>31 == 1 {
			cbLastLen = lastLenOverride % (cmttypes.BlockPartSizeBytes + 1)
		}
		cb := &CompactBlock{
			BpHash:   eps.Hash(),
			LastLen:  cbLastLen,
			Proposal: cmttypes.Proposal{BlockID: cmttypes.BlockID{PartSetHeader: ops.Header()}},
		}
		cps := NewCombinedSetFromCompactBlock(cb)

		genuine := func(i uint32) *cmttypes.Part {
			if i < total {
				return ops.GetPart(int(i))
			}
			return eps.GetPart(int(i - total))
		}

		// accepted tracks which AddPart calls succeeded, independently of the
		// set's own bitmap, so a bitmap or CanDecode bug cannot hide itself.
		accepted := make([]bool, 2*total)
		acceptedCount := uint32(0)
		for i := uint32(0); i < 2*total; i++ {
			if present&(1<<i) == 0 {
				continue
			}
			want := genuine(i)
			var variant byte
			if len(variants) > 0 {
				variant, variants = variants[0], variants[1:]
			}
			part := &RecoveryPart{Index: i, Data: append([]byte{}, want.Bytes...)}
			proof := want.Proof
			switch variant % 4 {
			case 1:
				part.Data[int(variant)*997%len(part.Data)] ^= 0x5a
			case 2:
				part.Data = part.Data[:len(part.Data)/2+1]
			case 3:
				proof = genuine((i + 1) % (2 * total)).Proof
			}
			added, err := cps.AddPart(part, proof)
			if variant%4 == 0 {
				// An unmodified part with its own proof is genuine and must
				// be accepted, or acceptance regressions would go unnoticed.
				require.NoError(t, err, "genuine part %d rejected", i)
				require.True(t, added, "genuine part %d not added", i)
			}
			if added {
				require.NoError(t, err)
				require.Equal(t, []byte(want.Bytes), part.Data, "part %d accepted with data that does not match its root", i)
				accepted[i] = true
				acceptedCount++
			}
			for j := uint32(0); j < 2*total; j++ {
				require.Equal(t, accepted[j], cps.HasPart(int(j)), "bitmap disagrees with accepted parts at %d", j)
				stored := storedPart(cps, total, j)
				if accepted[j] {
					require.NotNil(t, stored, "accepted part %d not stored", j)
					require.Equal(t, genuine(j).Bytes, stored.Bytes, "stored part %d differs from the genuine part", j)
				} else {
					require.Nil(t, stored, "part %d stored without being accepted", j)
				}
			}
		}

		if acceptedCount >= total {
			require.True(t, cps.CanDecode(), "%d of %d parts accepted but CanDecode is false", acceptedCount, total)
		}
		if !cps.CanDecode() {
			return
		}
		honest := !byzantineParity && cbLastLen == uint32(lastLen)
		err = cps.Decode()
		if err != nil {
			require.False(t, honest, "decoding enough genuine parts failed: %v", err)
			return
		}
		require.True(t, cps.IsComplete())
		got := make([]byte, 0, dataLen)
		for i := 0; i < int(total); i++ {
			got = append(got, cps.Original().GetPart(i).Bytes...)
		}
		require.Equal(t, data, got, "decoded block differs from the original")
		for i := 0; i < int(total); i++ {
			require.Equal(t, eps.GetPart(i).Bytes, cps.Parity().GetPart(i).Bytes, "decoded parity part %d differs from the committed parity", i)
		}
		require.Equal(t, int(2*total), len(cps.BitArray().GetTrueIndices()))
	})
}

// storedPart returns combined part i as stored in the underlying original or
// parity part set, bypassing the combined bitmap, or nil if it is absent.
func storedPart(cps *CombinedPartSet, total, i uint32) *cmttypes.Part {
	if i < total {
		return cps.Original().GetPart(int(i))
	}
	return cps.Parity().GetPart(int(i - total))
}

// FuzzTxsToPartsMetadata feeds TxsToParts attacker-chosen tx metadata, as a
// byzantine proposer controls it in a compact block. TxsToParts must not
// panic and may only return parts at valid indexes whose length matches the
// part size, or lastPartLen for a short final part.
func FuzzTxsToPartsMetadata(f *testing.F) {
	f.Add(uint8(4), uint32(1000), []byte{0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 2, 0})
	f.Add(uint8(1), uint32(10), []byte{0, 0, 0, 0, 10, 0, 0, 0})
	f.Fuzz(func(t *testing.T, partCount uint8, lastPartLen uint32, meta []byte) {
		count := uint32(partCount%16) + 1
		partSize := cmttypes.BlockPartSizeBytes
		limit := count*partSize + 16
		var txs []UnmarshalledTx
		budget := 4 * int(partSize) * 4
		for len(meta) >= 8 && len(txs) < 64 {
			start := (uint32(meta[0]) | uint32(meta[1])<<8 | uint32(meta[2])<<16 | uint32(meta[3])<<24) % limit
			end := (uint32(meta[4]) | uint32(meta[5])<<8 | uint32(meta[6])<<16 | uint32(meta[7])<<24) % limit
			meta = meta[8:]
			n := 0
			if end > start {
				n = int(end - start)
			}
			if n > budget {
				break
			}
			budget -= n
			tx := make([]byte, n)
			for i := range tx {
				tx[i] = byte(len(txs))
			}
			txs = append(txs, UnmarshalledTx{MetaData: TxMetaData{Start: start, End: end}, TxBytes: tx})
		}
		lastLen := lastPartLen % (partSize + 2)
		parts, err := TxsToParts(txs, count, partSize, lastLen)
		if err != nil {
			return
		}
		seen := make(map[uint32]bool)
		for _, p := range parts {
			require.Less(t, p.Index, count)
			require.False(t, seen[p.Index], "duplicate part %d", p.Index)
			seen[p.Index] = true
			wantLen := int(partSize)
			if p.Index == count-1 && lastLen > 0 && lastLen < partSize {
				wantLen = int(lastLen)
			}
			require.Len(t, p.Bytes, wantLen, "part %d has the wrong length", p.Index)
		}
	})
}
