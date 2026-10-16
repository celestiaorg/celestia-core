package propagation

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	propproto "github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
)

func TestPropagationPrecheck(t *testing.T) {
	channels := (&Reactor{}).GetChannels()
	for _, channel := range channels {
		require.NotNil(t, channel.RecvMessagePrecheck)
	}

	appendEntries := func(outer, inner protowire.Number, count int) []byte {
		payload := make([]byte, 0, count*2)
		for range count {
			payload = protowire.AppendTag(payload, inner, protowire.BytesType)
			payload = protowire.AppendBytes(payload, nil)
		}
		message := protowire.AppendTag(nil, outer, protowire.BytesType)
		return protowire.AppendBytes(message, payload)
	}
	wrap := func(field protowire.Number, payload []byte) []byte {
		message := protowire.AppendTag(nil, field, protowire.BytesType)
		return protowire.AppendBytes(message, payload)
	}
	check := channels[1].RecvMessagePrecheck
	for _, tc := range []struct {
		name    string
		message []byte
		wantErr bool
	}{
		{"empty", nil, false},
		{"compact block count at limit", appendEntries(1, 6, 2*int(types.MaxBlockPartsCount)), false},
		{"excess compact block hashes", appendEntries(1, 6, 2*int(types.MaxBlockPartsCount)+1), true},
		{"excess compact block blobs", appendEntries(1, 2, maxCompactBlockBlobs+1), true},
		{"reported compact block shape", appendEntries(1, 2, 250_000), true},
		{"excess have parts", appendEntries(2, 3, 2*int(types.MaxBlockPartsCount)+1), true},
		{"malformed nested field", []byte{0x0a, 0x01, 0xff}, true},
		{"malformed outer field", []byte{0x0a, 0x05}, true},
		{"other message", appendEntries(3, 3, 1), false},
		{"recovery part aunts at limit", wrap(4, appendEntries(5, 4, merkle.MaxAunts)), false},
		{"excess recovery part aunts", wrap(4, appendEntries(5, 4, merkle.MaxAunts+1)), true},
		{"repeated recovery part proofs", wrap(4, append(appendEntries(5, 4, merkle.MaxAunts), appendEntries(5, 4, 1)...)), true},
		{"malformed recovery part proof", wrap(4, []byte{0x2a, 0x01, 0xff}), true},
		{"repeated compact blocks", append(wrap(1, nil), wrap(1, nil)...), true},
		{"mixed message types", append(wrap(1, nil), wrap(2, nil)...), true},
		{"want parts elems at limit", wrap(3, wrap(1, wrap(2, make([]byte, maxWantPartsElems)))), false},
		{"excess packed want parts elems", wrap(3, wrap(1, wrap(2, make([]byte, maxWantPartsElems+1)))), true},
		{"excess unpacked want parts elems", wrap(3, wrap(1, bytes.Repeat([]byte{0x10, 0x00}, maxWantPartsElems+1))), true},
		{"repeated want parts bit arrays", wrap(3, append(wrap(1, wrap(2, make([]byte, maxWantPartsElems))), wrap(1, wrap(2, []byte{0}))...)), true},
		{"malformed want parts elems", wrap(3, wrap(1, wrap(2, []byte{0xff}))), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.message)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestPropagationPrecheckLegitimateMessages checks that the largest messages an
// honest peer can send for a maximum-size block pass the precheck.
func TestPropagationPrecheckLegitimateMessages(t *testing.T) {
	maxParts := 2 * int(types.MaxBlockPartsCount)
	leaves := make([][]byte, maxParts)
	for i := range leaves {
		leaves[i] = cmtrand.Bytes(32)
	}
	_, proofs := merkle.ProofsFromByteSlices(leaves)

	partsHashes := make([][]byte, maxParts)
	haves := make([]*propproto.PartMetaData, maxParts)
	for i := range partsHashes {
		partsHashes[i] = cmtrand.Bytes(32)
		haves[i] = &propproto.PartMetaData{Index: uint32(i), Hash: partsHashes[i]}
	}
	compactBlock := &propproto.CompactBlock{
		BpHash:      cmtrand.Bytes(32),
		Signature:   cmtrand.Bytes(64),
		PartsHashes: partsHashes,
	}
	// Fill the rest of the channel message with minimal transaction metadata.
	for i := uint32(0); ; i++ {
		compactBlock.Blobs = append(compactBlock.Blobs, &propproto.TxMetaData{Hash: cmtrand.Bytes(32), Start: i, End: i + 1})
		if proto.Size(&propproto.Message{Sum: &propproto.Message_CompactBlock{CompactBlock: compactBlock}}) > maxMsgSize {
			compactBlock.Blobs = compactBlock.Blobs[:len(compactBlock.Blobs)-1]
			break
		}
	}
	proof := proofs[len(proofs)-1].ToProto()

	channels := (&Reactor{}).GetChannels()
	for _, tc := range []struct {
		name string
		msg  *propproto.Message
	}{
		{"compact block", &propproto.Message{Sum: &propproto.Message_CompactBlock{CompactBlock: compactBlock}}},
		{"have parts", &propproto.Message{Sum: &propproto.Message_HaveParts{HaveParts: &propproto.HaveParts{Height: 1, Parts: haves}}}},
		{"want parts", &propproto.Message{Sum: &propproto.Message_WantParts{WantParts: &propproto.WantParts{
			Parts: *bits.NewBitArray(maxParts).ToProto(), Height: 1, MissingPartsCount: int32(types.MaxBlockPartsCount),
		}}}},
		{"recovery part", &propproto.Message{Sum: &propproto.Message_RecoveryPart{RecoveryPart: &propproto.RecoveryPart{
			Height: 1, Index: uint32(maxParts - 1), Data: cmtrand.Bytes(int(types.BlockPartSizeBytes)), Proof: *proof,
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bz, err := proto.Marshal(tc.msg)
			require.NoError(t, err)
			require.LessOrEqual(t, len(bz), maxMsgSize)
			for _, channel := range channels {
				require.NoError(t, channel.RecvMessagePrecheck(bz))
			}
		})
	}
}
