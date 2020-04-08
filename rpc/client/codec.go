package client

import (
	amino "github.com/tendermint/go-amino"

	"github.com/lazyledger/lazyledger-core/types"
)

var cdc = amino.NewCodec()

func init() {
	types.RegisterEvidences(cdc)
}
