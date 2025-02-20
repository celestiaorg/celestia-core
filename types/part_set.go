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
	ErrPartSetUnexpectedIndex = errors.New("error part set unexpected index")
	ErrPartSetInvalidProof    = errors.New("error part set invalid proof")
	ErrPartTooBig             = errors.New("error part size too big")
	ErrPartInvalidSize        = errors.New("error inner part with invalid size")
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
	// All parts except the last one should have the same constant size.
	if int64(part.Index) < part.Proof.Total-1 && len(part.Bytes) != int(BlockPartSizeBytes) {
		return ErrPartInvalidSize
	}
	if int64(part.Index) != part.Proof.Index {
		return fmt.Errorf("part index %d != proof index %d", part.Index, part.Proof.Index)
	}
	if err := part.Proof.ValidateBasic(); err != nil {
		return fmt.Errorf("wrong Proof: %w", err)
	}
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
	proof, err := merkle.ProofFromProto(&pb.Proof)
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
}

// Returns an immutable, full PartSet from the data bytes.
// The data bytes are split into "partSize" chunks, and merkle tree computed.
// CONTRACT: partSize is greater than zero.
func NewPartSetFromData(data []byte, partSize uint32) (ops *PartSet, eps *PartSet) {
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

	echunks, err := Encode(chunks)
	if err != nil {
		// todo: we can likely get rid of this panic, although it should never
		// happen.
		panic(err)
	}
	echunks = echunks[ops.Total():]
	eparts := make([]*Part, total)
	epartsBitArray := bits.NewBitArray(int(total))
	for i := uint32(0); i < total; i++ {
		part := &Part{
			Index: i,
			Bytes: echunks[i],
		}
		eparts[i] = part
		epartsBitArray.SetIndex(int(i), true)
	}

	// Compute merkle proofs
	eroot, eproofs := merkle.ProofsFromByteSlices(echunks)
	for i := uint32(0); i < total; i++ {
		eparts[i].Proof = *eproofs[i]
	}

	eps = &PartSet{
		total:         total,
		hash:          eroot,
		parts:         eparts,
		partsBitArray: partsBitArray,
		count:         total,
		byteSize:      int64(len(echunks) * int(partSize)),
	}

	return ops, eps
}

// Extend erasure encodes the block parts. Only the original parts should be
// provided. The data returned has the parity parts appended to the original.
func Encode(parts [][]byte) ([][]byte, error) {
	// init an an encoder if it is not already initialized using the original
	// number of parts.
	enc, err := reedsolomon.New(len(parts), len(parts))
	if err != nil {
		return nil, err
	}

	// Create a new slice of parts with the original parts and parity parts.
	eparts := make([][]byte, len(parts)*2)
	for i := 0; i < len(eparts); i++ {
		if i < len(parts) {
			eparts[i] = parts[i]
			continue
		}
		eparts[i] = make([]byte, len(parts[0]))
	}

	// Encode the parts.
	err = enc.Encode(eparts)
	if err != nil {
		return nil, err
	}

	return eparts, nil
}

// CanDecode determines if the set of PartSets have enough parts to decode the block.
func CanDecode(ops, eps *PartSet) bool {
	return (len(ops.BitArray().GetTrueIndices()) + len(eps.BitArray().GetTrueIndices())) >= int(ops.Total())
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

	for i, part := range eps.parts {
		if part == nil {
			data[int(ops.Total())+i] = nil
			continue
		}
		data[int(ops.Total())+i] = part.Bytes
	}

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
	root, proofs := merkle.ProofsFromByteSlices(data[:ops.total])
	if !bytes.Equal(root, ops.hash) {
		return nil, nil, fmt.Errorf("reconstructed data has different hash!! want: %X, got: %X", ops.hash, root)
	}

	for i, d := range data[:ops.Total()] {
		ops.partsBitArray.SetIndex(i, true)
		if ops.parts[i] != nil {
			continue
		}
		ops.parts[i] = &Part{
			Index: uint32(i),
			Bytes: d,
			Proof: *proofs[i],
		}
	}

	ops.count = ops.total

	// recalculate all of the proofs since we apparently don't have a function
	// to generate a single proof... TODO: don't generate proofs for block parts
	// we already have.
	eroot, eproofs := merkle.ProofsFromByteSlices(data[ops.total:])
	if !bytes.Equal(eroot, eps.hash) {
		return nil, nil, fmt.Errorf("reconstructed parity data has different hash!! want: %X, got: %X", eps.hash, eroot)
	}

	for i := 0; i < int(eps.Total()); i++ {
		eps.partsBitArray.SetIndex(i, true)
		if eps.parts[i] != nil {
			continue
		}
		eps.parts[i] = &Part{
			Index: uint32(i),
			Bytes: data[int(ops.Total())+i],
			Proof: *eproofs[i],
		}
	}

	eps.count = eps.total

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
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
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
		return false, ErrPartSetInvalidProof
	}

	// Check hash proof
	if part.Proof.Verify(ps.Hash(), part.Bytes) != nil {
		return false, ErrPartSetInvalidProof
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
