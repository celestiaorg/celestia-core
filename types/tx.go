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

func TxKeyFromBytes(bytes []byte) (TxKey, error) {
	if len(bytes) != TxKeySize {
		return TxKey{}, fmt.Errorf("incorrect tx key size. Expected %d bytes, got %d", TxKeySize, len(bytes))
	}
	var key TxKey
	copy(key[:], bytes)
	return key, nil
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

func (tp *TxProof) VerifyProof() bool {
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

func (tp *TxProof) IncludesTx(tx Tx) bool {
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

func TxProofFromProto(pb tmproto.TxProof) (TxProof, error) {
	rowRoots := make([]tmbytes.HexBytes, len(pb.RowRoots))
	for i, root := range pb.RowRoots {
		rowRoots[i] = tmbytes.HexBytes(root)
	}
	pbtp := TxProof{
		RowRoots: rowRoots,
		Data:     pb.Data,
		Proofs:   pb.Proofs,
	}

	return pbtp, nil
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
func MarshalIndexWrapper(shareIndex uint32, tx Tx) (Tx, error) {
	wTx := tmproto.IndexWrapper{
		Tx:         tx,
		ShareIndex: shareIndex,
		TypeId:     consts.ProtoIndexWrapperTypeID,
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
