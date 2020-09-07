package rpc

import (
	"github.com/lazyledger/lazyledger-core/crypto/merkle"
)

func defaultProofRuntime() *merkle.ProofRuntime {
	prt := merkle.NewProofRuntime()
	prt.RegisterOpDecoder(
		merkle.ProofOpValue,
		merkle.ValueOpDecoder,
	)
	return prt
}
