package fraudproofs

import (
	"errors"
	"github.com/celestiaorg/celestia-core/state/txindex/null"
	tmproto "github.com/proto/tendermint/types"
	nmt "github.com/celestiaorg/nmt"
	tmhash "github.com/celestiaorg/lazyledger-core/crypto/tmhash"
	"github.com/celestiaorg/rsmt2d"
)


func VerifyBadEncodingFraudProof(proof tmproto.BadEncodingFraudProof) (bool, error) {

	// get the block
	//TODO
	
	// parse the bad encoding fraud proof
	isColForProof := proof.GetIsCol()
	height := proof.GetHeight()
	shareProofs := proof.GetShareProofs()
	position := proof.GetPosition()

	// check if shareProofs has correct size
	if len(shareProofs) != AVAILABLE_DATA_ORIGINAL_SQUARE_MAX {
		return errors.New("Number of shares provided is incorrect.")
	}

	// get the dataRoot within the header
	dataRoot := block.GetHeader().GetDataHash()

	// get the row or column root challenged by the fraud proof within the DA header
	dataAvailabilityHeader := block.GetDataAvailabilityHeader()
	axisRoot := nil
	if isColForProof {
		columnRoots := dataAvailabilityHeader.GetColumnRoots()
		if (position < len(columnRoots)) && (0 <= position) {
			axisRoot = columnRoots[position] 
		}
		else {
			return errors.New("Position out of bounds in the badencodingfraudproof.")
		}
	}
	else {
		rowRoots := dataAvailabilityHeader.GetRowRoots()
		if (position < len(rowRoots)) && (0 <= position) {
			axisRoot = rowRoots[position] 
		}
		else {
			return errors.New("Position out of bounds in the badencodingfraudproof.")
		}
	}

	// new namespacedMerkleTree for calculating the new root
	namespacedMerkleTree := nmt.New(tmhash.New())
	err := nil

	for shareProof:= range shareProofs {
		share := shareProof.GetShare()
		merkleProof := shareProof.GetProof()
		isCol := shareProof.GetIsCol()
		position := shareProof.GetPosition()

		namespaceID := share.GetNamespaceID()
		data := share.GetData()

		// verify that dataRoot commits to the share using the proof, isCol and position
		if !nmt.VerifyInclusion(tmhash.New(), namespaceID, data, axisRoot) {
			return errors.New("Root in the data availability header does not commit to the share.")
		}

		// push shares to the new namespacedMerkleTree

		// we need to ensure that data is extended before the loop ends.
		err = namespacedMerkleTree.push(share)
		if err != nil {
			return err
		}
	}

	// calculate the real axisRoot
	realAxisRoot := namespacedMerkleTree.Root().getByte()
 
	// compare the real axisRoot with the given axisRoot above
	if realAxisRoot == axisRoot {
		return errors.New("There is no bad encoding!")
	}
	else {
		return true
	}
}

func CreateBadEncodingFraudProof(block tmproto.Block) (tmproto.BadEncodingFraudProof, error) {

	//TODO
}
