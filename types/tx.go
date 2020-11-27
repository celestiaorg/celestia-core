package types

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/lazyledger/lazyledger-core/crypto/merkle"
	"github.com/lazyledger/lazyledger-core/crypto/tmhash"
	tmbytes "github.com/lazyledger/lazyledger-core/libs/bytes"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
)

// Tx has a key and value arbitrary byte array.
// NOTE: Tx has no types at this level, so when wire encoded it's just length-prefixed.
// Might we want types here ?
type Tx struct {
	Key   []byte
	Value []byte
}

// Hash computes the TMHASH hash of the wire encoded transaction.
// Hash(key) + Hash(value)
func (tx Tx) Hash() []byte {
	var bz []byte
	bz = append(bz, tmhash.Sum(tx.Key)...)
	bz = append(bz, tmhash.Sum(tx.Value)...)
	return bz
}

// Size returns the size of a transaction (Key + Value)
func (tx Tx) Size() int64 {
	var txSize int64

	txSize += int64(len(tx.Key))
	txSize += int64(len(tx.Value))

	return txSize
}

func (tx Tx) Equal(t Tx) bool {
	// check keys are the same
	if !bytes.Equal(tx.Key, t.Key) {
		return false
	}
	if !bytes.Equal(tx.Value, t.Value) {
		return false
	}
	return true
}

// String returns the hex-encoded transaction as a string.
func (tx Tx) String() string {
	return fmt.Sprintf("Tx{Key:%X, Value:%X}", tx.Key, tx.Value)
}

func (tx Tx) ToProto() *tmproto.Tx {
	return &tmproto.Tx{
		Key:   tx.Key,
		Value: tx.Value,
	}
}

func TxFromProto(pbTx *tmproto.Tx) Tx {
	if pbTx == nil {
		return Tx{}
	}
	return Tx{
		Key:   pbTx.Key,
		Value: pbTx.Value,
	}
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
		if bytes.Equal(txs[i].Hash(), tx.Hash()) {
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

// Proof returns a simple merkle proof for this node.
// Panics if i < 0 or i >= len(txs)
// TODO: optimize this!
func (txs Txs) Proof(i int) TxProof {
	l := len(txs)
	bzs := make([][]byte, l)
	for i := 0; i < l; i++ {
		bzs[i] = txs[i].Hash()
	}
	root, proofs := merkle.ProofsFromByteSlices(bzs)

	return TxProof{
		RootHash: root,
		Data:     txs[i],
		Proof:    *proofs[i],
	}
}

func (txs Txs) splitIntoShares(shareSize int) NamespacedShares {
	shares := make([]NamespacedShare, 0)
	for _, tx := range txs {
		rawData, err := tx.MarshalDelimited()
		if err != nil {
			panic(fmt.Sprintf("included Tx in mem-pool that can not be encoded %v", tx))
		}
		shares = appendToShares(shares, TxNamespaceID, rawData, shareSize)
	}
	return shares
}

// TxProof represents a Merkle proof of the presence of a transaction in the Merkle tree.
type TxProof struct {
	RootHash tmbytes.HexBytes `json:"root_hash"`
	Data     Tx               `json:"data"`
	Proof    merkle.Proof     `json:"proof"`
}

// Leaf returns the hash(tx), which is the leaf in the merkle tree which this proof refers to.
func (tp TxProof) Leaf() []byte {
	return tp.Data.Hash()
}

// Validate verifies the proof. It returns nil if the RootHash matches the dataHash argument,
// and if the proof is internally consistent. Otherwise, it returns a sensible error.
func (tp TxProof) Validate(dataHash []byte) error {
	if !bytes.Equal(dataHash, tp.RootHash) {
		return errors.New("proof matches different data hash")
	}
	if tp.Proof.Index < 0 {
		return errors.New("proof index cannot be negative")
	}
	if tp.Proof.Total <= 0 {
		return errors.New("proof total must be positive")
	}
	valid := tp.Proof.Verify(tp.RootHash, tp.Leaf())
	if valid != nil {
		return errors.New("proof is not internally consistent")
	}
	return nil
}

func (tp TxProof) ToProto() tmproto.TxProof {

	pbProof := tp.Proof.ToProto()

	pbtp := tmproto.TxProof{
		RootHash: tp.RootHash,
		Data:     &tmproto.Tx{Key: tp.Data.Key, Value: tp.Data.Value},
		Proof:    pbProof,
	}

	return pbtp
}
func TxProofFromProto(pb tmproto.TxProof) (TxProof, error) {

	pbProof, err := merkle.ProofFromProto(pb.Proof)
	if err != nil {
		return TxProof{}, err
	}

	pbtp := TxProof{
		RootHash: pb.RootHash,
		Proof:    *pbProof,
	}

	if pb.Data != nil {
		pbtp.Data = Tx{Key: pb.Data.Key, Value: pb.Data.Value}
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
