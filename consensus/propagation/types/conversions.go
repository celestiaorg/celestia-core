package types

import (
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	tt "github.com/tendermint/tendermint/types"
)

func MetaPartsFromPart(height int64, round int32, part tt.Part) (protoprop.RecoveryPart, PartMetaData) {
	meta := PartMetaData{
		Index: part.Index,
		Proof: part.Proof,
	}
	rp := protoprop.RecoveryPart{
		Height: height,
		Round:  round,
		Index:  part.Index,
		Data:   part.Bytes,
	}
	return rp, meta
}

func PartFromMeta(rp protoprop.RecoveryPart, meta PartMetaData) tt.Part {
	return tt.Part{
		Index: rp.Index,
		Bytes: rp.Data,
		Proof: meta.Proof,
	}
}
