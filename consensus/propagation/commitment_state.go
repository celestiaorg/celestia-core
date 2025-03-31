package propagation

import (
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

type proposalData struct {
	compactBlock  *proptypes.CompactBlock
	block         *proptypes.CombinedPartSet
	maxRequests   *bits.BitArray
	catchup       bool
	decomissioned bool
}

type ProposalCache struct {
	store         *store.BlockStore
	pmtx          *sync.Mutex
	proposals     map[int64]map[int32]*proposalData
	currentHeight int64
	currentRound  int32

	consensusHeight int64
	consensusRound  int32
}

func NewProposalCache(bs *store.BlockStore) *ProposalCache {
	pc := &ProposalCache{
		pmtx:      &sync.Mutex{},
		proposals: make(map[int64]map[int32]*proposalData),
		store:     bs,
	}

	// if there is a block saved in the store, set the current height and round.
	if bs.Height() != 0 {
		pc.currentHeight = bs.Height()
	}
	return pc
}

func (p *ProposalCache) AddProposal(cb *proptypes.CompactBlock) (added bool) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()

	if !p.relevant(cb.Proposal.Height, cb.Proposal.Round) {
		return false
	}

	if p.proposals[cb.Proposal.Height] == nil {
		p.proposals[cb.Proposal.Height] = make(map[int32]*proposalData)
	}
	if p.proposals[cb.Proposal.Height][cb.Proposal.Round] != nil {
		return false
	}

	// if we don't have this proposal, and its height is greater than the current
	// height, update the current height and round.
	if cb.Proposal.Height > p.currentHeight {
		p.currentHeight = cb.Proposal.Height
		p.currentRound = cb.Proposal.Round
	} else if cb.Proposal.Height == p.currentHeight && cb.Proposal.Round > p.currentRound {
		p.currentRound = cb.Proposal.Round
	}

	p.proposals[cb.Proposal.Height][cb.Proposal.Round] = &proposalData{
		compactBlock: cb,
		block:        proptypes.NewCombinedSetFromCompactBlock(cb),
		maxRequests:  bits.NewBitArray(int(cb.Proposal.BlockID.PartSetHeader.Total)),
	}
	return true
}

// GetProposal returns the proposal and block for a given height and round if
// this node has it stored or cached.
func (p *ProposalCache) GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool) {
	cb, parts, _, has := p.getAllState(height, round, true)
	if !has {
		return nil, nil, false
	}
	return &cb.Proposal, parts.Original(), has
}

func (p *ProposalCache) unfinishedHeights() []*proposalData {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	data := make([]*proposalData, 0)
	for _, heightData := range p.proposals {
		var prop *proposalData
		for _, pd := range heightData {
			if prop == nil {
				prop = pd
				continue
			}
			if pd.compactBlock.Proposal.Round > prop.compactBlock.Proposal.Round {
				prop = pd
			}
		}
		if prop.block.IsComplete() {
			continue
		}
		data = append(data, prop)
	}
	return data
}

func (p *ProposalCache) relevant(height int64, round int32) bool {
	if height <= p.consensusHeight {
		return false
	}

	if round < p.consensusRound {
		return false
	}

	return true
}

// GetProposal returns the proposal and block for a given height and round if
// this node has it stored or cached. It also return the max requests for that
// block.
func (p *ProposalCache) getAllState(height int64, round int32, catchup bool) (*proptypes.CompactBlock, *proptypes.CombinedPartSet, *bits.BitArray, bool) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()

	if !catchup && !p.relevant(height, round) {
		return nil, nil, nil, false
	}

	cachedProps, has := p.proposals[height]
	cachedProp, hasRound := cachedProps[round]

	// if the round is less than -1, then they're asking for the latest
	// proposal
	if round < -1 && len(cachedProps) > 0 {
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

	var hasStored *types.BlockMeta
	if height < p.currentHeight {
		hasStored = p.store.LoadBlockMeta(height)
	}

	switch {
	case has && hasRound:
		return cachedProp.compactBlock, cachedProp.block, cachedProp.maxRequests, true
	case hasStored != nil:
		parts, _, err := p.store.LoadPartSet(height)
		if err != nil {
			return nil, nil, nil, false
		}
		cparts := proptypes.NewCombinedPartSetFromOriginal(parts, false)
		return nil, cparts, cparts.BitArray(), true
	default:
		return nil, nil, nil, false
	}
}

// GetCurrentProposal returns the current proposal and block for the current
// height and round.
func (p *ProposalCache) GetCurrentProposal() (*types.Proposal, *proptypes.CombinedPartSet, bool) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	if p.proposals[p.currentHeight] == nil {
		return nil, nil, false
	}
	proposalData, has := p.proposals[p.currentHeight][p.currentRound]
	if !has {
		return nil, nil, false
	}
	return &proposalData.compactBlock.Proposal, proposalData.block, true
}

// GetCurrentCompactBlock returns the current compact block for the current
// height and round.
func (p *ProposalCache) GetCurrentCompactBlock() (*proptypes.CompactBlock, *proptypes.CombinedPartSet, bool) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	if p.proposals[p.currentHeight] == nil {
		return nil, nil, false
	}
	proposalData, has := p.proposals[p.currentHeight][p.currentRound]
	if !has {
		return nil, nil, false
	}
	return proposalData.compactBlock, proposalData.block, true
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

func (p *ProposalCache) SetConsensusRound(height int64, round int32) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	p.consensusRound = round
	// todo: delete the old round data as its no longer relevant don't delete
	// past round data if it has a POL
}

// prune deletes all cached compact blocks for heights less than the provided
// height and round.
//
// todo: also prune rounds. this requires prune in the consensus reactor after
// moving rounds.
func (p *ProposalCache) prune(pruneHeight int64) {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	for height := range p.proposals {
		if height < pruneHeight {
			delete(p.proposals, height)
		}
	}
}
