package types

import (
	"bytes"
	"errors"
	"fmt"

	tmbytes "github.com/lazyledger/lazyledger-core/libs/bytes"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
)

// BlockMeta contains meta information.
type BlockMeta struct {
	HeaderHash tmbytes.HexBytes `json:"header_hash"`
	BlockSize  int              `json:"block_size"`
	Header     Header           `json:"header"`
	NumTxs     int              `json:"num_txs"`
}

// NewBlockMeta returns a new BlockMeta.
func NewBlockMeta(block *Block, blockParts *PartSet) *BlockMeta {
	return &BlockMeta{
		HeaderHash: block.Header.Hash(),
		BlockSize:  block.Size(),
		Header:     block.Header,
		NumTxs:     len(block.Data.Txs),
	}
}

func (bm *BlockMeta) ToProto() *tmproto.BlockMeta {
	if bm == nil {
		return nil
	}

	pb := &tmproto.BlockMeta{
		HeaderHash: bm.HeaderHash,
		BlockSize:  int64(bm.BlockSize),
		Header:     *bm.Header.ToProto(),
		NumTxs:     int64(bm.NumTxs),
	}
	return pb
}

func BlockMetaFromProto(pb *tmproto.BlockMeta) (*BlockMeta, error) {
	if pb == nil {
		return nil, errors.New("blockmeta is empty")
	}

	bm := new(BlockMeta)

	h, err := HeaderFromProto(&pb.Header)
	if err != nil {
		return nil, err
	}

	bm.HeaderHash = pb.HeaderHash
	bm.BlockSize = int(pb.BlockSize)
	bm.Header = h
	bm.NumTxs = int(pb.NumTxs)

	return bm, bm.ValidateBasic()
}

// ValidateBasic performs basic validation.
func (bm *BlockMeta) ValidateBasic() error {
	if err := ValidateHash(bm.HeaderHash); err != nil {
		return err
	}
	if !bytes.Equal(bm.HeaderHash, bm.Header.Hash()) {
		return fmt.Errorf("expected BlockID#Hash and Header#Hash to be the same, got %X != %X",
			bm.HeaderHash, bm.Header.Hash())
	}
	return nil
}
