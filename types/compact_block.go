package types

import (
	"errors"

	"github.com/gogo/protobuf/proto"

	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

// AssembleBlock takes a compact block and the constituent
// transactions and returns a full block
func AssembleBlock(cb *tmproto.CompactBlock, txs [][]byte) (*Block, error) {
	if cb == nil {
		return nil, errors.New("nil block")
	}
	if len(txs) != len(cb.CompactData.TxTags) {
		return nil, errors.New("txs and tx tags must be of equal length")
	}

	b := new(Block)
	h, err := HeaderFromProto(&cb.Header)
	if err != nil {
		return nil, err
	}
	b.Header = h
	b.Data = Data{
		Txs:        ToTxs(txs),
		SquareSize: cb.CompactData.SquareSize,
	}
	b.Data.hash = cb.CompactData.Hash
	if err := b.Evidence.FromProto(&cb.Evidence); err != nil {
		return nil, err
	}

	if cb.LastCommit != nil {
		lc, err := CommitFromProto(cb.LastCommit)
		if err != nil {
			return nil, err
		}
		b.LastCommit = lc
	}
	return b, nil
}

func MakePartSetFromCompactBlock(cb *tmproto.CompactBlock, partSize uint32) *PartSet {
	bz, err := proto.Marshal(cb)
	if err != nil {
		panic(err)
	}

	return NewPartSetFromData(bz, partSize)
}

func MakeCompactBlock(b *Block, txTags [][]byte) (*tmproto.CompactBlock, error) {
	if len(txTags) != len(b.Txs) {
		return nil, errors.New("tx tags must equal in length to the txs in a block")
	}

	header := b.Header.ToProto()
	if header == nil {
		return nil, errors.New("nil header")
	}

	evData, err := b.Evidence.ToProto()
	if err != nil {
		return nil, err
	}
	if evData == nil {
		return nil, errors.New("nil evidence")
	}

	return &tmproto.CompactBlock{
		Header: *header,
		CompactData: tmproto.CompactData{
			TxTags:     txTags,
			SquareSize: b.Data.SquareSize,
			Hash:       b.Data.hash,
		},
		Evidence:   *evData,
		LastCommit: b.LastCommit.ToProto(),
	}, nil
}
