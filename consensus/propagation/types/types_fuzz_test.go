package types

import (
	"testing"

	fuzz "github.com/google/gofuzz"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/proto/tendermint/crypto"
	protobits "github.com/tendermint/tendermint/proto/tendermint/libs/bits"
	"github.com/tendermint/tendermint/proto/tendermint/propagation"
	prototypes "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/types"
)

// TestFuzzMsgFromProto tests the MsgFromProto function with expected message types
// and mutates them with high probability of nil values
func TestFuzzMsgFromProto(t *testing.T) {
	seedCorpus := []struct {
		name   string
		msg    *propagation.Message
		mutate func(fz *fuzz.Fuzzer, msg *propagation.Message)
	}{
		{
			name: "message: CompactBlock",
			msg: &propagation.Message{
				Sum: &propagation.Message_CompactBlock{
					CompactBlock: &propagation.CompactBlock{
						BpHash:    rand.Bytes(tmhash.Size),
						Blobs:     []*propagation.TxMetaData{{Hash: rand.Bytes(tmhash.Size), Start: 0, End: 10}},
						Signature: rand.Bytes(types.MaxSignatureSize),
						Proposal:  &prototypes.Proposal{},
					},
				},
			},
			mutate: func(fz *fuzz.Fuzzer, msg *propagation.Message) {
				fz.Fuzz(msg.Sum.(*propagation.Message_CompactBlock).CompactBlock)
			},
		},
		{
			name: "message: HaveParts",
			msg: &propagation.Message{
				Sum: &propagation.Message_HaveParts{
					HaveParts: &propagation.HaveParts{
						Height: 1,
						Round:  0,
						Parts:  []*propagation.PartMetaData{{Index: 0, Hash: rand.Bytes(tmhash.Size)}},
					},
				},
			},
			mutate: func(fz *fuzz.Fuzzer, msg *propagation.Message) {
				fz.Fuzz(msg.Sum.(*propagation.Message_HaveParts).HaveParts)
			},
		},
		{
			name: "message: WantParts",
			msg: &propagation.Message{
				Sum: &propagation.Message_WantParts{
					WantParts: &propagation.WantParts{
						Parts:  protobits.BitArray{Bits: 10, Elems: []uint64{0}},
						Height: 1,
						Round:  0,
						Prove:  true,
					},
				},
			},
			mutate: func(fz *fuzz.Fuzzer, msg *propagation.Message) {
				fz.Fuzz(msg.Sum.(*propagation.Message_WantParts).WantParts)
			},
		},
		{
			name: "message: RecoveryPart",
			msg: &propagation.Message{
				Sum: &propagation.Message_RecoveryPart{
					RecoveryPart: &propagation.RecoveryPart{
						Height: 1,
						Round:  0,
						Index:  0,
						Data:   rand.Bytes(100),
						Proof:  crypto.Proof{},
					},
				},
			},
			mutate: func(fz *fuzz.Fuzzer, msg *propagation.Message) {
				fz.Fuzz(msg.Sum.(*propagation.Message_RecoveryPart).RecoveryPart)
			},
		},
	}
	fz := fuzz.New().NilChance(0.9).NumElements(1, 3)

	for i := 0; i < 10000; i++ {
		// Cycle through the seed corpus in round robin fashion
		seed := seedCorpus[i%len(seedCorpus)]
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic:%v in %s:", r, seed.name)
			}
		}()
		seed.mutate(fz, seed.msg)
		_, _ = MsgFromProto(seed.msg)
	}
}
