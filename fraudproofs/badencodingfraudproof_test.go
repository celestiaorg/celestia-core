package fraudproofs

import (
	tmproto "github.com/proto/tendermint/types"
)

const (
	bad_encoding               = iota
	correct_block              = iota
	incorrect_number_of_shares = iota
	position_out_of_bound      = iota
	non_committed_shares       = iota
)

func CreateBadEncodingFraudProofWithError(block tmproto.Block, err int) tmproto.BadEncodingFraudProof {

	proof := CreateBadEncodingFraudProof(block)

	switch err {
	case bad_encoding:
		// BadEncodingFraudProof for a block with bad encoding

	case correct_block:
		// BadEncodingFraudProof for a correct block

	case incorrect_number_of_shares:
		// BadEncodingFraudProof with insufficient or too many shares
		proof.Get

	case position_out_of_bound:
		// BadEncodingFraudProof with position out of bound

	case non_committed_shares:
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
