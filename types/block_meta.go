package types

import (
	"bytes"
	"errors"
	"fmt"

	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
)

// BlockMeta contains meta information.
type BlockMeta struct {
	BlockID       BlockID                `json:"block_id"`
	BlockSize     int                    `json:"block_size"`
	Header        Header                 `json:"header"`
	NumTxs        int                    `json:"num_txs"`
	DAHeader      DataAvailabilityHeader `json:"da_header"`
	PartSetHeader PartSetHeader          `json:"part_set_header"`
}

// NewBlockMeta returns a new BlockMeta.
func NewBlockMeta(block *Block, blockParts *PartSet) *BlockMeta {
	return &BlockMeta{
		BlockID:       BlockID{block.Hash()},
		BlockSize:     block.Size(),
		Header:        block.Header,
		NumTxs:        len(block.Data.Txs),
		DAHeader:      block.DataAvailabilityHeader,
		PartSetHeader: blockParts.Header(),
	}
}

func (bm *BlockMeta) ToProto() (*tmproto.BlockMeta, error) {
	if bm == nil {
		return nil, nil
	}

	protoDAH, err := bm.DAHeader.ToProto()
	if err != nil {
		return nil, err
	}

	ppsh := bm.PartSetHeader.ToProto()

	pb := &tmproto.BlockMeta{
		BlockID:       bm.BlockID.ToProto(),
		BlockSize:     int64(bm.BlockSize),
		Header:        *bm.Header.ToProto(),
		NumTxs:        int64(bm.NumTxs),
		DaHeader:      protoDAH,
		PartSetHeader: &ppsh,
	}
	return pb, nil
}

func BlockMetaFromProto(pb *tmproto.BlockMeta) (*BlockMeta, error) {
	if pb == nil {
		return nil, errors.New("blockmeta is empty")
	}

	bm := new(BlockMeta)

	bi, err := BlockIDFromProto(&pb.BlockID)
	if err != nil {
		return nil, err
	}

	h, err := HeaderFromProto(&pb.Header)
	if err != nil {
		return nil, err
	}

	dah, err := DataAvailabilityHeaderFromProto(pb.DaHeader)
	if err != nil {
		return nil, err
	}

	psh, err := PartSetHeaderFromProto(pb.PartSetHeader)
	if err != nil {
		return nil, err
	}

	bm.BlockID = *bi
	bm.BlockSize = int(pb.BlockSize)
	bm.Header = h
	bm.NumTxs = int(pb.NumTxs)
	bm.DAHeader = *dah
	bm.PartSetHeader = *psh

	return bm, bm.ValidateBasic()
}

// ValidateBasic performs basic validation.
func (bm *BlockMeta) ValidateBasic() error {
	if err := bm.BlockID.ValidateBasic(); err != nil {
		return err
	}
	if err := bm.PartSetHeader.ValidateBasic(); err != nil {
		return err
	}
	if !bytes.Equal(bm.BlockID.Hash, bm.Header.Hash()) {
		return fmt.Errorf("expected BlockID#Hash and Header#Hash to be the same, got %X != %X",
			bm.BlockID.Hash, bm.Header.Hash())
	}
	return nil
}
