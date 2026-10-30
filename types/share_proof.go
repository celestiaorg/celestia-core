package types

import (
	"errors"
	"fmt"
	"math"

	"github.com/celestiaorg/go-square/v4/share"
	"github.com/celestiaorg/nmt"

	"github.com/cometbft/cometbft/libs/consts"
	crypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// ShareProof is an NMT proof that a set of shares exist in a set of rows and a
// Merkle proof that those rows exist in a Merkle tree with a given data root.
type ShareProof struct {
	// Data are the raw shares that are being proven.
	Data [][]byte `json:"data"`
	// ShareProofs are NMT proofs that the shares in Data exist in a set of
	// rows. There will be one ShareProof per row that the shares occupy.
	ShareProofs []*tmproto.NMTProof `json:"share_proofs"`
	// NamespaceID is the namespace id of the shares being proven. This
	// namespace id is used when verifying the proof. If the namespace id doesn't
	// match the namespace of the shares, the proof will fail verification.
	NamespaceID      []byte   `json:"namespace_id"`
	RowProof         RowProof `json:"row_proof"`
	NamespaceVersion uint32   `json:"namespace_version"`
}

func (sp ShareProof) ToProto() tmproto.ShareProof {
	// TODO consider extracting a ToProto function for RowProof
	rowRoots := make([][]byte, len(sp.RowProof.RowRoots))
	rowProofs := make([]*crypto.Proof, len(sp.RowProof.Proofs))
	for i := range sp.RowProof.RowRoots {
		rowRoots[i] = sp.RowProof.RowRoots[i].Bytes()
		rowProofs[i] = sp.RowProof.Proofs[i].ToProto()
	}
	pbtp := tmproto.ShareProof{
		Data:        sp.Data,
		ShareProofs: sp.ShareProofs,
		NamespaceId: sp.NamespaceID,
		RowProof: &tmproto.RowProof{
			RowRoots: rowRoots,
			Proofs:   rowProofs,
			StartRow: sp.RowProof.StartRow,
			EndRow:   sp.RowProof.EndRow,
		},
		NamespaceVersion: sp.NamespaceVersion,
	}

	return pbtp
}

// ShareProofFromProto creates a ShareProof from a proto message.
// Expects the proof to be pre-validated.
func ShareProofFromProto(pb tmproto.ShareProof) (ShareProof, error) {
	return ShareProof{
		RowProof:         RowProofFromProto(pb.RowProof),
		Data:             pb.Data,
		ShareProofs:      pb.ShareProofs,
		NamespaceID:      pb.NamespaceId,
		NamespaceVersion: pb.NamespaceVersion,
	}, nil
}

// Validate runs basic validations on the proof then verifies if it is consistent.
// It returns nil if the proof is valid. Otherwise, it returns a sensible error.
// The `root` is the block data root that the shares to be proven belong to.
// Note: these proofs are tested on the app side.
func (sp ShareProof) Validate(root []byte) error {
	if len(sp.NamespaceID) != share.NamespaceIDSize {
		return fmt.Errorf("namespace ID must be %d bytes, got %d", share.NamespaceIDSize, len(sp.NamespaceID))
	}
	// Accumulate in int64: the ranges are attacker-supplied int32 values, so
	// summing them in int32 can overflow and bypass the length check below.
	numberOfSharesInProofs := int64(0)
	for _, proof := range sp.ShareProofs {
		if proof == nil {
			return errors.New("nil share proof")
		}
		if proof.Start < 0 {
			return errors.New("proof index cannot be negative")
		}
		if proof.End <= proof.Start {
			return errors.New("proof total must be positive")
		}
		// the range is not inclusive from the left.
		numberOfSharesInProofs += int64(proof.End) - int64(proof.Start)
	}

	if len(sp.ShareProofs) != len(sp.RowProof.RowRoots) {
		return fmt.Errorf("the number of share proofs %d must equal the number of row roots %d", len(sp.ShareProofs), len(sp.RowProof.RowRoots))

	}
	if int64(len(sp.Data)) != numberOfSharesInProofs {
		return fmt.Errorf("the number of shares %d must equal the number of shares in share proofs %d", len(sp.Data), numberOfSharesInProofs)
	}

	if err := sp.RowProof.Validate(root); err != nil {
		return err
	}

	// The data root commits to 2k row roots and 2k column roots for a k-wide
	// original square. Shares must form one contiguous range inside it.
	width := sp.RowProof.Proofs[0].Total / 4
	last := len(sp.ShareProofs) - 1
	for i, proof := range sp.ShareProofs {
		if sp.RowProof.Proofs[i].Index >= width {
			return fmt.Errorf("row %d is outside the original data square of width %d", sp.RowProof.Proofs[i].Index, width)
		}
		if int64(proof.End) > width {
			return fmt.Errorf("share proof %d ends at %d past the square width %d", i, proof.End, width)
		}
		if i > 0 && proof.Start != 0 {
			return fmt.Errorf("share proof %d must start at the beginning of its row", i)
		}
		if i < last && int64(proof.End) != width {
			return fmt.Errorf("share proof %d must end at the end of its row", i)
		}
	}

	if ok := sp.VerifyProof(); !ok {
		return errors.New("share proof failed to verify")
	}

	return nil
}

// VerifyProof checks only the NMT proofs of the shares against the row roots.
// It does not check share positions or the row proof. Use Validate for that.
func (sp ShareProof) VerifyProof() bool {
	if sp.NamespaceVersion > math.MaxUint8 || len(sp.NamespaceID) != share.NamespaceIDSize {
		return false
	}
	if len(sp.RowProof.RowRoots) != len(sp.ShareProofs) {
		return false
	}
	cursor := int64(0)
	for i, proof := range sp.ShareProofs {
		if proof == nil || proof.Start < 0 || proof.End <= proof.Start {
			return false
		}
		sharesUsed := int64(proof.End) - int64(proof.Start)
		if cursor+sharesUsed > int64(len(sp.Data)) {
			return false
		}
		nmtProof := nmt.NewInclusionProof(
			int(proof.Start),
			int(proof.End),
			proof.Nodes,
			true,
		)
		// Consider extracting celestia-app's namespace package. We can't use it
		// here because that would introduce a circulcar import.
		//nolint:gosec
		namespace := append([]byte{uint8(sp.NamespaceVersion)}, sp.NamespaceID...)
		valid := nmtProof.VerifyInclusion(
			consts.NewBaseHashFunc(),
			namespace,
			sp.Data[cursor:sharesUsed+cursor],
			sp.RowProof.RowRoots[i],
		)
		if !valid {
			return false
		}
		cursor += sharesUsed
	}
	return true
}
