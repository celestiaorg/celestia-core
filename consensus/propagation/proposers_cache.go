package propagation

import (
	"errors"
	"github.com/tendermint/tendermint/crypto"
	"sync"
)

// TODO table test

var ErrConflictingProposer = errors.New("conflicting proposer")

type ProposersCache struct {
	mutex     sync.RWMutex
	proposers map[int64]map[int32]crypto.PubKey
}

func NewProposersCache() *ProposersCache {
	p := ProposersCache{
		proposers: make(map[int64]map[int32]crypto.PubKey),
		mutex:     sync.RWMutex{},
	}
	// TODO add proposers pruning!
	return &p
}

func (pc *ProposersCache) GetProposer(height int64, round int32) (crypto.PubKey, bool) {
	pc.mutex.RLock()
	defer pc.mutex.RUnlock()
	pubKey, has := pc.proposers[height][round]
	return pubKey, has
}

func (pc *ProposersCache) SetProposer(height int64, round int32, proposer crypto.PubKey) error {
	pc.mutex.Lock()
	defer pc.mutex.Unlock()
	if existingProposer, ok := pc.proposers[height][round]; ok && existingProposer != nil {
		//pc.logger2.Error(
		//	"conflicting proposer",
		//	"height",
		//	height,
		//	"round",
		//	round,
		//	"existing_proposer",
		//	existingProposer.Address().String(),
		//	"new_proposer",
		//	proposer.Address().String(),
		//)
		return ErrConflictingProposer
	}
	if _, ok := pc.proposers[height]; !ok {
		pc.proposers[height] = make(map[int32]crypto.PubKey)
	}
	pc.proposers[height][round] = proposer
	pc.pruneProposers(height)
	//pc.logger2.Info("set proposer", "height", height, "round", round)
	return nil
}

func (pc *ProposersCache) pruneProposers(currentHeight int64) {
	for height := range pc.proposers {
		if height < currentHeight-10 {
			delete(pc.proposers, height)
		}
	}
}
