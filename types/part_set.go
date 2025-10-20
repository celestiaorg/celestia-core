package types

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/klauspost/reedsolomon"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

var (
	ErrPartSetUnexpectedIndex   = errors.New("error part set unexpected index")
	ErrPartSetInvalidProof      = errors.New("error part set invalid proof")
	ErrPartSetInvalidProofHash  = errors.New("error part set invalid proof: wrong hash")
	ErrPartSetInvalidProofTotal = errors.New("error part set invalid proof: wrong total")
	ErrPartTooBig               = errors.New("error part size too big")
	ErrPartInvalidSize          = errors.New("error inner part with invalid size")
)

// ErrInvalidPart is an error type for invalid parts.
type ErrInvalidPart struct {
	Reason error
}

func (e ErrInvalidPart) Error() string {
	return fmt.Sprintf("invalid part: %v", e.Reason)
}

func (e ErrInvalidPart) Unwrap() error {
	return e.Reason
}

type PartInfo struct {
	*Part
	Height int64
	Round  int32
}

type Part struct {
	mtx   cmtsync.Mutex
	Index uint32            `json:"index"`
	Bytes cmtbytes.HexBytes `json:"bytes"`
	Proof merkle.Proof      `json:"proof"`
	// this is just a cache not to verify the proofs multiple times.
	// shouldn't be serialised
	verified bool
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
		return ErrInvalidPart{Reason: fmt.Errorf("part index %d != proof index %d", part.Index, part.Proof.Index)}
	}
	if err := part.Proof.ValidateBasic(); err != nil {
		return ErrInvalidPart{Reason: fmt.Errorf("wrong Proof: %w", err)}
	}
	return nil
}

// GetProof returns the merkle proof while safely handling concurrent access
func (part *Part) GetProof() merkle.Proof {
	part.mtx.Lock()
	defer part.mtx.Unlock()
	return part.Proof
}

// SetProof sets the merkle proof while safely handling concurrent access
func (part *Part) SetProof(proof merkle.Proof) {
	part.mtx.Lock()
	defer part.mtx.Unlock()
	part.Proof = proof
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

// ProtoPartSetHeaderIsZero is similar to the IsZero function for
// PartSetHeader, but for the Protobuf representation.
func ProtoPartSetHeaderIsZero(ppsh *cmtproto.PartSetHeader) bool {
	return ppsh.Total == 0 && len(ppsh.Hash) == 0
}

//-------------------------------------

type PartSet struct {
	total uint32
	hash  []byte

	mtx cmtsync.Mutex

	// buffer is a single preallocated buffer for all parts
	buffer []byte
	// partSize specifies the size of each part in bytes (excluding last part)
	partSize int
	// lastPartSize is the size of the last part (which may be smaller than partSize)
	lastPartSize int
	// proofs stores part proofs separately
	proofs        []merkle.Proof
	partsBitArray *bits.BitArray
	count         uint32
	// byteSize is the total size (in bytes). Used to ensure that the
	// part set doesn't exceed the maximum block bytes
	byteSize int64

	TxPos []TxPosition
}

// Returns an immutable, full PartSet from the data bytes.
// The data bytes are split into "partSize" chunks, and merkle tree computed.
// CONTRACT: partSize is greater than zero.
func NewPartSetFromData(data []byte, partSize uint32) (ops *PartSet, err error) {
	total := (uint32(len(data)) + partSize - 1) / partSize
	chunks := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		chunk := data[i*partSize : cmtmath.MinInt(len(data), int((i+1)*partSize))]
		chunks[i] = chunk
	}

	// Compute merkle proofs
	root, proofs := merkle.ParallelProofsFromByteSlices(chunks)

	ops = NewPartSetFromHeader(PartSetHeader{
		Total: total,
		Hash:  root,
	}, partSize)

	// Fill the buffer in a single copy and populate metadata.
	copied := copy(ops.buffer, data)
	if copied != len(data) {
		return nil, fmt.Errorf("copy failed: %d < %d", copied, len(data))
	}

	// Set sizes and bookkeeping.
	if total > 0 {
		lastIdx := total - 1
		ops.lastPartSize = len(chunks[lastIdx])
	}
	ops.proofs = make([]merkle.Proof, total)
	for i := uint32(0); i < total; i++ {
		ops.proofs[i] = *proofs[i]
	}
	ops.partsBitArray.Fill()
	ops.count = total
	ops.byteSize = int64(len(data))

	return ops, nil
}

// newPartSetFromChunks creates a new PartSet from given data chunks, and other data.
func newPartSetFromChunks(chunks [][]byte, root cmtbytes.HexBytes, proofs []*merkle.Proof, partSize int) (*PartSet, error) {
	total := len(chunks)
	if total != len(proofs) {
		return nil, fmt.Errorf("chunks and proofs have different lengths: %d != %d", len(chunks), len(proofs))
	}
	if root == nil {
		return nil, fmt.Errorf("root is nil")
	}

	// create a new partset using the new parity parts.
	ps := NewPartSetFromHeader(PartSetHeader{
		Total: uint32(total),
		Hash:  root,
	}, uint32(partSize))

	// access ps directly, without mutex, because we know it is not used elsewhere
	for i := 0; i < total; i++ {
		start := i * partSize
		end := start + len(chunks[i])

		// Ensure we don't exceed buffer bounds
		if end > len(ps.buffer) {
			return nil, fmt.Errorf("part data exceeds buffer bounds")
		}

		copy(ps.buffer[start:end], chunks[i])
		ps.proofs[i] = *proofs[i]
	}
	ps.partsBitArray.Fill()
	ps.count = uint32(total)
	ps.lastPartSize = len(chunks[total-1])
	ps.byteSize = int64(len(ps.buffer))
	return ps, nil
}

// Encode Extend erasure encodes the block parts. Only the original parts should be
// provided. The parity data is formed into its own PartSet and returned
// alongside the length of the last part. The length of the last part is
// necessary because the last part may be padded with zeros after decoding. These zeros must be removed before computi
func Encode(ops *PartSet, partSize uint32) (*PartSet, int, error) {
	total := int(ops.Total())
	chunks := make([][]byte, 2*total)
	ops.mtx.Lock()
	for i := range total {
		chunks[i] = ops.getPartBytes(i)
	}
	ops.mtx.Unlock()

	ps := int(partSize)
	parityBuffer := make([]byte, total*ps) // allocate once, only slice later
	for i := 0; i < total; i++ {
		chunks[total+i] = parityBuffer[i*ps : (i+1)*ps]
	}

	// pad ONLY the last chunk and not the part with zeros if necessary AFTER the root has been generated
	lastLen := len(ops.GetPartBytes(total - 1))
	if lastLen < int(partSize) {
		padded := make([]byte, partSize)
		count := copy(padded, chunks[total-1])
		if count < len(chunks[total-1]) {
			return nil, 0, fmt.Errorf("copy failed of unpadded part with index %d: %d < %d", ops.Total()-1, count, len(chunks[ops.Total()-1]))
		}
		chunks[total-1] = padded
	}

	// init an encoder if it is not already initialized using the original
	// number of parts.
	enc, err := reedsolomon.New(total, total)
	if err != nil {
		return nil, 0, err
	}

	// Encode the parts.
	err = enc.Encode(chunks)
	if err != nil {
		return nil, 0, err
	}

	// only the parity data is needed for the new partset.
	chunks = chunks[total:]
	eroot, eproofs := merkle.ParallelProofsFromByteSlices(chunks)

	eps, err := newPartSetFromChunks(chunks, eroot, eproofs, ps)
	if err != nil {
		return nil, 0, err
	}
	return eps, lastLen, nil
}

// IsReadyForDecoding returns true if the PartSet has every single part, not just
// ready to be decoded.
// TODO: this here only requires 2/3rd. We need all the data now because we have no erasure encoding.
func (ps *PartSet) IsReadyForDecoding() bool {
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

	s := time.Now()
	data := make([][]byte, ops.Total()+eps.Total())
	ops.mtx.Lock()
	for i := 0; i < int(ops.Total()); i++ {
		chunk := ops.getPartBytes(i)
		if chunk == nil {
			data[i] = nil
			continue
		}
		if len(chunk) != int(BlockPartSizeBytes) {
			padded := make([]byte, BlockPartSizeBytes)
			count := copy(padded, chunk)
			if count < len(chunk) {
				return nil, nil, fmt.Errorf("unpadded part with index %d copy failed %d < %d", i, count, len(chunk))
			}
			chunk = padded
		}
		data[i] = chunk
	}
	ops.mtx.Unlock()

	eps.mtx.Lock()
	for i := 0; i < int(eps.Total()); i++ {
		chunk := eps.getPartBytes(i)
		if chunk == nil {
			data[int(ops.Total())+i] = nil
			continue
		}
		data[int(ops.Total())+i] = chunk
	}
	eps.mtx.Unlock()

	fmt.Println("decode1: ", time.Since(s))
	err = enc.Reconstruct(data)
	if err != nil {
		return nil, nil, err
	}

	// prune the last part if we need to
	if len(data[:(ops.Total()-1)]) != lastPartLen {
		data[(ops.Total() - 1)] = data[(ops.Total() - 1)][:lastPartLen]
	}

	// recalculate all of the proofs since we apparently don't have a function
	// to generate a single proof... TODO: don't generate proofs for block parts
	// we already have...
	root, proofs := merkle.ParallelProofsFromByteSlices(data[:ops.Total()])
	if !bytes.Equal(root, ops.Hash()) {
		return nil, nil, fmt.Errorf("reconstructed data has different hash!! want: %X, got: %X", ops.hash, root)
	}

	eg := errgroup.Group{}
	eg.SetLimit(runtime.NumCPU())
	for i, d := range data[:ops.Total()] {
		eg.Go(func() error {
			if !ops.HasPart(i) {
				added, err := ops.AddPart(&Part{
					Index: uint32(i),
					Bytes: d,
					Proof: *proofs[i],
				})
				if err != nil {
					return err
				}
				if !added {
					return fmt.Errorf("couldn't add original part %d when decoding", i)
				}
			}
			return nil
		})
	}
	err = eg.Wait()
	if err != nil {
		return nil, nil, err
	}

	// recalculate all of the proofs since we apparently don't have a function
	// to generate a single proof... TODO: don't generate proofs for block parts
	// we already have.
	eroot, eproofs := merkle.ParallelProofsFromByteSlices(data[ops.Total():])
	if !bytes.Equal(eroot, eps.Hash()) {
		return nil, nil, fmt.Errorf("reconstructed parity data has different hash!! want: %X, got: %X", eps.hash, eroot)
	}

	eg = errgroup.Group{}
	eg.SetLimit(runtime.NumCPU())
	for i := 0; i < int(eps.Total()); i++ {
		eg.Go(func() error {
			if !eps.HasPart(i) {
				added, err := eps.AddPart(&Part{
					Index: uint32(i),
					Bytes: data[int(ops.Total())+i],
					Proof: *eproofs[i],
				})
				if err != nil {
					return err
				}
				if !added {
					return fmt.Errorf("couldn't add parity part %d when decoding", i)
				}
			}
			return nil
		})
	}
	err = eg.Wait()
	if err != nil {
		return nil, nil, err
	}

	return ops, eps, nil
}

// Returns an empty PartSet ready to be populated.
func NewPartSetFromHeader(header PartSetHeader, partSize uint32) *PartSet {
	// Calculate buffer size: (total-1) full parts + potentially smaller last part
	bufferSize := int(header.Total) * int(partSize)
	return &PartSet{
		total:         header.Total,
		hash:          header.Hash,
		buffer:        make([]byte, bufferSize),
		partSize:      int(partSize),
		lastPartSize:  0, // default to 0; will be updated when the last part is added
		proofs:        make([]merkle.Proof, header.Total),
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

// CONTRACT: part is validated using ValidateBasic.
func (ps *PartSet) AddPart(part *Part) (bool, error) {
	// TODO: remove this? would be preferable if this only returned (false, nil)
	// when its a duplicate block part
	if ps == nil {
		return false, nil
	}

	if part == nil {
		return false, fmt.Errorf("nil part")
	}

	// If part already exists, return false.
	if ps.partsBitArray.GetIndex(int(part.Index)) {
		return false, nil
	}

	// The proof should be compatible with the number of parts.
	if part.Proof.Total != int64(ps.total) {
		return false, fmt.Errorf(ErrPartSetInvalidProofTotal.Error()+":%v %v", part.Proof.Total, ps.total)
	}

	if !part.verified {
		if err := part.Proof.Verify(ps.Hash(), part.Bytes); err != nil {
			return false, fmt.Errorf("%w:%w", ErrPartSetInvalidProofHash, err)
		}
		part.verified = true
	}

	return ps.addPart(part)
}

func (ps *PartSet) AddPartWithoutProof(part *Part) (bool, error) {
	return ps.addPart(part)
}

func (ps *PartSet) addPart(part *Part) (bool, error) {
	if part == nil {
		return false, errors.New("nil part")
	}

	// Invalid part index
	if part.Index >= ps.total {
		return false, ErrPartSetUnexpectedIndex
	}

	// If part already exists, return false.
	if ps.partsBitArray.GetIndex(int(part.Index)) {
		return false, nil
	}

	// Calculate buffer position and copy part data
	start := int(part.Index) * ps.partSize
	end := start + len(part.Bytes)

	// Ensure we don't exceed buffer bounds
	if end > len(ps.buffer) {
		return false, fmt.Errorf("part data exceeds buffer bounds")
	}

	ps.mtx.Lock()

	copy(ps.buffer[start:end], part.Bytes)
	ps.proofs[part.Index] = part.Proof

	// Track last part size if this is the last part
	if part.Index == ps.total-1 {
		ps.lastPartSize = len(part.Bytes)
	}
	ps.count++
	ps.byteSize += int64(len(part.Bytes))

	ps.mtx.Unlock()

	ps.partsBitArray.SetIndex(int(part.Index), true)

	return true, nil
}

// getPartBytes returns the bytes for a part from the internal buffer.
// Assumes mutex is already locked.
//
// Returns nil if:
// - The part at given index doesn't exist (not yet received)
// - The index is out of bounds
// - Buffer access would be out of bounds
func (ps *PartSet) getPartBytes(index int) []byte {
	if !ps.partsBitArray.GetIndex(index) {
		return nil
	}

	// Calculate buffer position
	start := index * ps.partSize
	partSize := ps.partSize

	// For the last part, use the actual size
	if index == int(ps.total)-1 {
		partSize = ps.lastPartSize
	}

	end := start + partSize
	if start >= len(ps.buffer) || end > len(ps.buffer) {
		return nil
	}

	return ps.buffer[start:end]
}

// GetPartBytes returns only the bytes for a part, without the proof.
// This is more efficient when only the data is needed.
func (ps *PartSet) GetPartBytes(index int) []byte {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	return ps.getPartBytes(index)
}

func (ps *PartSet) GetPart(index int) *Part {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	bytes := ps.getPartBytes(index)
	if bytes == nil {
		return nil
	}

	return &Part{
		Index: uint32(index),
		Bytes: bytes,
		Proof: ps.proofs[index],
	}
}

func (ps *PartSet) HasPart(index int) bool {
	return ps.partsBitArray.GetIndex(index)
}

func (ps *PartSet) IsComplete() bool {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()
	return ps.count == ps.total
}

func (ps *PartSet) GetBytes() []byte {
	if !ps.IsComplete() {
		panic("Cannot GetBytes() on incomplete PartSet")
	}
	dataSize := int(ps.total-1)*ps.partSize + ps.lastPartSize
	return ps.buffer[:dataSize]
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
