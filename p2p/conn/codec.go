package conn

import (
	amino "github.com/tendermint/go-amino"

	cryptoamino "github.com/lazyledger/lazyledger-core/crypto/encoding/amino"
)

var cdc *amino.Codec = amino.NewCodec()

func init() {
	cryptoamino.RegisterAmino(cdc)
	RegisterPacket(cdc)
}
