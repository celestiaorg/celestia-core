package propagation

import (
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/types"
)

// generateParity schedules parity generation once, after originals reach consensus.
func (blockProp *Reactor) generateParity(cb *proptypes.CompactBlock, parts *proptypes.CombinedPartSet) {
	blockProp.generateParityWith(cb, parts, types.Encode)
}

func (blockProp *Reactor) generateParityWith(cb *proptypes.CompactBlock, parts *proptypes.CombinedPartSet, encode func(*types.PartSet, uint32) (*types.PartSet, int, error)) {
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()
	entry := blockProp.proposals[cb.Proposal.Height][cb.Proposal.Round]
	if blockProp.ctx.Err() != nil || entry == nil || entry.compactBlock != cb || entry.block != parts ||
		len(cb.PartsHashes) == 0 || !parts.IsComplete() || parts.Parity().IsComplete() {
		return
	}
	original := parts.Original()
	entry.parityOnce.Do(func() {
		go func() {
			if !blockProp.hasParityProposal(cb, parts) {
				return
			}
			// Completed originals are immutable; encoding holds no reactor or
			// combined-part-set lock needed by consensus or other peers.
			parity, _, err := encode(original, types.BlockPartSizeBytes)
			if err != nil {
				blockProp.Logger.Error("failed to generate parity", "height", cb.Proposal.Height, "round", cb.Proposal.Round, "err", err)
				return
			}
			blockProp.publishParity(cb, parts, parity)
		}()
	})
}

func (blockProp *Reactor) hasParityProposal(cb *proptypes.CompactBlock, parts *proptypes.CombinedPartSet) bool {
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()
	entry := blockProp.proposals[cb.Proposal.Height][cb.Proposal.Round]
	return blockProp.ctx.Err() == nil && entry != nil && entry.compactBlock == cb && entry.block == parts
}

func (blockProp *Reactor) publishParity(cb *proptypes.CompactBlock, parts *proptypes.CombinedPartSet, parity *types.PartSet) {
	blockProp.pmtx.Lock()
	entry := blockProp.proposals[cb.Proposal.Height][cb.Proposal.Round]
	if blockProp.ctx.Err() != nil || entry == nil || entry.compactBlock != cb || entry.block != parts {
		blockProp.pmtx.Unlock()
		return
	}
	err := parts.SetParity(parity)
	blockProp.pmtx.Unlock()
	if err != nil {
		blockProp.Logger.Error("failed to install parity", "height", cb.Proposal.Height, "round", cb.Proposal.Round, "err", err)
		return
	}
	if !blockProp.hasParityProposal(cb, parts) {
		return
	}
	blockProp.broadcastLocalHaves(blockProp.self, cb, parts)

	// Requests may have been advertised before their parity arrived. Serve
	// them even though this node has already delivered every original.
	for i := uint32(0); i < parity.Total(); i++ {
		if !blockProp.hasParityProposal(cb, parts) {
			return
		}
		part := parity.GetPart(int(i))
		blockProp.clearWants(&proptypes.RecoveryPart{
			Height: cb.Proposal.Height,
			Round:  cb.Proposal.Round,
			Index:  parity.Total() + i,
			Data:   part.Bytes,
		}, part.Proof)
	}
}
