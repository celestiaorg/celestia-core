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

// Hash computes the TMHASH hash of the wire encoded transaction.
func (tx Tx) Hash() []byte {
	return tmhash.Sum(tx)
}

func (tx Tx) Key() TxKey {
	return sha256.Sum256(tx)
}

// String returns the hex-encoded transaction as a string.
func (tx Tx) String() string {
	return fmt.Sprintf("Tx{%X}", []byte(tx))
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

// UnwrapMalleatedTx attempts to unmarshal the provided transaction into a
// malleated transaction wrapper, if this can be done, then it returns true. A
// malleated transaction is a normal transaction that has been wrapped with meta
// data.
//
// NOTE: protobuf sometimes does not throw an error if the transaction passed is
// not a tmproto.MalleatedTx, since the schema for PayForMessage is kept in the
// app, we cannot perform further checks without creating an import cycle.
func UnwrapMalleatedTx(tx Tx) (malleatedTx tmproto.MalleatedTx, isMalleated bool) {
	// attempt to unmarshal into a a malleated transaction
	err := proto.Unmarshal(tx, &malleatedTx)
	if err != nil {
		return malleatedTx, false
	}
	return malleatedTx, true
}

// WrapMalleatedTx creates a wrapped Tx that includes the original transaction
// and the share index of the start of its blob.
//
// NOTE: must be unwrapped to be a viable sdk.Tx
func WrapMalleatedTx(shareIndex uint32, malleated Tx) (Tx, error) {
	wTx := tmproto.MalleatedTx{
		Tx:         malleated,
		ShareIndex: shareIndex,
	}
	return proto.Marshal(&wTx)
}

// UnwrapBlobTx attempts to unmarshal a transaction into blob transaction. If an
// error is thrown, false is returned.
func UnwrapBlobTx(tx Tx) (bTx tmproto.BlobTx, isBlob bool) {
	err := bTx.Unmarshal(tx)
	if err != nil {
		return tmproto.BlobTx{}, false
	}
	return bTx, true
}

// WrapBlobTx creates a BlobTx using a normal transaction and some number of
// blobs.
//
// NOTE: Any checks on the blobs or the transaction must be performed in the
// application
func WrapBlobTx(tx []byte, blobs ...*tmproto.Blob) (Tx, error) {
	bTx := tmproto.BlobTx{
		Tx:    tx,
		Blobs: blobs,
	}
	return bTx.Marshal()
}
