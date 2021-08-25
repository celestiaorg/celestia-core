package fraudproofs

import (
	"errors"

	tmhash "github.com/celestiaorg/lazyledger-core/crypto/tmhash"
	nmt "github.com/celestiaorg/nmt"
	tmproto "github.com/proto/tendermint/types"
)

type BadEncodingFraudProof struct {
	// height of the block with the offending row or column
	Height int64
	// the available shares in the offending row or column and their Merkle proofs
	// array of ShareProofs
	ShareProofs []*ShareProof // TODO: remove pointer here
	// a Boolean indicating if it is an offending row or column; false if it is a row
	IsCol bool
	// the index of the offending row or column in the square
	Position uint64
}

func (befp *BadEncodingFraudProof) ToProto() (*tmproto.BadEncodingFraudProof, error) {
	// to get things to compile we must return types
	return nil, nil
}

func BadEncodingFraudProofFromProto(befpp *tmproto.BadEncodingFraudProof) (befp *BadEncodingFraudProof, err error) {
}

func (befp *BadEncodingFraudProof) ValidateBasic() error {
}

// Do the same thing for shareProof
// TODO: switch to the new local befp and DAH
func VerifyBadEncodingFraudProof(proof tmproto.BadEncodingFraudProof, dataAvailabilityHeader tmproto.DataAvailabilityHeader) (bool, error) {

	// parse the bad encoding fraud proof // TODO: we shouldn't need to parse if we use the local types for dah and befp
	isColForProof := proof.GetIsCol()
	height := proof.GetHeight()
	shareProofs := proof.GetShareProofs()
	position := proof.GetPosition()

	// check if shareProofs has correct size TODO: this check should be performed in validate basic
	if len(shareProofs) != AVAILABLE_DATA_ORIGINAL_SQUARE_MAX {
		return errors.New("Number of shares provided is incorrect.")
	}

	// get the row or column root challenged by the fraud proof within the DA header
	axisRoot := nil
	if isColForProof {
		columnRoots := dataAvailabilityHeader.GetColumnRoots()
		// position is uint64, thus always nonnegative
		if position < len(columnRoots) {
			axisRoot = columnRoots[position]
		} else {
			return errors.New("Position out of bounds in the badencodingfraudproof.")
		}
	} else {
		rowRoots := dataAvailabilityHeader.GetRowRoots()
		// position is uint64, thus always nonnegative
		if position < len(rowRoots) {
			axisRoot = rowRoots[position]
		} else {
			return errors.New("Position out of bounds in the badencodingfraudproof.")
		}
	}

	// new namespacedMerkleTree for calculating the new root
	namespacedMerkleTree := nmt.New(tmhash.New())

	for shareProof := range shareProofs {
		share := shareProof.GetShare() // TODO: we shouldn't need to parse is we use local types
		merkleProof := shareProof.GetProof()
		isCol := shareProof.GetIsCol()
		position := shareProof.GetPosition()

		namespaceID := share.GetNamespaceID()
		data := share.GetData()

		// verify that dataRoot commits to the share using the proof, isCol and position
		if !nmt.VerifyInclusion(tmhash.New(), namespaceID, data, axisRoot) {
			return errors.New("Root in the data availability header does not commit to the share.")
		}

		// extend the shares and push them to the new namespacedMerkleTree
		// TODO

		err := namespacedMerkleTree.push(share)
		if err != nil {
			return err
		}
	}

	// calculate the real axisRoot
	realAxisRoot := namespacedMerkleTree.Root().getByte()

	// compare the real axisRoot with the given axisRoot above
	if realAxisRoot == axisRoot {
		return false, errors.New("There is no bad encoding!")
	} else {
		return true // TODO: always return both types
	}
}

// TODO: use local types
func CreateBadEncodingFraudProof(block tmproto.Block) (tmproto.BadEncodingFraudProof, error) {

	//TODO
	// Is there a code to check each row or column for correct/incorrect encoding?
	// If an incorrect encoding is detected for a row or column,
	//	(i) set block height isCol and position accordingly,
	//	(ii) calculate NMT proofs for AVAILABLE_DATA_ORIGINAL_SQUARE_MAX of the shares, create shareProofs.
}
