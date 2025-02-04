package types

import (
	"errors"
	"fmt"

	"github.com/tendermint/tendermint/crypto/merkle"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/proto/tendermint/crypto"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

// RowProof is a Merkle proof that a set of rows exist in a Merkle tree with a
// given data root.
type RowProof struct {
	// RowRoots are the roots of the rows being proven.
	RowRoots []tmbytes.HexBytes `json:"row_roots"`
	// Proofs is a list of Merkle proofs where each proof proves that a row
	// exists in a Merkle tree with a given data root.
	Proofs []*merkle.Proof `json:"proofs"`
	// StartRow the index of the start row.
	// Note: currently, StartRow is not validated as part of the proof verification.
	// If this field is used downstream, Validate(root) should be called along with
	// extra validation depending on how it's used.
	StartRow uint32 `json:"start_row"`
	// EndRow the index of the end row.
	// Note: currently, EndRow is not validated as part of the proof verification.
	// If this field is used downstream, Validate(root) should be called along with
	// extra validation depending on how it's used.
	EndRow uint32 `json:"end_row"`
}

// Validate performs checks on the fields of this RowProof. Returns an error if
// the proof fails validation. If the proof passes validation, this function
// attempts to verify the proof. It returns nil if the proof is valid.
func (rp RowProof) Validate(root []byte) error {
	if rp.EndRow < rp.StartRow {
		return fmt.Errorf("end row %d cannot be less than start row %d", rp.EndRow, rp.StartRow)
	}
	if int(rp.EndRow-rp.StartRow+1) != len(rp.RowRoots) {
		return fmt.Errorf("the number of rows %d must equal the number of row roots %d", int(rp.EndRow-rp.StartRow+1), len(rp.RowRoots))
	}
	if len(rp.Proofs) != len(rp.RowRoots) {
		return fmt.Errorf("the number of proofs %d must equal the number of row roots %d", len(rp.Proofs), len(rp.RowRoots))
	}
	if !rp.VerifyProof(root) {
		return errors.New("row proof failed to verify")
	}

	return nil
}

// VerifyProof verifies that all the row roots in this RowProof exist in a
// Merkle tree with the given root. Returns true if all proofs are valid.
func (rp RowProof) VerifyProof(root []byte) bool {
	for i, proof := range rp.Proofs {
		err := proof.Verify(root, rp.RowRoots[i])
		if err != nil {
			return false
		}
	}
	return true
}

func RowProofFromProto(p *tmproto.RowProof) RowProof {
	if p == nil {
		return RowProof{}
	}
	rowRoots := make([]tmbytes.HexBytes, len(p.RowRoots))
	rowProofs := make([]*merkle.Proof, len(p.Proofs))
	for i := range p.Proofs {
		rowRoots[i] = p.RowRoots[i]
		rowProofs[i] = &merkle.Proof{
			Total:    p.Proofs[i].Total,
			Index:    p.Proofs[i].Index,
			LeafHash: p.Proofs[i].LeafHash,
			Aunts:    p.Proofs[i].Aunts,
		}
	}

	return RowProof{
		RowRoots: rowRoots,
		Proofs:   rowProofs,
		StartRow: p.StartRow,
		EndRow:   p.EndRow,
	}
}

// ToProto converts RowProof to its protobuf representation.
// It converts all the fields to their protobuf counterparts and returns a new RowProof message.
func (rp RowProof) ToProto() *tmproto.RowProof {
	if len(rp.RowRoots) == 0 && len(rp.Proofs) == 0 {
		return &tmproto.RowProof{
			StartRow: rp.StartRow,
			EndRow:   rp.EndRow,
		}
	}

	rowRoots := make([][]byte, len(rp.RowRoots))
	proofs := make([]*crypto.Proof, len(rp.Proofs))
	for i := range rp.Proofs {
		rowRoots[i] = rp.RowRoots[i].Bytes()
		proofs[i] = rp.Proofs[i].ToProto()
	}

	return &tmproto.RowProof{
		RowRoots: rowRoots,
		Proofs:   proofs,
		StartRow: rp.StartRow,
		EndRow:   rp.EndRow,
	}
}
