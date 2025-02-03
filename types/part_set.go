package types

import (
	"bytes"
	"encoding/binary"
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
func NewPartSetFromData(data []byte, partSize uint32) *PartSet {
	// var int encode the length of the data. This is used since the the last
	// part potentially needs to be padded.
	data = AddLengthPrefix(data)

	// divide data into 4kb parts.
	//nolint:gosec
	total := (uint32(len(data)) + partSize - 1) / partSize
	etotal := total * 2
	parts := make([]*Part, etotal)
	partsBitArray := bits.NewBitArray(int(etotal))

	chunks := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		chunks[i] = data[i*partSize : cmtmath.MinInt(len(data), int((i+1)*partSize))]

		// pad with zeros if necessary
		if len(chunks[i]) < int(partSize) {
			padded := make([]byte, partSize)
			copy(padded, chunks[i])
			chunks[i] = padded
		}
	}

	echunks, err := Encode(chunks)
	if err != nil {
		// todo: we can likely get rid of this panic, although it should never
		// happen.
		panic(err)
	}

	for i, chunk := range echunks {
		ui := uint32(i)
		part := &Part{
			Index: ui,
			Bytes: chunk,
		}
		parts[i] = part
		partsBitArray.SetIndex(int(i), true)
	}

	ps := &PartSet{
		total:         etotal,
		parts:         parts,
		count:         etotal,
		byteSize:      int64(len(data)), // todo(ef): this value is not really used.
		partsBitArray: partsBitArray,
	}

	// Compute merkle proofs
	root, proofs := merkle.ProofsFromByteSlices(echunks)
	for i := uint32(0); i < etotal; i++ {
		parts[i].Proof = *proofs[i]
	}

	ps.hash = root

	return ps
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
	if ps == nil {
		return false, nil
	}
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	if part == nil {
		return false, nil
	}

	// Invalid part index
	if part.Index >= ps.total {
		return false, ErrPartSetUnexpectedIndex
	}

	// If part already exists, return false.
	if ps.parts[part.Index] != nil {
		return false, nil
	}

	// The proof should be compatible with the number of parts.
	if part.Proof.Total != int64(ps.total) {
		return false, ErrPartSetInvalidProof
	}

	// Check hash proof
	if part.Proof.Verify(ps.Hash(), part.Bytes) != nil {
		return false, ErrPartSetInvalidProof
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
	return ps.count >= ps.total
}

// IsReadyForDecoding returns true if the PartSet has every single part, not just
// ready to be decoded.
func (ps *PartSet) IsReadyForDecoding() bool {
	return ps.count >= (ps.total / 2)
}

func (ps *PartSet) GetReader() io.Reader {
	if !ps.IsComplete() {
		panic("Cannot GetReader() on incomplete PartSet")
	}
	return NewPartSetReader(ps.parts)
}

type PartSetReader struct {
	i        int
	parts    []*Part
	reader   *bytes.Reader
	totalLen int64 // total length from varint prefix
}

func NewPartSetReader(parts []*Part) *PartSetReader {
	length, n, first, err := RemoveLengthPrefix(parts[0].Bytes)
	if err != nil {
		// todo: remove this before prod ready
		panic(err)
	}

	// only use the original parts, not the ones containing erasure encoded
	// data.
	parts = parts[:len(parts)/2]

	// remove zeros if the first part is the last part.
	if len(parts) == 1 {
		first = first[:length-n]
	}

	return &PartSetReader{
		i:        0,
		parts:    parts,
		reader:   bytes.NewReader(first),
		totalLen: int64(length),
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

	// if this is the last part, we need to remove the zero padding.
	b := psr.parts[psr.i].Bytes
	if psr.i == len(psr.parts)-1 {
		removeLen := (len(psr.parts) * int(BlockPartSizeBytes)) - int(psr.totalLen)
		if removeLen >= int(BlockPartSizeBytes) {
			return 0, fmt.Errorf("removeLen is larger than the block part's size: %d", removeLen)
		}
		b = psr.parts[psr.i].Bytes[:(int(BlockPartSizeBytes) - removeLen)]
	}

	psr.reader = bytes.NewReader(b)
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
		eparts[i] = make([]byte, BlockPartSizeBytes)
	}

	// Encode the parts.
	err = enc.Encode(eparts)
	if err != nil {
		return nil, err
	}

	return eparts, nil
}

// Decode uses the block parts that are provided to reconstruct the original
// data. It throws an error if the PartSet is incomplete or the resulting root
// is different from that in the PartSetHeader. Parts are fully complete with
// proofs after decoding.
func (ps *PartSet) Decode() error {
	if !ps.IsReadyForDecoding() {
		return errors.New("cannot decode an incomplete PartSet")
	}

	// optionally setup an encoder if its not already initialized. the total
	// should always be even, and thus never round down, as it is multiplied by
	// 2.
	enc, err := reedsolomon.New(int(ps.total/2), int(ps.total/2))
	if err != nil {
		return err
	}

	data := make([][]byte, ps.total)
	for i, part := range ps.parts {
		if part == nil {
			data[i] = nil
			continue
		}
		data[i] = part.Bytes
	}

	err = enc.Reconstruct(data)
	if err != nil {
		return err
	}

	// recalculate all of the proofs since we apparently don't have a function
	// to generate a single proof... TODO: don't generate proofs for block parts
	// we already have.
	root, proofs := merkle.ProofsFromByteSlices(data)
	if !bytes.Equal(root, ps.hash) {
		return fmt.Errorf("reconstructed data has different hash!! want: %X, got: %X", ps.hash, root)
	}

	for i, d := range data {
		ps.partsBitArray.SetIndex(i, true)
		if ps.parts[i] != nil {
			continue
		}
		ps.parts[i] = &Part{
			Index: uint32(i),
			Bytes: d,
			Proof: *proofs[i],
		}
	}

	ps.count = ps.total

	return nil
}

/*
I'm currently debugging a silly bug with the length prefixes that are being used
to preserved the original data. We need to figure out why the length is not
being added properly.
*/

// AddLengthPrefix adds the length of the input data as a varint prefix to the beginning of the data.
func AddLengthPrefix(data []byte) []byte {
	length := len(data)
	// Estimate the number of bytes needed for the varint encoding
	var lengthBuf [binary.MaxVarintLen64]byte
	n := binary.PutVarint(lengthBuf[:], int64(length))

	// Create the result slice with enough space for the prefix and the data
	prefixedData := make([]byte, n+len(data))
	copy(prefixedData[:n], lengthBuf[:n]) // Copy the length prefix
	copy(prefixedData[n:], data)          // Copy the original data
	return prefixedData
}

// RemoveLengthPrefix removes the length prefix from the data and returns the length and the original data.
func RemoveLengthPrefix(prefixedData []byte) (int, int, []byte, error) {
	if len(prefixedData) == 0 {
		return 0, 0, nil, errors.New("input data is empty")
	}

	length, n := binary.Varint(prefixedData)
	if n <= 0 {
		return 0, 0, nil, errors.New("failed to decode length prefix")
	}

	return int(length) + n, n, prefixedData[n:], nil
}
