package propagation

import (
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

type proposalData struct {
	proposal    *types.Proposal
	block       *types.PartSet
	maxRequests *bits.BitArray
}

type ProposalCache struct {
	store         *store.BlockStore
	pmtx          *sync.RWMutex
	proposals     map[int64]map[int32]*proposalData
	currentHeight int64
	currentRound  int32
}

func NewProposalCache(bs *store.BlockStore) *ProposalCache {
	pc := &ProposalCache{
		pmtx:      &sync.RWMutex{},
		proposals: make(map[int64]map[int32]*proposalData),
		store:     bs,
	}

	// if there is a block saved in the store, set the current height and round.
	// todo(evan): probably handle this in a more complete way so that the round
	// and block data is stored somewhere.
	if bs.Height() != 0 {
		pc.currentHeight = bs.Height()
	}
	return pc
}

func (p *ProposalCache) AddProposal(proposal *types.Proposal) (added bool, gapHeights []int64, gapRounds []int32) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	if p.proposals[proposal.Height] == nil {
		p.proposals[proposal.Height] = make(map[int32]*proposalData)
	}
	if p.proposals[proposal.Height][proposal.Round] != nil {
		return false, gapHeights, gapRounds
	}

	// if the propsoal is for a lower height, make sure that we have that height

	// if we don't have this proposal, and its height is greater than the current
	// height, update the current height and round.
	if proposal.Height > p.currentHeight {
		// add the missing heights to the gapHeights
		for h := p.currentHeight + 1; h < proposal.Height; h++ {
			gapHeights = append(gapHeights, h)
		}
		p.currentHeight = proposal.Height
		p.currentRound = proposal.Round
	} else if proposal.Height == p.currentHeight && proposal.Round > p.currentRound {
		// add the missing rounds to the gapRounds
		for r := p.currentRound + 1; r < proposal.Round; r++ {
			gapRounds = append(gapRounds, r)
		}
		p.currentRound = proposal.Round
	}

	p.proposals[proposal.Height][proposal.Round] = &proposalData{
		proposal:    proposal,
		block:       types.NewPartSetFromHeader(proposal.BlockID.PartSetHeader),
		maxRequests: bits.NewBitArray(int(proposal.BlockID.PartSetHeader.Total)),
	}
	return true, gapHeights, gapRounds
}

// GetProposal returns the proposal and block for a given height and round if
// this node has it stored or cached.
func (p *ProposalCache) GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, *bits.BitArray, bool) {
	p.pmtx.RLock()
	defer p.pmtx.RUnlock()
	// try to see if we have the block stored in the store. If so, we can ignore
	// the round.
	var hasStored *types.BlockMeta
	if height < p.currentHeight {
		hasStored = p.store.LoadBlockMeta(height)
	}

	cachedProps, has := p.proposals[height]
	cachedProp, hasRound := cachedProps[round]

	// if the round is less than zero, then they're asking for the latest
	// proposal
	if round < 0 && len(cachedProps) > 0 {
		// get the latest round
		var latestRound int32
		for r := range cachedProps {
			if r > latestRound {
				latestRound = r
			}
		}
		cachedProp = cachedProps[latestRound]
		hasRound = true
	}

	switch {
	case hasStored != nil:
		parts, _, err := p.store.LoadPartSet(height)
		if err != nil {
			return nil, nil, nil, false
		}
		return nil, parts, parts.BitArray(), true
	case has && hasRound:
		return cachedProp.proposal, cachedProp.block, cachedProp.maxRequests, true
	default:
		return nil, nil, nil, false
	}
}

// GetCurrentProposal returns the current proposal and block for the current
// height and round.
func (p *ProposalCache) GetCurrentProposal() (*types.Proposal, *types.PartSet, bool) {
	p.pmtx.RLock()
	defer p.pmtx.RUnlock()
	if p.proposals[p.currentHeight] == nil {
		return nil, nil, false
	}
	proposalData, has := p.proposals[p.currentHeight][p.currentRound]
	if !has {
		return nil, nil, false
	}
	return proposalData.proposal, proposalData.block, true
}

func (p *ProposalCache) DeleteHeight(height int64) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	delete(p.proposals, height)
}

func (p *ProposalCache) DeleteRound(height int64, round int32) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	if p.proposals[height] != nil {
		delete(p.proposals[height], round)
	}
}

// prune keeps the past X proposals / blocks in memory while deleting the rest.
func (p *ProposalCache) prune(keepRecentHeights, keepRecentRounds int) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	for height := range p.proposals {
		if height < p.currentHeight-int64(keepRecentHeights) {
			delete(p.proposals, height)
		}
	}
	// delete all but the last round for each remaining height except the current.
	// this is because we need to keep the last round for the current height.
	for height := range p.proposals {
		if height == p.currentHeight {
			continue
		}
		for round := range p.proposals[height] {
			if round <= p.currentRound-int32(keepRecentRounds) {
				delete(p.proposals[height], round)
			}
		}
	}
}
