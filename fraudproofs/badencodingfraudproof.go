package fraudproofs

import (
	"bytes"
	"errors"
	"types/consts"

	tmhash "github.com/celestiaorg/lazyledger-core/crypto/tmhash"
	tmproto "github.com/proto/tendermint/types"
)

// Should check types inside ValidateBasic?
// Line 301 and on... Verify function for a single share and its proof is undefined.
// How do we denote the error type?

type DataAvailabilityHeader struct {
	// RowRoot_j = root((M_{j,1} || M_{j,2} || ... || M_{j,2k} ))
	RowRoots [][]byte
	// ColumnRoot_j = root((M_{1,j} || M_{2,j} || ... || M_{2k,j} ))
	ColumnRoots [][]byte
}

func (dah *DataAvailabilityHeader) ToProto() (*tmproto.DataAvailabilityHeader, error) {
	if dah == nil {
		return nil, errors.New("DataAvailabilityHeader is nil.")
	}
	dahp := new(tmproto.DataAvailabilityHeader)
	dahp.RowRoots = dah.RowRoots
	dahp.ColumnRoots = dah.ColumnRoots
	return dahp, nil
}

func DataAvailabilityHeaderFromProto(dahp *tmproto.DataAvailabilityHeader) (*DataAvailabilityHeader, error) {
	if dahp == nil {
		return nil, errors.New("DataAvailabilityHeader from proto is nil.")
	}
	dah := new(DataAvailabilityHeader)
	dah.RowRoots = dahp.RowRoots
	dah.ColumnRoots = dahp.ColumnRoots
	return dah, dah.ValidateBasic()
}

func (dah *DataAvailabilityHeader) ValidateBasic() error {
	// check if the number of row roots is positive
	if len(dah.RowRoots) <= 0 {
		return errors.New("Non positive number of row roots.")
	}
	// check if the number of column roots is positive
	if len(dah.ColumnRoots) <= 0 {
		return errors.New("Non positive number of column roots.")
	}
	// check if the row roots and column roots have correct byte size
	for _, rowRoot := range dah.RowRoots {
		if len(rowRoot) != tmhash.Size {
			return errors.New("Number of hash bytes is incorrect.")
		}
	}
	for _, columnRoot := range dah.ColumnRoots {
		if len(columnRoot) != tmhash.Size {
			return errors.New("Number of hash bytes is incorrect.")
		}
	}
	return nil
}

type NamespaceMerkleTreeInclusionProof struct {
	// sibling hash values, ordered starting from the leaf's neighbor
	// array of 32-byte hashes
	SiblingValues [][]byte
	// sibling min namespace IDs
	// array of NAMESPACE_ID_BYTES-bytes
	SiblingMins [][]byte
	// sibling max namespace IDs
	// array of NAMESPACE_ID_BYTES-bytes
	SiblingMaxes [][]byte
}

func (nmtip *NamespaceMerkleTreeInclusionProof) ToProto() (*tmproto.NamespaceMerkleTreeInclusionProof, error) {
	if nmtip == nil {
		return nil, errors.New("NamespaceMerkleTreeInclusionProof is nil.")
	}
	nmtipp := new(tmproto.NamespaceMerkleTreeInclusionProof)
	nmtipp.SiblingValues = nmtip.SiblingValues
	nmtipp.SiblingMins = nmtip.SiblingMins
	nmtipp.SiblingMaxes = nmtip.SiblingMaxes
	return nmtipp, nil
}

func NamespaceMerkleTreeInclusionProofFromProto(nmtipp *tmproto.NamespaceMerkleTreeInclusionProof) (*NamespaceMerkleTreeInclusionProof, error) {
	if nmtipp == nil {
		return nil, errors.New("NamespaceMerkleTreeInclusionProof from proto is nil.")
	}
	nmtip := new(NamespaceMerkleTreeInclusionProof)
	nmtip.SiblingValues = nmtipp.SiblingValues
	nmtip.SiblingMins = nmtipp.SiblingMins
	nmtip.SiblingMaxes = nmtipp.SiblingMaxes
	return nmtip, nmtip.ValidateBasic()
}

func (nmtip *NamespaceMerkleTreeInclusionProof) ValidateBasic() error {
	// check if number of values and min/max namespaced provided by the proof match in numbers
	if len(nmtip.SiblingValues) != len(nmtip.SiblingMins) || len(nmtip.SiblingValues) != len(nmtip.SiblingMaxes) {
		return errors.New("Numbers of SiblingValues, SiblingMins and SiblingMaxes do not match.")
	}
	// check if the hash values have the correct byte size
	for _, siblingValue := range nmtip.SiblingValues {
		if len(siblingValue) != tmhash.Size {
			return errors.New("Number of hash bytes is incorrect.")
		}
	}
	// check if the namespaceIDs have the correct sizes
	for _, siblingMin := range nmtip.SiblingMins {
		if len(siblingMin) != consts.NamespaceSize {
			return errors.New("Number of namespace bytes is incorrect.")
		}
	}
	for _, siblingMax := range nmtip.SiblingMaxes {
		if len(siblingMax) != consts.NamespaceSize {
			return errors.New("Number of namespace bytes is incorrect.")
		}
	}
	return nil
}

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

func ShareFromProto(sharep *tmproto.Share) (*Share, error) {
	if sharep == nil {
		return nil, errors.New("Share from proto is nil.")
	}
	share := new(Share)
	share.NamespaceID = sharep.NamespaceID
	share.RawData = sharep.RawData
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
	Share *Share
	// the Merkle proof of the share in the offending row or column root
	Proof *NamespaceMerkleTreeInclusionProof
	// a Boolean indicating if the Merkle proof is from a row root or column root; false if it is a row root
	IsCol bool
	// the index of the share in the offending row or column
	Position uint64
}

func (sp *ShareProof) ToProto() (*tmproto.ShareProof, error) {
	if sp == nil {
		return nil, errors.New("ShareProof is nil.")
	}
	spp := new(tmproto.ShareProof)
	spp.Share = sp.Share
	spp.Proof = sp.Proof
	spp.IsCol = sp.IsCol
	spp.Position = sp.Position
	return spp, nil
}

func ShareProofFromProto(spp *tmproto.ShareProof) (*ShareProof, error) {
	if spp == nil {
		return nil, errors.New("ShareProof from proto is nil.")
	}
	sp := new(ShareProof)
	sp.Share = spp.Share
	sp.Proof = spp.Proof
	sp.IsCol = spp.IsCol
	sp.Position = spp.Position
	return sp, sp.ValidateBasic()
}

func (sp *ShareProof) ValidateBasic() error {
	if err := sp.Share.ValidateBasic(); err != nil {
		return err
		// return errors.New("Error in share: %w", err)
	}
	if err := sp.Proof.ValidateBasic(); err != nil {
		return err
		// return errors.New("Error in proof: %w", err)
	}
	// check if the position is within  2*MaxSquareSize
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
	ShareProofs []*ShareProof // TODO: remove pointer here
	// a Boolean indicating if it is an offending row or column; false if it is a row
	IsCol bool
	// the index of the offending row or column in the square
	Position uint64
}

func (befp *BadEncodingFraudProof) ToProto() (*tmproto.BadEncodingFraudProof, error) {
	if befp == nil {
		return nil, errors.New("BadEncodingFraudProof is nil.")
	}
	befpp := new(tmproto.BadEncodingFraudProof)
	befpp.Height = befp.Height
	befpp.ShareProofs = befp.ShareProofs
	befpp.IsCol = befp.IsCol
	befpp.Position = befp.Position
	return befpp, nil
}

func BadEncodingFraudProofFromProto(befpp *tmproto.BadEncodingFraudProof) (*BadEncodingFraudProof, error) {
	if befpp == nil {
		return nil, errors.New("BadEncodingFraudProof from proto is nil.")
	}
	befp := new(BadEncodingFraudProof)
	befp.Height = befpp.Height
	befp.ShareProofs = befpp.ShareProofs
	befp.IsCol = befpp.IsCol
	befp.Position = befpp.Position
	return befpp, nil
}

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
	// check if the position is within  2*MaxSquareSize
	if befp > 2*consts.MaxSquareSize {
		return errors.New("Position is out of bound.")
	}
	return nil
}

// Functionality to obtain DataAvailabilityHeader from block height has to be implemented
func VerifyBadEncodingFraudProof(befp BadEncodingFraudProof, dah DataAvailabilityHeader) (bool, error) {

	// get the row or column root challenged by the fraud proof within the DA header
	axisRoot := dah.ColumnRoots[0]
	if befp.IsCol {
		// position is uint64, thus always nonnegative
		if int(befp.Position) < len(dah.ColumnRoots) {
			axisRoot = dah.ColumnRoots[befp.Position]
		} else {
			return false, errors.New("Position out of bounds in the badencodingfraudproof.")
		}
	} else {
		// position is uint64, thus always nonnegative
		if int(befp.Position) < len(dah.RowRoots) {
			axisRoot = dah.RowRoots[befp.Position]
		} else {
			return false, errors.New("Position out of bounds in the badencodingfraudproof.")
		}
	}

	// new namespacedMerkleTree for calculating the new root
	// namespacedMerkleTree := nmt.New(tmhash.New())

	// for _, shareProof := range befp.ShareProofs {

	// verify that dataRoot commits to the share using the proof, isCol and position
	// https://github.com/celestiaorg/nmt/blob/02cdbfdb328211a7e5d5eb2f42e15b72348265d8/proof.go#L207
	// TODO
	// if !shareProof.Proof.VerifyInclusion(tmhash.New(), shareProof.Share.NamespaceID, shareProof.Share.RawData, axisRoot) {
	//	return false, errors.New("Root in the data availability header does not commit to the share.")
	// }

	// extend the shares and push them to the new namespacedMerkleTree
	// https://github.com/celestiaorg/rsmt2d/blob/2aa7e42d2fda53c542b7a602d3023f675ba9051e/extendeddatacrossword.go#L278
	// TODO

	// err := namespacedMerkleTree.Push(shareProof.Share)
	// if err != nil {
	//	return false, err
	// }
	// }

	// calculate the real axisRoot
	// realAxisRoot := namespacedMerkleTree.Root().GetByte()
	realAxisRoot := axisRoot

	// compare the real axisRoot with the given axisRoot above
	if bytes.Compare(realAxisRoot, axisRoot) != 0 {
		return false, errors.New("There is no bad encoding!")
	} else {
		return true, nil
	}
	return true, nil
}

// TODO: use local types
func CreateBadEncodingFraudProof(block tmproto.Block) (tmproto.BadEncodingFraudProof, error) {

	//TODO
	// Is there a code to check each row or column for correct/incorrect encoding?
	// If an incorrect encoding is detected for a row or column,
	//	(i) set block height isCol and position accordingly,
	//	(ii) calculate NMT proofs for AVAILABLE_DATA_ORIGINAL_SQUARE_MAX of the shares, create shareProofs.
	return nil, nil
}
