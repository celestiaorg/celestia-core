package fraudproofs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/celestiaorg/nmt/namespace"

	// "pkg/consts" // This is not defined.

	"github.com/celestiaorg/celestia-core/pkg/consts"
	"github.com/celestiaorg/celestia-core/pkg/wrapper"
	tmproto "github.com/celestiaorg/celestia-core/proto/tendermint/types"
	"github.com/celestiaorg/celestia-core/types"
	"github.com/celestiaorg/nmt"
	"github.com/celestiaorg/rsmt2d"
)

// type NamespaceMerkleTreeInclusionProof struct {
// 	// sibling hash values, ordered starting from the leaf's neighbor
// 	// array of 32-byte hashes
// 	SiblingValues [][]byte
// 	// sibling min namespace IDs
// 	// array of NAMESPACE_ID_BYTES-bytes
// 	SiblingMins [][]byte
// 	// sibling max namespace IDs
// 	// array of NAMESPACE_ID_BYTES-bytes
// 	SiblingMaxes [][]byte
// }

// func (nmtip *NamespaceMerkleTreeInclusionProof) ToProto() (*tmproto.NamespaceMerkleTreeInclusionProof, error) {
// 	if nmtip == nil {
// 		return nil, errors.New("NamespaceMerkleTreeInclusionProof is nil.")
// 	}
// 	nmtipp := new(tmproto.NamespaceMerkleTreeInclusionProof)
// 	nmtipp.SiblingValues = nmtip.SiblingValues
// 	nmtipp.SiblingMins = nmtip.SiblingMins
// 	nmtipp.SiblingMaxes = nmtip.SiblingMaxes
// 	return nmtipp, nil
// }

// // TODO(EVAN): stop using hack
// func ToProto(nmtip *nmt.NamespaceMerkleTreeInclusionProof) (*tmproto.NamespaceMerkleTreeInclusionProof, error) {
// 	if nmtip == nil {
// 		return nil, errors.New("NamespaceMerkleTreeInclusionProof is nil.")
// 	}
// 	nmtipp := new(tmproto.NamespaceMerkleTreeInclusionProof)
// 	nmtipp.SiblingValues = nmtip.SiblingValues
// 	nmtipp.SiblingMins = nmtip.SiblingMins
// 	nmtipp.SiblingMaxes = nmtip.SiblingMaxes
// 	return nmtipp, nil
// }

// func NamespaceMerkleTreeInclusionProofFromProto(nmtipp *tmproto.NamespaceMerkleTreeInclusionProof) (*nmt.NamespaceMerkleTreeInclusionProof, error) {
// 	if nmtipp == nil {
// 		return nil, errors.New("NamespaceMerkleTreeInclusionProof from proto is nil.")
// 	}
// 	nmtip := new(nmt.NamespaceMerkleTreeInclusionProof)
// 	nmtip.SiblingValues = nmtipp.SiblingValues
// 	nmtip.SiblingMins = nmtipp.SiblingMins
// 	nmtip.SiblingMaxes = nmtipp.SiblingMaxes
// 	return nmtip, nmtip.ValidateBasic()
// }

// func (nmtip *NamespaceMerkleTreeInclusionProof) ValidateBasic() error {
// 	// check if number of values and min/max namespaced provided by the proof match in numbers
// 	if len(nmtip.SiblingValues) != len(nmtip.SiblingMins) || len(nmtip.SiblingValues) != len(nmtip.SiblingMaxes) {
// 		return errors.New("Numbers of SiblingValues, SiblingMins and SiblingMaxes do not match.")
// 	}
// 	// check if the hash values have the correct byte size
// 	for _, siblingValue := range nmtip.SiblingValues {
// 		if len(siblingValue) != tmhash.Size {
// 			return errors.New("Number of hash bytes is incorrect.")
// 		}
// 	}
// 	// check if the namespaceIDs have the correct sizes
// 	for _, siblingMin := range nmtip.SiblingMins {
// 		if len(siblingMin) != consts.NamespaceSize {
// 			return errors.New("Number of namespace bytes is incorrect.")
// 		}
// 	}
// 	for _, siblingMax := range nmtip.SiblingMaxes {
// 		if len(siblingMax) != consts.NamespaceSize {
// 			return errors.New("Number of namespace bytes is incorrect.")
// 		}
// 	}
// 	return nil
// }

type Share struct {
	// namespace ID of the share
	// NAMESPACE_ID_BYTES-bytes
	NamespaceID []byte
	// raw share data
	// SHARE_SIZE-bytes
	RawData []byte
}

func (share *Share) ToProto() (*tmproto.Share, error) {
	if share == nil {
		return nil, errors.New("Share is nil.")
	}
	sharep := new(tmproto.Share)
	sharep.NamespaceID = share.NamespaceID
	sharep.RawData = share.RawData
	return sharep, nil
}

func ShareFromProto(sharep *tmproto.Share) (Share, error) {
	if sharep == nil {
		return Share{}, errors.New("Share from proto is nil.")
	}

	share := Share{NamespaceID: sharep.NamespaceID, RawData: sharep.RawData}
	return share, share.ValidateBasic()
}

func (share *Share) ValidateBasic() error {
	// check if the namespaceID has correct size
	if len(share.NamespaceID) != consts.NamespaceSize {
		return errors.New("Number of namespace bytes is incorrect.")
	}
	// check if the share has correct size
	if len(share.RawData) != consts.ShareSize {
		return errors.New("Number of share bytes is incorrect.")
	}
	if bytes.Compare(share.RawData[0:consts.NamespaceSize-1], share.NamespaceID) != 0 {
		return errors.New("Structure of the raw data is incorrect.")
	}
	return nil
}

type ShareProof struct {
	// the share
	Share Share
	// the Merkle proof of the share in the offending row or column root
	Proof nmt.Proof
	// a Boolean indicating if the Merkle proof is from a row root or column root; false if it is a row root
	IsCol bool
	// the index of the share in the offending row or column
	Position uint64
}

func (sp *ShareProof) ToProto() (*tmproto.ShareProof, error) {
	if sp == nil {
		return nil, errors.New("ShareProof is nil.")
	}
	pshare, err := sp.Share.ToProto()
	if err != nil {
		return nil, err
	}
	pproof := NmtInclusionProofToProto(sp.Proof)
	spp := new(tmproto.ShareProof)
	spp.Share = pshare
	spp.Proof = &pproof
	spp.IsCol = sp.IsCol
	spp.Position = sp.Position
	return spp, nil
}

func ShareProofFromProto(spp *tmproto.ShareProof) (*ShareProof, error) {
	if spp == nil {
		return nil, errors.New("ShareProof from proto is nil.")
	}
	share, err := ShareFromProto(spp.Share)
	if err != nil {
		return nil, err
	}
	proof := NmtInclusionProofFromProto(*spp.Proof)

	sp := new(ShareProof)
	sp.Share = share
	sp.Proof = proof
	sp.IsCol = spp.IsCol
	sp.Position = spp.Position
	return sp, sp.ValidateBasic()
}

func (sp *ShareProof) ValidateBasic() error {
	if err := sp.Share.ValidateBasic(); err != nil {
		return err
	}
	// if the position is within  2*MaxSquareSize
	if sp.Position > 2*consts.MaxSquareSize {
		return errors.New("Position is out of bound.")
	}
	return nil
}

type BadEncodingFraudProof struct {
	// height of the block with the offending row or column
	Height int64
	// the available shares in the offending row or column and their Merkle proofs
	// array of ShareProofs
	ShareProofs []*ShareProof
	// a Boolean indicating if it is an offending row or column; false if it is a row
	IsCol bool
	// the index of the offending row or column in the square
	Position uint64
}

// func (befp *BadEncodingFraudProof) ToProto() (*tmproto.BadEncodingFraudProof, error) {
// 	if befp == nil {
// 		return nil, errors.New("BadEncodingFraudProof is nil.")
// 	}
// 	shareProofsProto := make([]*tmproto.ShareProof, len(befp.ShareProofs))
// 	for i, shareProof := range befp.ShareProofs {
// 		shareProofProto, err := shareProof.ToProto()
// 		if err != nil {
// 			return nil, err
// 		}
// 		shareProofsProto[i] = shareProofProto
// 	}

// 	befpp := new(tmproto.BadEncodingFraudProof)
// 	befpp.Height = befp.Height
// 	befpp.ShareProofs = shareProofsProto
// 	befpp.IsCol = befp.IsCol
// 	befpp.Position = befp.Position
// 	return befpp, nil
// }

// func BadEncodingFraudProofFromProto(befpp *tmproto.BadEncodingFraudProof) (*BadEncodingFraudProof, error) {
// 	if befpp == nil {
// 		return nil, errors.New("BadEncodingFraudProof from proto is nil.")
// 	}

// 	shareProofs := make([]*ShareProof, len(befpp.ShareProofs))
// 	for i, shareProofProto := range befpp.ShareProofs {
// 		shareProof, err := ShareProofFromProto(shareProofProto)
// 		if err != nil {
// 			return nil, err
// 		}
// 		shareProofs[i] = shareProof
// 	}

// 	befp := new(BadEncodingFraudProof)
// 	befp.Height = befpp.Height
// 	befp.ShareProofs = shareProofs
// 	befp.IsCol = befpp.IsCol
// 	befp.Position = befpp.Position
// 	return befp, nil
// }

func (befp *BadEncodingFraudProof) ValidateBasic() error {
	// block height cannot be a negative number
	if befp.Height < 0 {
		return errors.New("Block height cannot be a negative number.")
	}
	// Number of shares provided is incorrect, i.e is not 2*MaxSquareSize
	if len(befp.ShareProofs) != consts.MaxSquareSize {
		return errors.New("Number of shares provided is incorrect.")
	}
	for _, shareProof := range befp.ShareProofs {
		if err := shareProof.ValidateBasic(); err != nil {
			return err
		}
	}
	const maxPosition = 2*consts.MaxSquareSize - 1
	// check if the position is within  2*MaxSquareSize
	if befp.Position > maxPosition {
		return errors.New("Position is out of bound.")
	}
	return nil
}

// Functionality to obtain DataAvailabilityHeader from block height has to be implemented
func VerifyBadEncodingFraudProof(befp BadEncodingFraudProof, dah *types.DataAvailabilityHeader) (bool, error) {
	// get the row or column root challenged by the fraud proof within the DA header
	axisRoot := dah.ColumnRoots[0]
	if befp.IsCol {
		// position is uint64, thus always nonnegative
		if int(befp.Position) >= len(dah.ColumnRoots) {
			return false, errors.New("Position out of bounds in the badencodingfraudproof.")
		}
		axisRoot = dah.ColumnRoots[befp.Position]
	} else {
		// position is uint64, thus always nonnegative
		if int(befp.Position) >= len(dah.RowsRoots) {
			return false, errors.New("Position out of bounds in the badencodingfraudproof.")
		}
		axisRoot = dah.RowsRoots[befp.Position]
	}

	// For debugging; delete later
	// -------------------------
	fmt.Println("axisRoot should match the axisRoot corresponding to the corrupted row: ", axisRoot)
	// -------------------------

	// new namespacedMerkleTree for calculating the new root
	rawShares := make([][]byte, len(befp.ShareProofs))
	for i, shareProof := range befp.ShareProofs {
		// verify that dataRoot commits to the share using the proof, isCol and position
		valid := shareProof.Proof.VerifyInclusion(sha256.New(), shareProof.Share.NamespaceID, shareProof.Share.RawData, axisRoot)
		if !valid {
			// For debugging; delete later
			// -------------------------
			fmt.Println("Failed index: ", i)
			// -------------------------
			return false, errors.New("share does not belong in the data square")
		}
		// extract raw data
		rawShares[i] = shareProof.Share.RawData
	}

	// extend the shares to create the real axis root
	codec := consts.DefaultCodec()
	erasureShares, err := codec.Encode(rawShares)
	if err != nil {
		return false, err
	}

	// create a tree to generate the real axis root
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(len(erasureShares)))
	for i, share := range erasureShares {
		tree.Push(share, rsmt2d.SquareIndex{Axis: 0, Cell: uint(i)})
	}

	// calculate the real axisRoot
	root := tree.Root()
	realAxisRoot := namespace.IntervalDigest{
		Min:    root[0:consts.NamespaceSize],
		Max:    root[consts.NamespaceSize : consts.NamespaceSize*2],
		Digest: root[consts.NamespaceSize*2:],
	}

	// For debugging; delete later
	// -------------------------
	fmt.Println("realAxisRoot should match the axisRoot corresponding to the uncorrupted row: ", realAxisRoot)
	// -------------------------

	// compare the real axisRoot with the given axisRoot above
	if bytes.Compare(realAxisRoot.Digest, axisRoot.Digest) == 0 {
		return false, errors.New("There is no bad encoding!")
	}

	return true, nil
}

// squareSize is original square size
func CreateBadEncodingFraudProof(height int64, squareSize, position uint64, shares [][]byte, isCol bool) (BadEncodingFraudProof, error) {
	newTree := wrapper.NewErasuredNamespacedMerkleTree(squareSize * 2)
	for j, share := range shares {
		newTree.Push(share, rsmt2d.SquareIndex{Axis: uint(position), Cell: uint(j)})
	}

	// For debugging; delete later
	// -------------------------
	root := newTree.Root()
	rootDigest := namespace.IntervalDigest{
		Min:    root[0:consts.NamespaceSize],
		Max:    root[consts.NamespaceSize : consts.NamespaceSize*2],
		Digest: root[consts.NamespaceSize*2:],
	}
	fmt.Println("This should be the same as axisRoot corresponding to the corrupted shares: ", rootDigest)
	// --------------------------

	// create bad encoding fraud proof
	shareProofs := make([]*ShareProof, squareSize)
	for j, rowElement := range shares {
		if j < int(squareSize) {
			share := Share{
				NamespaceID: rowElement[:consts.NamespaceSize],
				RawData:     rowElement[consts.NamespaceSize:],
			}
			merkleProof, err := newTree.CreateInclusionProof(j)
			if err != nil {
				return BadEncodingFraudProof{}, err
			}
			// there's no way to generate a proof while also adding the namespace to the data
			shareProof := ShareProof{
				Share:    share,
				Proof:    merkleProof,
				IsCol:    false,
				Position: uint64(position),
			}
			shareProofs[j] = &shareProof
		}
	}
	proof := BadEncodingFraudProof{
		Height:      height,
		ShareProofs: shareProofs,
		IsCol:       isCol,
		Position:    uint64(position),
	}
	return proof, nil
}

// TODO(EVAN): complete once the DAH refactor PR is merged.
// Note: this function will only be called by celestia-nodes, as a block with bad encoding should be rejected.
// TODO(evan): split this functionality into two distinct fucntions
func CheckAndCreateBadEncodingFraudProof(block *types.Block, dah *types.DataAvailabilityHeader) (BadEncodingFraudProof, error) {
	namespacedShares, _ := block.Data.ComputeShares() // revert this later
	shares := namespacedShares.RawShares()

	// extend the original data
	origSquareSize := len(dah.ColumnRoots) / 2
	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(origSquareSize))
	extendedDataSquare, err := rsmt2d.ComputeExtendedDataSquare(shares, rsmt2d.NewRSGF8Codec(), tree.Constructor)
	if err != nil {
		return BadEncodingFraudProof{}, err
	}

	// generate the row and col roots of the extended data square
	rowRoots := extendedDataSquare.RowRoots()
	// colRoots := extendedDataSquare.ColRoots()

	// find the first difference between the data availability headers
	originalRowRoots := dah.RowsRoots
	for i, rowRoot := range rowRoots {
		// first difference at row i
		if bytes.Compare(rowRoot, originalRowRoots[i].Bytes()) != 0 {
			// create another nmt tree so that we can access inner nodes
			newTree := wrapper.NewErasuredNamespacedMerkleTree(uint64(origSquareSize))
			for j, share := range extendedDataSquare.Row(uint(i)) {
				newTree.Push(share, rsmt2d.SquareIndex{Axis: uint(i), Cell: uint(j)})
			}
			// create bad encoding fraud proof
			shareProofs := make([]*ShareProof, origSquareSize)
			for j, rowElement := range extendedDataSquare.Row(uint(i))[0:origSquareSize] {
				share := Share{
					NamespaceID: rowElement[:consts.NamespaceSize],
					RawData:     rowElement[consts.NamespaceSize:],
				}
				merkleProof, err := newTree.CreateInclusionProof(j)
				if err != nil {
					return BadEncodingFraudProof{}, err
				}
				// there's no way to generate a proof while also adding the namespace to the data
				shareProof := ShareProof{
					Share:    share,
					Proof:    merkleProof,
					IsCol:    false,
					Position: uint64(i),
				}
				shareProofs[j] = &shareProof
			}
			proof := BadEncodingFraudProof{
				Height:      block.Height,
				ShareProofs: shareProofs,
				IsCol:       false,
				Position:    uint64(i),
			}
			return proof, nil
		}
	}
	return BadEncodingFraudProof{}, errors.New("There is no bad encoding.")
}

/*
In order to make code useful, I think that we are going to have to write some other code to help

mainly, have a way to essentially generate a row of erasure data and namespace it properly using the plugin
 - this will allow us to only create a fraud proof instead of checking for the entire thing

Use the normal inclusion proofs instead of the ones in the spec

don't make the protobuf version of the nmt.Proof

*/
