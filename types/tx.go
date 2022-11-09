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

// TxProof represents a Merkle proof of the presence of a transaction in the Merkle tree.
type TxShares struct {
	StartingShare uint64 `json:"starting_share"`
	EndShare      uint64 `json:"end_share"`
}

// SharesProof represents a Merkle proof of the presence of a transaction in the Merkle tree.
type SharesProof struct {
	RowRoots []tmbytes.HexBytes  `json:"root_hash"`
	Data     [][]byte            `json:"data"`
	Proofs   []*tmproto.NMTProof `json:"proof"`
	// NamespaceID needs to be specified as it will be used when verifying proofs.
	// A wrong NamespaceID will result in an invalid proof.
	NamespaceID []byte `json:"namespace_id"`
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
	rowRoots := make([][]byte, len(sp.RowRoots))
	for i, root := range sp.RowRoots {
		rowRoots[i] = root.Bytes()
	}
	pbtp := tmproto.SharesProof{
		RowRoots:    rowRoots,
		Data:        sp.Data,
		Proofs:      sp.Proofs,
		NamespaceId: sp.NamespaceID,
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

func SharesFromProto(pb tmproto.SharesProof) (SharesProof, error) {
	rowRoots := make([]tmbytes.HexBytes, len(pb.RowRoots))
	for i, root := range pb.RowRoots {
		rowRoots[i] = root
	}

	return SharesProof{
		RowRoots:    rowRoots,
		Data:        pb.Data,
		Proofs:      pb.Proofs,
		NamespaceID: pb.NamespaceId,
	}, nil
}

func RowsFromProto(rp tmproto.RowsProof) (RowsProof, error) {
	rowRoots := make([]tmbytes.HexBytes, len(rp.RowRoots))
	for i, root := range rp.RowRoots {
		rowRoots[i] = root
	}

	proofs := make([]*merkle.Proof, len(rp.Proofs))
	for i, proof := range rp.Proofs {
		proofs[i] = &merkle.Proof{
			Total:    proof.Total,
			Index:    proof.Index,
			LeafHash: proof.LeafHash,
			Aunts:    proof.Aunts,
		}
	}

	return RowsProof{
		RowRoots:    rowRoots,
		Proofs:      proofs,
		Root:        rp.Root,
		StartingRow: rp.StartRow,
		EndingRow:   rp.EndRow,
	}, nil
}

// ComputeProtoSizeForTxs wraps the transactions in tmproto.Data{} and calculates the size.
// https://developers.google.com/protocol-buffers/docs/encoding
func ComputeProtoSizeForTxs(txs []Tx) int64 {
	data := Data{Txs: txs}
	pdData := data.ToProto()
	return int64(pdData.Size())
}

// UnwrapMalleatedTx attempts to unmarshal the provided transaction into a malleated
// transaction wrapper, if this an be done, then it returns true. A malleated
// transaction is a normal transaction that has been derived (malleated) from a
// different original transaction. The returned hash is that of the original
// transaction, which allows us to remove the original transaction from the
// mempool. NOTE: protobuf sometimes does not throw an error if the transaction
// passed is not a tmproto.MalleatedTx, since the schema for PayForMessage is kept
// in the app, we cannot perform further checks without creating an import
// cycle.
func UnwrapMalleatedTx(tx Tx) (malleatedTx tmproto.MalleatedTx, isMalleated bool) {
	// attempt to unmarshal into a a malleated transaction
	err := proto.Unmarshal(tx, &malleatedTx)
	if err != nil {
		return malleatedTx, false
	}
	// this check will fail to catch unwanted types should those unmarshalled
	// types happen to have a hash sized slice of bytes in the same field number
	// as originalTxHash. TODO(evan): either fix this, or better yet use a different
	// mechanism
	if len(malleatedTx.OriginalTxHash) != tmhash.Size {
		return malleatedTx, false
	}
	return malleatedTx, true
}

// WrapMalleatedTx creates a wrapped Tx that includes the original transaction's hash
// so that it can be easily removed from the mempool. note: must be unwrapped to
// be a viable sdk.Tx
func WrapMalleatedTx(originalHash []byte, shareIndex uint32, malleated Tx) (Tx, error) {
	wTx := tmproto.MalleatedTx{
		OriginalTxHash: originalHash,
		Tx:             malleated,
		ShareIndex:     shareIndex,
	}
	return proto.Marshal(&wTx)
}

// Validate verifies the proof. It returns nil if the RootHash matches the dataHash argument,
// and if the proof is internally consistent. Otherwise, it returns a sensible error.
func (sp SharesProof) Validate() error {
	if len(sp.RowRoots) != len(sp.Proofs) || len(sp.Data) != len(sp.Proofs) {
		return errors.New(
			"invalid number of proofs, row roots, or data. they all must be the same to verify the proof",
		)
	}
	for _, proof := range sp.Proofs {
		if proof.Start < 0 {
			return errors.New("proof index cannot be negative")
		}
		if (proof.End - proof.Start) <= 0 {
			return errors.New("proof total must be positive")
		}
		valid := sp.VerifyProof()
		if !valid {
			return errors.New("proof is not internally consistent")
		}
	}

	return nil
}

func (sp SharesProof) VerifyProof() bool {
	cursor := int32(0)
	for i, proof := range sp.Proofs {
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
			sp.RowRoots[i],
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

// SharesProof represents a Merkle proof of the presence of a transaction in the Merkle tree.
type RowsProof struct {
	RowRoots    []tmbytes.HexBytes `json:"data"`
	Proofs      []*merkle.Proof    `json:"proof"`
	Root        []byte             `json:"root"`
	StartingRow uint32             `json:"starting_row"`
	EndingRow   uint32             `json:"ending_row"`
}

// Validate verifies the proof. It returns nil if the RootHash matches the dataHash argument,
// and if the proof is internally consistent. Otherwise, it returns a sensible error.
func (rp RowsProof) Validate() error {
	if int(rp.EndingRow-rp.StartingRow+1) != len(rp.RowRoots) {
		return errors.New(
			"invalid number of row roots, or rows range. they all must be the same to verify the proof",
		)
	}
	if !rp.VerifyProof() {
		return errors.New("proofs verification failed")
	}

	return nil
}

func (rp RowsProof) VerifyProof() bool {
	for _, proof := range rp.Proofs {
		err := proof.Verify(rp.Root, proof.LeafHash)
		if err != nil {
			return false
		}
	}
	return true
}

//func (tp *TxProof) IncludesTx(tx Tx) bool {
//	return bytes.Contains(bytes.Join(tp.Data, []byte{}), tx)
//}

func (rp RowsProof) ToProto() tmproto.RowsProof {
	rowRoots := make([][]byte, len(rp.RowRoots))
	for i, root := range rp.RowRoots {
		rowRoots[i] = root
	}
	proofs := make([]*crypto.Proof, len(rp.Proofs))
	for i, proof := range rp.Proofs {
		proofs[i] = proof.ToProto()
	}
	pbtp := tmproto.RowsProof{
		RowRoots: rowRoots,
		Proofs:   proofs,
		Root:     rp.Root,
		StartRow: rp.StartingRow, // TODO same name Starting/Start
		EndRow:   rp.EndingRow,
	}

	return pbtp
}
