package propagation

import (
	"errors"
	"fmt"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/libs/log"
	"sync"
	"time"
)

// TODO table test

var ErrConflictingProposer = errors.New("conflicting proposer")

type ProposersCache struct {
	logger    log.Logger
	mutex     sync.RWMutex
	proposers map[int64]map[int32]crypto.PubKey
}

func NewProposersCache() *ProposersCache {
	p := ProposersCache{
		proposers: make(map[int64]map[int32]crypto.PubKey),
		logger:    log.NewNopLogger(),
		mutex:     sync.RWMutex{},
	}
	go func() {
		for {
			time.Sleep(time.Second)
			p.mutex.RLock()
			fmt.Println(p.proposers)
			p.mutex.RUnlock()
		}
	}()
	return &p
}

func (pc *ProposersCache) SetLogger(logger log.Logger) {
	pc.logger = logger
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
		pc.logger.Error(
			"conflicting proposer",
			"height",
			height,
			"round",
			round,
			"existing_proposer",
			existingProposer.Address().String(),
			"new_proposer",
			proposer.Address().String(),
		)
		return ErrConflictingProposer
	}
	pc.proposers[height] = make(map[int32]crypto.PubKey)
	pc.proposers[height][round] = proposer
	pc.logger.Info("set proposer", "height", height, "round", round)
	return nil
}
