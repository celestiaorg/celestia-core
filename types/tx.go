package types

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/celestiaorg/nmt"
	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/crypto/tmhash"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/pkg/consts"
	"github.com/tendermint/tendermint/proto/tendermint/crypto"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

// TxKeySize is the size of the transaction key index
const TxKeySize = sha256.Size

type (
	// Tx is an arbitrary byte array.
	// NOTE: Tx has no types at this level, so when wire encoded it's just length-prefixed.
	// Might we want types here ?
	Tx []byte

	// TxKey is the fixed length array key used as an index.
	TxKey [TxKeySize]byte
)

// Hash computes the TMHASH hash of the wire encoded transaction. It attempts to
// unwrap the transaction if it is a IndexWrapper or a BlobTx.
func (tx Tx) Hash() []byte {
	if indexWrapper, isIndexWrapper := UnmarshalIndexWrapper(tx); isIndexWrapper {
		return tmhash.Sum(indexWrapper.Tx)
	}
	if blobTx, isBlobTx := UnmarshalBlobTx(tx); isBlobTx {
		return tmhash.Sum(blobTx.Tx)
	}
	return tmhash.Sum(tx)
}

// Key returns the sha256 hash of the wire encoded transaction. It attempts to
// unwrap the transaction if it is a BlobTx or a IndexWrapper.
func (tx Tx) Key() TxKey {
	if blobTx, isBlobTx := UnmarshalBlobTx(tx); isBlobTx {
		return sha256.Sum256(blobTx.Tx)
	}
	if indexWrapper, isIndexWrapper := UnmarshalIndexWrapper(tx); isIndexWrapper {
		return sha256.Sum256(indexWrapper.Tx)
	}
	return sha256.Sum256(tx)
}

// String returns the hex-encoded transaction as a string.
func (tx Tx) String() string {
	return fmt.Sprintf("Tx{%X}", []byte(tx))
}

func (key TxKey) String() string {
	return fmt.Sprintf("TxKey{%X}", key[:])
}

// Txs is a slice of Tx.
type Txs []Tx

// Hash returns the Merkle root hash of the transaction hashes.
// i.e. the leaves of the tree are the hashes of the txs.
func (txs Txs) Hash() []byte {
	// These allocations will be removed once Txs is switched to [][]byte,
	// ref #2603. This is because golang does not allow type casting slices without unsafe
	txBzs := make([][]byte, len(txs))
	for i := 0; i < len(txs); i++ {
		txBzs[i] = txs[i].Hash()
	}
	return merkle.HashFromByteSlices(txBzs)
}

// Index returns the index of this transaction in the list, or -1 if not found
func (txs Txs) Index(tx Tx) int {
	for i := range txs {
		if bytes.Equal(txs[i], tx) {
			return i
		}
	}
	return -1
}

// IndexByHash returns the index of this transaction hash in the list, or -1 if not found
func (txs Txs) IndexByHash(hash []byte) int {
	for i := range txs {
		if bytes.Equal(txs[i].Hash(), hash) {
			return i
		}
	}
	return -1
}

// ToSliceOfBytes converts a Txs to slice of byte slices.
//
// NOTE: This method should become obsolete once Txs is switched to [][]byte.
// ref: #2603 https://github.com/tendermint/tendermint/issues/2603
func (txs Txs) ToSliceOfBytes() [][]byte {
	txBzs := make([][]byte, len(txs))
	for i := 0; i < len(txs); i++ {
		txBzs[i] = txs[i]
	}
	return txBzs
}

// ToTxs converts a raw slice of byte slices into a Txs type.
func ToTxs(txs [][]byte) Txs {
	txBzs := make(Txs, len(txs))
	for i := 0; i < len(txs); i++ {
		txBzs[i] = txs[i]
	}
	return txBzs
}

// TxProof represents a Merkle proof of the presence of a transaction in the Merkle tree.
type TxProof struct {
	RowRoots []tmbytes.HexBytes  `json:"root_hash"`
	Data     [][]byte            `json:"data"`
	Proofs   []*tmproto.NMTProof `json:"proof"`
}

// RowsProof represents a Merkle proof for a set of rows to the data root.
type RowsProof struct {
	RowsRoots []tmbytes.HexBytes `json:"row_root"`
	Proofs    []*merkle.Proof    `json:"proof"`
	StartRow  uint32             `json:"starting_row"`
	EndRow    uint32             `json:"ending_row"`
}

// SharesProof represents an NMT proof for a set of shares to the data root.
type SharesProof struct {
	// Data are the raw shares that are being proven.
	Data [][]byte `json:"data"`
	// SharesProofs proofs to the shares. Can contain multiple proofs if the shares
	// span on multiple rows.
	SharesProofs []*tmproto.NMTProof `json:"proof"`
	// NamespaceID needs to be specified as it will be used when verifying proofs.
	// A wrong NamespaceID will result in an invalid proof.
	NamespaceID []byte `json:"namespace_id"`
	// RowsProof binary merkle proofs of the rows to the data root.
	RowsProof RowsProof `json:"rows_proof"`
}

// Validate verifies the proof. It returns nil if the RootHash matches the dataHash argument,
// and if the proof is internally consistent. Otherwise, it returns a sensible error.
func (tp TxProof) Validate() error {
	if len(tp.RowRoots) != len(tp.Proofs) || len(tp.Data) != len(tp.Proofs) {
		return errors.New(
			"invalid number of proofs, row roots, or data. they all must be the same to verify the proof",
		)
	}
	for _, proof := range tp.Proofs {
		if proof.Start < 0 {
			return errors.New("proof index cannot be negative")
		}
		if (proof.End - proof.Start) <= 0 {
			return errors.New("proof total must be positive")
		}
		valid := tp.VerifyProof()
		if !valid {
			return errors.New("proof is not internally consistent")
		}
	}

	return nil
}

func (tp TxProof) VerifyProof() bool {
	cursor := int32(0)
	for i, proof := range tp.Proofs {
		nmtProof := nmt.NewInclusionProof(
			int(proof.Start),
			int(proof.End),
			proof.Nodes,
			true,
		)
		sharesUsed := proof.End - proof.Start
		valid := nmtProof.VerifyInclusion(
			consts.NewBaseHashFunc(),
			consts.TxNamespaceID,
			tp.Data[cursor:sharesUsed+cursor],
			tp.RowRoots[i],
		)
		if !valid {
			return false
		}
		cursor += sharesUsed
	}
	return true
}

func (sp SharesProof) ToProto() tmproto.SharesProof {
	rowsRoots := make([][]byte, len(sp.RowsProof.RowsRoots))
	rowsProofs := make([]*crypto.Proof, len(sp.RowsProof.Proofs))
	for i := range sp.RowsProof.RowsRoots {
		rowsRoots[i] = sp.RowsProof.RowsRoots[i].Bytes()
		rowsProofs[i] = sp.RowsProof.Proofs[i].ToProto()
	}
	pbtp := tmproto.SharesProof{
		Data:        sp.Data,
		SharesProof: sp.SharesProofs,
		NamespaceId: sp.NamespaceID,
		RowsProof: &tmproto.RowsProof{
			RowsRoots: rowsRoots,
			Proofs:    rowsProofs,
			StartRow:  sp.RowsProof.StartRow,
			EndRow:    sp.RowsProof.EndRow,
		},
	}

	return pbtp
}

func TxProofFromProto(pb tmproto.TxProof) (TxProof, error) {
	rowRoots := make([]tmbytes.HexBytes, len(pb.RowRoots))
	for i, root := range pb.RowRoots {
		rowRoots[i] = root
	}
	pbtp := TxProof{
		RowRoots: rowRoots,
		Data:     pb.Data,
		Proofs:   pb.Proofs,
	}

	return pbtp, nil
}

// SharesProofFromProto creates the SharesProof from a proto message.
// Expects the proof to be pre-validated.
func SharesProofFromProto(pb tmproto.SharesProof) (SharesProof, error) {
	rowsRoots := make([]tmbytes.HexBytes, len(pb.RowsProof.RowsRoots))
	rowsProofs := make([]*merkle.Proof, len(pb.RowsProof.Proofs))
	for i := range pb.RowsProof.Proofs {
		rowsRoots[i] = pb.RowsProof.RowsRoots[i]
		rowsProofs[i] = &merkle.Proof{
			Total:    pb.RowsProof.Proofs[i].Total,
			Index:    pb.RowsProof.Proofs[i].Index,
			LeafHash: pb.RowsProof.Proofs[i].LeafHash,
			Aunts:    pb.RowsProof.Proofs[i].Aunts,
		}
	}

	return SharesProof{
		RowsProof: RowsProof{
			RowsRoots: rowsRoots,
			Proofs:    rowsProofs,
			StartRow:  pb.RowsProof.StartRow,
			EndRow:    pb.RowsProof.EndRow,
		},
		Data:         pb.Data,
		SharesProofs: pb.SharesProof,
		NamespaceID:  pb.NamespaceId,
	}, nil
}

// ComputeProtoSizeForTxs wraps the transactions in tmproto.Data{} and calculates the size.
// https://developers.google.com/protocol-buffers/docs/encoding
func ComputeProtoSizeForTxs(txs []Tx) int64 {
	data := Data{Txs: txs}
	pdData := data.ToProto()
	return int64(pdData.Size())
}

// UnmarshalIndexWrapper attempts to unmarshal the provided transaction into an
// IndexWrapper transaction. It returns true if the provided transaction is an
// IndexWrapper transaction. An IndexWrapper transaction is a transaction that contains
// a MsgPayForBlob that has been wrapped with a share index.
//
// NOTE: protobuf sometimes does not throw an error if the transaction passed is
// not a tmproto.IndexWrapper, since the protobuf definition for MsgPayForBlob is
// kept in the app, we cannot perform further checks without creating an import
// cycle.
func UnmarshalIndexWrapper(tx Tx) (indexWrapper tmproto.IndexWrapper, isIndexWrapper bool) {
	// attempt to unmarshal into an IndexWrapper transaction
	err := proto.Unmarshal(tx, &indexWrapper)
	if err != nil {
		return indexWrapper, false
	}
	if indexWrapper.TypeId != consts.ProtoIndexWrapperTypeID {
		return indexWrapper, false
	}
	return indexWrapper, true
}

// MarshalIndexWrapper creates a wrapped Tx that includes the original transaction
// and the share index of the start of its blob.
//
// NOTE: must be unwrapped to be a viable sdk.Tx
func MarshalIndexWrapper(tx Tx, shareIndexes ...uint32) (Tx, error) {
	wTx := tmproto.IndexWrapper{
		Tx:           tx,
		ShareIndexes: shareIndexes,
		TypeId:       consts.ProtoIndexWrapperTypeID,
	}
	return proto.Marshal(&wTx)
}

// UnmarshalBlobTx attempts to unmarshal a transaction into blob transaction. If an
// error is thrown, false is returned.
func UnmarshalBlobTx(tx Tx) (bTx tmproto.BlobTx, isBlob bool) {
	err := bTx.Unmarshal(tx)
	if err != nil {
		return tmproto.BlobTx{}, false
	}
	// perform some quick basic checks to prevent false positives
	if bTx.TypeId != consts.ProtoBlobTxTypeID {
		return bTx, false
	}
	if len(bTx.Blobs) == 0 {
		return bTx, false
	}
	for _, b := range bTx.Blobs {
		if len(b.NamespaceId) != int(consts.TxNamespaceID.Size()) {
			return bTx, false
		}
	}
	return bTx, true
}

// MarshalBlobTx creates a BlobTx using a normal transaction and some number of
// blobs.
//
// NOTE: Any checks on the blobs or the transaction must be performed in the
// application
func MarshalBlobTx(tx []byte, blobs ...*tmproto.Blob) (Tx, error) {
	bTx := tmproto.BlobTx{
		Tx:     tx,
		Blobs:  blobs,
		TypeId: consts.ProtoBlobTxTypeID,
	}
	return bTx.Marshal()
}

// Validate runs basic validations on the proof then verifies if it is consistent.
// It returns nil if the proof is valid. Otherwise, it returns a sensible error.
// The `root` is  the block data root that the shares to be proven belong to.
// Note: these proofs are tested on the app side.
func (sp SharesProof) Validate(root []byte) error {
	numberOfSharesInProofs := int32(0)
	for _, proof := range sp.SharesProofs {
		// the range is not inclusive from the left.
		numberOfSharesInProofs += proof.End - proof.Start
	}

	if len(sp.RowsProof.RowsRoots) != len(sp.SharesProofs) ||
		int32(len(sp.Data)) != numberOfSharesInProofs {
		return errors.New(
			"invalid number of proofs, row roots, or data. they all must be the same to verify the proof",
		)
	}

	for _, proof := range sp.SharesProofs {
		if proof.Start < 0 {
			return errors.New("proof index cannot be negative")
		}
		if (proof.End - proof.Start) <= 0 {
			return errors.New("proof total must be positive")
		}
	}

	valid := sp.VerifyProof()
	if !valid {
		return errors.New("proof is not internally consistent")
	}

	if err := sp.RowsProof.Validate(root); err != nil {
		return err
	}

	return nil
}

func (sp SharesProof) VerifyProof() bool {
	cursor := int32(0)
	for i, proof := range sp.SharesProofs {
		nmtProof := nmt.NewInclusionProof(
			int(proof.Start),
			int(proof.End),
			proof.Nodes,
			true,
		)
		sharesUsed := proof.End - proof.Start
		valid := nmtProof.VerifyInclusion(
			consts.NewBaseHashFunc(),
			sp.NamespaceID,
			sp.Data[cursor:sharesUsed+cursor],
			sp.RowsProof.RowsRoots[i],
		)
		if !valid {
			return false
		}
		cursor += sharesUsed
	}
	return true
}

func (tp TxProof) IncludesTx(tx Tx) bool {
	return bytes.Contains(bytes.Join(tp.Data, []byte{}), tx)
}

func (tp TxProof) ToProto() tmproto.TxProof {
	rowRoots := make([][]byte, len(tp.RowRoots))
	for i, root := range tp.RowRoots {
		rowRoots[i] = root.Bytes()
	}
	pbtp := tmproto.TxProof{
		RowRoots: rowRoots,
		Data:     tp.Data,
		Proofs:   tp.Proofs,
	}

	return pbtp
}

// Validate verifies the proof. It returns nil if the proof is valid.
// Otherwise, it returns a sensible error.
func (rp RowsProof) Validate(root []byte) error {
	if int(rp.EndRow-rp.StartRow+1) != len(rp.RowsRoots) {
		return errors.New(
			"invalid number of row roots, or rows range. they all must be the same to verify the proof",
		)
	}
	if len(rp.Proofs) != len(rp.RowsRoots) {
		return errors.New(
			"invalid number of row roots, or proofs. they all must be the same to verify the proof",
		)
	}
	if !rp.VerifyProof(root) {
		return errors.New("proofs verification failed")
	}

	return nil
}

func (rp RowsProof) VerifyProof(root []byte) bool {
	for i, proof := range rp.Proofs {
		err := proof.Verify(root, rp.RowsRoots[i])
		if err != nil {
			return false
		}
	}
	return true
}
