package types

import (
	"errors"

	"github.com/tendermint/tendermint/crypto/merkle"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"
)

// RowProof is a Merkle proof that a set of rows exist in a Merkle tree with a
// given data root.
type RowProof struct {
	// RowRoots are the roots of the rows being proven.
	RowRoots []tmbytes.HexBytes `json:"row_roots"`
	// Proofs is a list of Merkle proofs where each proof proves that a row
	// exists in a Merkle tree with a given data root.
	Proofs   []*merkle.Proof `json:"proofs"`
	StartRow uint32          `json:"start_row"`
	EndRow   uint32          `json:"end_row"`
}

// Validate verifies the proof. It returns nil if the proof is valid.
// Otherwise, it returns a sensible error.
func (rp RowProof) Validate(root []byte) error {
	if int(rp.EndRow-rp.StartRow+1) != len(rp.RowRoots) {
		return errors.New(
			"invalid number of row roots, or rows range. they all must be the same to verify the proof",
		)
	}
	if len(rp.Proofs) != len(rp.RowRoots) {
		return errors.New(
			"invalid number of row roots, or proofs. they all must be the same to verify the proof",
		)
	}
	if !rp.VerifyProof(root) {
		return errors.New("proofs verification failed")
	}

	return nil
}

func (rp RowProof) VerifyProof(root []byte) bool {
	for i, proof := range rp.Proofs {
		err := proof.Verify(root, rp.RowRoots[i])
		if err != nil {
			return false
		}
	}
	return true
}
