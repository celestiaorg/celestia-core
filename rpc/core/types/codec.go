package coretypes

import (
	amino "github.com/tendermint/go-amino"

	"github.com/lazyledger/lazyledger-core/types"
)

func RegisterAmino(cdc *amino.Codec) {
	types.RegisterEventDatas(cdc)
	types.RegisterBlockAmino(cdc)
}
