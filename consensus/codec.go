package consensus

import (
	amino "github.com/tendermint/go-amino"

	"github.com/lazyledger/lazyledger-core/types"
)

var cdc = amino.NewCodec()

func init() {
	RegisterMessages(cdc)
	RegisterWALMessages(cdc)
	types.RegisterBlockAmino(cdc)
}
