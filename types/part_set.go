package types

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/reedsolomon"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
	cmtbytes "github.com/tendermint/tendermint/libs/bytes"
	cmtjson "github.com/tendermint/tendermint/libs/json"
	cmtmath "github.com/tendermint/tendermint/libs/math"
	cmtsync "github.com/tendermint/tendermint/libs/sync"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

var (
	ErrPartSetUnexpectedIndex   = errors.New("error part set unexpected index")
	ErrPartSetInvalidProof      = errors.New("error part set invalid proof")
	ErrPartSetInvalidProofHash  = errors.New("error part set invalid proof: wrong hash")
	ErrPartSetInvalidProofTotal = errors.New("error part set invalid proof: wrong total")
	ErrPartTooBig               = errors.New("error part size too big")
	ErrPartInvalidSize          = errors.New("error inner part with invalid size")
)

type Part struct {
	Index uint32            `json:"index"`
	Bytes cmtbytes.HexBytes `json:"bytes"`
	Proof merkle.Proof      `json:"proof"`
}

// ValidateBasic performs basic validation.
func (part *Part) ValidateBasic() error {
	if len(part.Bytes) > int(BlockPartSizeBytes) {
		return ErrPartTooBig
	}
	// // All parts except the last one should have the same constant size.
	// if int64(part.Index) < part.Proof.Total-1 && len(part.Bytes) != int(BlockPartSizeBytes) {
	// 	return ErrPartInvalidSize
	// }
	// if int64(part.Index) != part.Proof.Index {
	// 	return fmt.Errorf("part index %d != proof index %d", part.Index, part.Proof.Index)
	// }
	// if err := part.Proof.ValidateBasic(); err != nil {
	// 	return fmt.Errorf("wrong Proof: %w", err)
	// }
	return nil
}

// String returns a string representation of Part.
//
// See StringIndented.
func (part *Part) String() string {
	return part.StringIndented("")
}

// StringIndented returns an indented Part.
//
// See merkle.Proof#StringIndented
func (part *Part) StringIndented(indent string) string {
	return fmt.Sprintf(`Part{#%v
%s  Bytes: %X...
%s  Proof: %v
%s}`,
		part.Index,
		indent, cmtbytes.Fingerprint(part.Bytes),
		indent, part.Proof.StringIndented(indent+"  "),
		indent)
}

func (part *Part) ToProto() (*cmtproto.Part, error) {
	if part == nil {
		return nil, errors.New("nil part")
	}
	pb := new(cmtproto.Part)
	proof := part.Proof.ToProto()

	pb.Index = part.Index
	pb.Bytes = part.Bytes
	pb.Proof = *proof

	return pb, nil
}

func PartFromProto(pb *cmtproto.Part) (*Part, error) {
	if pb == nil {
		return nil, errors.New("nil part")
	}

	part := new(Part)
	proof, err := merkle.ProofFromProto(&pb.Proof, false)
	if err != nil {
		return nil, err
	}
	part.Index = pb.Index
	part.Bytes = pb.Bytes
	part.Proof = *proof

	return part, part.ValidateBasic()
}

//-------------------------------------

type PartSetHeader struct {
	Total uint32            `json:"total"`
	Hash  cmtbytes.HexBytes `json:"hash"`
}

// String returns a string representation of PartSetHeader.
//
// 1. total number of parts
// 2. first 6 bytes of the hash
func (psh PartSetHeader) String() string {
	return fmt.Sprintf("%v:%X", psh.Total, cmtbytes.Fingerprint(psh.Hash))
}

func (psh PartSetHeader) IsZero() bool {
	return psh.Total == 0 && len(psh.Hash) == 0
}

func (psh PartSetHeader) Equals(other PartSetHeader) bool {
	return psh.Total == other.Total && bytes.Equal(psh.Hash, other.Hash)
}

// ValidateBasic performs basic validation.
func (psh PartSetHeader) ValidateBasic() error {
	// Hash can be empty in case of POLBlockID.PartSetHeader in Proposal.
	if err := ValidateHash(psh.Hash); err != nil {
		return fmt.Errorf("wrong Hash: %w", err)
	}
	return nil
}

// ToProto converts PartSetHeader to protobuf
func (psh *PartSetHeader) ToProto() cmtproto.PartSetHeader {
	if psh == nil {
		return cmtproto.PartSetHeader{}
	}

	return cmtproto.PartSetHeader{
		Total: psh.Total,
		Hash:  psh.Hash,
	}
}

// FromProto sets a protobuf PartSetHeader to the given pointer
func PartSetHeaderFromProto(ppsh *cmtproto.PartSetHeader) (*PartSetHeader, error) {
	if ppsh == nil {
		return nil, errors.New("nil PartSetHeader")
	}
	psh := new(PartSetHeader)
	psh.Total = ppsh.Total
	psh.Hash = ppsh.Hash

	return psh, psh.ValidateBasic()
}

//-------------------------------------

type PartSet struct {
	total uint32
	hash  []byte

	mtx           cmtsync.Mutex
	parts         []*Part
	partsBitArray *bits.BitArray
	count         uint32
	// a count of the total size (in bytes). Used to ensure that the
	// part set doesn't exceed the maximum block bytes
	byteSize int64

	TxPos []TxPosition
}

// Returns an immutable, full PartSet from the data bytes.
// The data bytes are split into "partSize" chunks, and merkle tree computed.
// CONTRACT: partSize is greater than zero.
func NewPartSetFromData(data []byte, partSize uint32) (ops *PartSet) {
	total := (uint32(len(data)) + partSize - 1) / partSize
	parts := make([]*Part, total)
	partsBitArray := bits.NewBitArray(int(total))
	chunks := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		chunk := data[i*partSize : cmtmath.MinInt(len(data), int((i+1)*partSize))]
		part := &Part{
			Index: i,
			Bytes: chunk,
		}
		parts[i] = part
		partsBitArray.SetIndex(int(i), true)
		chunks[i] = chunk
	}

	// Compute merkle proofs
	root, proofs := merkle.ProofsFromByteSlices(chunks)
	for i := uint32(0); i < total; i++ {
		parts[i].Proof = *proofs[i]
	}

	ops = &PartSet{
		total:         total,
		hash:          root,
		parts:         parts,
		partsBitArray: partsBitArray,
		count:         total,
		byteSize:      int64(len(data)),
	}

	// pad ONLY the last chunk and not the part with zeros if necessary AFTER the root has been generated
	if len(chunks[len(chunks)-1]) < int(partSize) {
		padded := make([]byte, partSize)
		copy(padded, chunks[len(chunks)-1])
		chunks[len(chunks)-1] = padded
	}

	return ops
}

// Extend erasure encodes the block parts. Only the original parts should be
// provided. The parity data is formed into its own PartSet and returned
// alongside the length of the last part. The length of the last part is
// necessary because the last part may be padded with zeros after decoding. These zeros must be removed before computi
func Encode(ops *PartSet, partSize uint32) (*PartSet, int, error) {
	chunks := make([][]byte, 2*ops.Total())
	lastLen := len(ops.GetPart(int(ops.Total() - 1)).Bytes.Bytes())
	// pad ONLY the last chunk and not the part with zeros if necessary AFTER the root has been generated
	for i := range chunks {
		if i < int(ops.Total()) {
			chunks[i] = ops.GetPart(i).Bytes.Bytes()
			continue
		}
		chunks[i] = make([]byte, partSize)
	}

	// pad ONLY the last chunk and not the part with zeros if necessary AFTER the root has been generated
	if lastLen < int(partSize) {
		padded := make([]byte, partSize)
		copy(padded, chunks[ops.Total()-1])
		chunks[ops.Total()-1] = padded
	}

	// init an an encoder if it is not already initialized using the original
	// number of parts.
	enc, err := reedsolomon.New(int(ops.Total()), int(ops.Total()))
	if err != nil {
		return nil, 0, err
	}

	// Encode the parts.
	err = enc.Encode(chunks)
	if err != nil {
		return nil, 0, err
	}

	// only the parity data is needed for the new partset.
	chunks = chunks[ops.Total():]
	newParts := make([]*Part, ops.Total())
	ba := bits.NewBitArray(int(ops.Total()))
	eroot, eproofs := merkle.ProofsFromByteSlices(chunks)

	// create a new partset using the new parity parts.
	for i := uint32(0); i < ops.Total(); i++ {
		part := &Part{
			Index: i,
			Bytes: chunks[i],
			Proof: *eproofs[i],
		}
		newParts[i] = part
		ba.SetIndex(int(i), true)
	}

	eps := &PartSet{
		total:         ops.Total(),
		hash:          eroot,
		parts:         newParts,
		partsBitArray: ba,
		count:         ops.Total(),
		byteSize:      int64(ops.Total()) * int64(partSize),
	}

	return eps, lastLen, nil
}

// IsReadyForDecoding returns true if the PartSet has every single part, not just
// ready to be decoded.
// TODO: this here only requires 2/3rd. We need all the data now because we have no erasure encoding.
func (ps *PartSet) IsReadyForDecoding() bool {
	//return ps.count >= (ps.total / 2)
	return ps.IsComplete()
}

// Decode uses the block parts that are provided to reconstruct the original
// data. It throws an error if the PartSet is incomplete or the resulting root
// is different from that in the PartSetHeader. Parts are fully complete with
// proofs after decoding.
func Decode(ops, eps *PartSet, lastPartLen int) (*PartSet, *PartSet, error) {
	enc, err := reedsolomon.New(int(ops.total), int(eps.total))
	if err != nil {
		return nil, nil, err
	}

	data := make([][]byte, ops.Total()+eps.Total())
	unpadded := []byte{}
	ops.mtx.Lock()
	for i, part := range ops.parts {
		if part == nil {
			data[i] = nil
			continue
		}
		chunk := part.Bytes
		if len(chunk) != int(BlockPartSizeBytes) {
			unpadded = chunk
			padded := make([]byte, BlockPartSizeBytes)
			copy(padded, chunk)
			chunk = padded
		}
		data[i] = chunk
	}
	ops.mtx.Unlock()

	eps.mtx.Lock()
	for i, part := range eps.parts {
		if part == nil {
			data[int(ops.Total())+i] = nil
			continue
		}
		data[int(ops.Total())+i] = part.Bytes
	}
	eps.mtx.Unlock()

	err = enc.Reconstruct(data)
	if err != nil {
		return nil, nil, err
	}

	// prune the last part if we need to
	if len(data[:(ops.Total()-1)]) != lastPartLen {
		data[(ops.Total() - 1)] = data[(ops.Total() - 1)][:lastPartLen]
	}

	if len(unpadded) != 0 {
		data[ops.Total()-1] = unpadded
	}

	// recalculate all of the proofs since we apparently don't have a function
	// to generate a single proof... TODO: don't generate proofs for block parts
	// we already have...
	root, proofs := merkle.ProofsFromByteSlices(data[:ops.Total()])
	if !bytes.Equal(root, ops.Hash()) {
		return nil, nil, fmt.Errorf("reconstructed data has different hash!! want: %X, got: %X", ops.hash, root)
	}

	ops.mtx.Lock()
	for i, d := range data[:ops.Total()] {
		ops.partsBitArray.SetIndex(i, true)
		if ops.parts[i] != nil {
			ops.parts[i].Proof = *proofs[i]
			continue
		}
		ops.parts[i] = &Part{
			Index: uint32(i),
			Bytes: d,
			Proof: *proofs[i],
		}
	}

	ops.count = ops.total
	ops.mtx.Unlock()

	// recalculate all of the proofs since we apparently don't have a function
	// to generate a single proof... TODO: don't generate proofs for block parts
	// we already have.
	eroot, eproofs := merkle.ProofsFromByteSlices(data[ops.Total():])
	if !bytes.Equal(eroot, eps.Hash()) {
		return nil, nil, fmt.Errorf("reconstructed parity data has different hash!! want: %X, got: %X", eps.hash, eroot)
	}

	eps.mtx.Lock()
	for i := 0; i < int(eps.Total()); i++ {
		eps.partsBitArray.SetIndex(i, true)
		if eps.parts[i] != nil {
			eps.parts[i].Proof = *eproofs[i]
			continue
		}
		eps.parts[i] = &Part{
			Index: uint32(i),
			Bytes: data[int(ops.Total())+i],
			Proof: *eproofs[i],
		}
	}

	eps.count = eps.total
	eps.mtx.Unlock()

	return ops, eps, nil
}

// Returns an empty PartSet ready to be populated.
func NewPartSetFromHeader(header PartSetHeader) *PartSet {
	return &PartSet{
		total:         header.Total,
		hash:          header.Hash,
		parts:         make([]*Part, header.Total),
		partsBitArray: bits.NewBitArray(int(header.Total)),
		count:         0,
		byteSize:      0,
	}
}

func (ps *PartSet) Header() PartSetHeader {
	if ps == nil {
		return PartSetHeader{}
	}
	return PartSetHeader{
		Total: ps.total,
		Hash:  ps.hash,
	}
}

func (ps *PartSet) HasHeader(header PartSetHeader) bool {
	if ps == nil {
		return false
	}
	return ps.Header().Equals(header)
}

func (ps *PartSet) BitArray() *bits.BitArray {
	return ps.partsBitArray.Copy()
}

func (ps *PartSet) Hash() []byte {
	if ps == nil {
		return merkle.HashFromByteSlices(nil)
	}
	return ps.hash
}

func (ps *PartSet) HashesTo(hash []byte) bool {
	if ps == nil {
		return false
	}
	return bytes.Equal(ps.hash, hash)
}

func (ps *PartSet) Count() uint32 {
	if ps == nil {
		return 0
	}
	return ps.count
}

func (ps *PartSet) ByteSize() int64 {
	if ps == nil {
		return 0
	}
	return ps.byteSize
}

func (ps *PartSet) Total() uint32 {
	if ps == nil {
		return 0
	}
	return ps.total
}

func (ps *PartSet) AddPart(part *Part) (bool, error) {
	if part == nil {
		return false, fmt.Errorf("nil part")
	}

	// The proof should be compatible with the number of parts.
	if part.Proof.Total != int64(ps.total) {
		return false, fmt.Errorf(ErrPartSetInvalidProofTotal.Error()+":%v %v", part.Proof.Total, ps.total)
	}

	if part.Proof.Verify(ps.Hash(), part.Bytes) != nil {
		return false, ErrPartSetInvalidProofHash
	}

	return ps.AddPartWithoutProof(part)
}

func (ps *PartSet) AddPartWithoutProof(part *Part) (bool, error) {
	if part == nil {
		return false, fmt.Errorf("nil part")
	}
	if ps == nil {
		return false, nil
	}
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	// Invalid part index
	if part.Index >= ps.total {
		return false, ErrPartSetUnexpectedIndex
	}

	// If part already exists, return false.
	if ps.parts[part.Index] != nil {
		return false, nil
	}

	// Add part
	ps.parts[part.Index] = part
	ps.partsBitArray.SetIndex(int(part.Index), true)
	ps.count++
	ps.byteSize += int64(len(part.Bytes))
	return true, nil
}

func (ps *PartSet) GetPart(index int) *Part {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	return ps.parts[index]
}

func (ps *PartSet) IsComplete() bool {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	return ps.count == ps.total
}

func (ps *PartSet) GetReader() io.Reader {
	if !ps.IsComplete() {
		panic("Cannot GetReader() on incomplete PartSet")
	}
	return NewPartSetReader(ps.parts)
}

type PartSetReader struct {
	i      int
	parts  []*Part
	reader *bytes.Reader
}

func NewPartSetReader(parts []*Part) *PartSetReader {
	return &PartSetReader{
		i:      0,
		parts:  parts,
		reader: bytes.NewReader(parts[0].Bytes),
	}
}

func (psr *PartSetReader) Read(p []byte) (n int, err error) {
	readerLen := psr.reader.Len()
	if readerLen >= len(p) {
		return psr.reader.Read(p)
	} else if readerLen > 0 {
		n1, err := psr.Read(p[:readerLen])
		if err != nil {
			return n1, err
		}
		n2, err := psr.Read(p[readerLen:])
		return n1 + n2, err
	}

	psr.i++
	if psr.i >= len(psr.parts) {
		return 0, io.EOF
	}
	psr.reader = bytes.NewReader(psr.parts[psr.i].Bytes)
	return psr.Read(p)
}

// StringShort returns a short version of String.
//
// (Count of Total)
func (ps *PartSet) StringShort() string {
	if ps == nil {
		return "nil-PartSet"
	}
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	return fmt.Sprintf("(%v of %v)", ps.Count(), ps.Total())
}

func (ps *PartSet) MarshalJSON() ([]byte, error) {
	if ps == nil {
		return []byte("{}"), nil
	}

	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	return cmtjson.Marshal(struct {
		CountTotal    string         `json:"count/total"`
		PartsBitArray *bits.BitArray `json:"parts_bit_array"`
	}{
		fmt.Sprintf("%d/%d", ps.Count(), ps.Total()),
		ps.partsBitArray,
	})
}
