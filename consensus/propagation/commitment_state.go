package propagation

import (
	"sync/atomic"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

type proposalData struct {
	compactBlock *proptypes.CompactBlock
	block        *proptypes.CombinedPartSet
	maxRequests  *bits.BitArray
	catchup      bool
}

type ProposalCache struct {
	store     *store.BlockStore
	pmtx      *sync.Mutex
	proposals map[int64]map[int32]*proposalData

	// height the height we're trying to get consensus on.
	// the last committed height is height-1.
	height int64
	round  int32

	// currentProposalPartsCount is the maximum number of concurrent requests allowed for the current proposal.
	currentProposalPartsCount atomic.Int64
}

func NewProposalCache(bs *store.BlockStore) *ProposalCache {
	mtx := sync.Mutex{}
	pc := &ProposalCache{
		pmtx:      &mtx,
		proposals: make(map[int64]map[int32]*proposalData),
		store:     bs,
	}

	// if there is a block saved in the store, set the current height and round.
	if bs.Height() != 0 {
		pc.height = bs.Height()
	}
	return pc
}

// getCurrentProposalPartsCount returns the current proposal number of parts.
func (p *ProposalCache) getCurrentProposalPartsCount() int64 {
	return p.currentProposalPartsCount.Load()
}

// setCurrentProposalPartsCount sets the current proposal number of parts.
func (p *ProposalCache) setCurrentProposalPartsCount(limit int64) {
	p.currentProposalPartsCount.Store(limit)
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

	p.height = cb.Proposal.Height
	p.round = cb.Proposal.Round

	block := proptypes.NewCombinedSetFromCompactBlock(cb)
	p.proposals[cb.Proposal.Height][cb.Proposal.Round] = &proposalData{
		compactBlock: cb,
		block:        block,
		maxRequests:  bits.NewBitArray(int(block.Total())),
	}

	p.setCurrentProposalPartsCount(int64(block.Total()))
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
		if prop == nil || prop.block.IsComplete() {
			continue
		}
		data = append(data, prop)
	}
	return data
}

// relevant determines if a height or round is currently actionable. For
// example, passing the height that was already committed is not actionable.
// Passing a round that has already been surpassed is not actionable.
func (p *ProposalCache) relevant(height int64, round int32) bool {
	if height < p.height {
		return false
	}

	if round < p.round {
		return false
	}

	return true
}

// safeRelevant determines if a have message is relevant in a thread safe way.
func (p *ProposalCache) safeRelevant(height int64, round int32) bool {
	p.pmtx.Lock()
	defer p.pmtx.Unlock()
	return p.relevant(height, round)
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
	if height < p.height {
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
	if p.proposals[p.height] == nil {
		return nil, nil, false
	}
	proposalData, has := p.proposals[p.height][p.round]
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
	if p.proposals[p.height] == nil {
		return nil, nil, false
	}
	proposalData, has := p.proposals[p.height][p.round]
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
