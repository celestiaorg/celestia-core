package types

import (
	"reflect"
	"testing"

	"github.com/lazyledger/lazyledger-core/crypto/tmhash"
	tmrand "github.com/lazyledger/lazyledger-core/libs/rand"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
)

func TestCanonicalizeBlockID(t *testing.T) {
	randhash := tmrand.Bytes(tmhash.Size)
	randDah := makeDAHeaderRandom().ToProto()
	block1 := tmproto.BlockID{
		Hash:                   randhash,
		PartSetHeader:          tmproto.PartSetHeader{Total: 5, Hash: randhash},
		DataAvailabilityHeader: randDah,
	}
	block2 := tmproto.BlockID{
		Hash:                   randhash,
		PartSetHeader:          tmproto.PartSetHeader{Total: 10, Hash: randhash},
		DataAvailabilityHeader: randDah,
	}
	cblock1 := tmproto.CanonicalBlockID{
		Hash:                   randhash,
		PartSetHeader:          tmproto.CanonicalPartSetHeader{Total: 5, Hash: randhash},
		DataAvailabilityHeader: randDah,
	}
	cblock2 := tmproto.CanonicalBlockID{
		Hash:                   randhash,
		PartSetHeader:          tmproto.CanonicalPartSetHeader{Total: 10, Hash: randhash},
		DataAvailabilityHeader: randDah,
	}

	tests := []struct {
		name string
		args tmproto.BlockID
		want *tmproto.CanonicalBlockID
	}{
		{"first", block1, &cblock1},
		{"second", block2, &cblock2},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalizeBlockID(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CanonicalizeBlockID() = %v, want %v", got, tt.want)
			}
		})
	}
}
