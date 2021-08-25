package fraudproofs

import (
	tmproto "github.com/proto/tendermint/types"
)

const (
	Bad_encoding               = iota
	Correct_block              = iota
	Incorrect_number_of_shares = iota
	Position_out_of_bound      = iota
	Non_committed_shares       = iota
)

func CreateBadEncodingFraudProofWithError(block tmproto.Block, err int) tmproto.BadEncodingFraudProof {

	proof := CreateBadEncodingFraudProof(block)

	switch err {
	case Bad_encoding:
		// BadEncodingFraudProof for a block with bad encoding

	case Correct_block:
		// BadEncodingFraudProof for a correct block

	case Incorrect_number_of_shares:
		// BadEncodingFraudProof with insufficient or too many shares
		proof.Get

	case Position_out_of_bound:
		// BadEncodingFraudProof with position out of bound

	case Non_committed_shares:
		// BadEncodingFraudProof with shares such that the calculate root does not commit to the shares

	default:
		return CreateBadEncodingFraudProof(block)
	}
}

func TestBadEncodingFraudProofWithError(proof tmproto.BadEncodingFraudProof) bool {

	// BadEncodingFraudProof for a block with bad encoding

	if err != nil {
		panic(err)
	}

	// BadEncodingFraudProof for a correct block

	if err != nil {
		panic(err)
	}

	// BadEncodingFraudProof with insufficient or too many shares

	if err != nil {
		panic(err)
	}

	// BadEncodingFraudProof with position out of bound

	if err != nil {
		panic(err)
	}

	// BadEncodingFraudProof with shares such that the calculate root does not commit to the shares

	if err != nil {
		panic(err)
	}

}
