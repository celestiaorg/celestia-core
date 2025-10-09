package preconf

import (
	"sync"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/types"
)

// PreconfirmationState manages the state of transaction preconfirmations.
// It tracks which validators have acknowledged seeing specific transactions.
type PreconfirmationState struct {
	mtx          sync.RWMutex
	txs          map[types.TxKey]*TxPreconfirmation
	validatorSet *types.ValidatorSet
	logger       log.Logger
}

// TxPreconfirmation holds the set of validators that have preconfirmed a transaction.
type TxPreconfirmation struct {
	mtx            sync.RWMutex
	seenValidators map[string]struct{} // Key: Validator Address (hex string)
	totalPower     int64
}

// NewPreconfirmationState creates a new PreconfirmationState.
func NewPreconfirmationState(logger log.Logger, validatorSet *types.ValidatorSet) *PreconfirmationState {
	return &PreconfirmationState{
		txs:          make(map[types.TxKey]*TxPreconfirmation),
		validatorSet: validatorSet,
		logger:       logger.With("module", "preconf"),
	}
}

// newTxPreconfirmation creates a new empty TxPreconfirmation.
func newTxPreconfirmation() *TxPreconfirmation {
	return &TxPreconfirmation{
		seenValidators: make(map[string]struct{}),
		totalPower:     0,
	}
}

// AddTx creates a preconfirmation entry for a new transaction.
// This should be called when a transaction is added to the mempool.
func (ps *PreconfirmationState) AddTx(txKey types.TxKey) {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	// Only create if it doesn't already exist
	if _, exists := ps.txs[txKey]; !exists {
		ps.txs[txKey] = newTxPreconfirmation()
	}
}

// RemoveTx removes the preconfirmation entry for a transaction.
// This should be called when a transaction is removed from the mempool.
func (ps *PreconfirmationState) RemoveTx(txKey types.TxKey) {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	delete(ps.txs, txKey)
}

// GetTotalVotingPower returns the total voting power that has preconfirmed
// a transaction. Returns 0 if the transaction is not tracked.
func (ps *PreconfirmationState) GetTotalVotingPower(txKey types.TxKey) int64 {
	ps.mtx.RLock()
	defer ps.mtx.RUnlock()

	preconf, exists := ps.txs[txKey]
	if !exists {
		return 0
	}

	preconf.mtx.RLock()
	defer preconf.mtx.RUnlock()

	return preconf.totalPower
}

// UpdateValidatorSet updates the validator set used for tracking voting power.
// This should be called whenever the validator set changes (new block height).
func (ps *PreconfirmationState) UpdateValidatorSet(validatorSet *types.ValidatorSet) {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	ps.validatorSet = validatorSet
	ps.logger.Debug("updated validator set", "validators", len(validatorSet.Validators))

	// Recalculate all voting powers with the new validator set
	ps.recalculateAllVotingPowers()
}

// recalculateAllVotingPowers recalculates the total voting power for all
// tracked transactions based on the current validator set.
// Must be called with ps.mtx held.
func (ps *PreconfirmationState) recalculateAllVotingPowers() {
	for _, preconf := range ps.txs {
		preconf.mtx.Lock()
		preconf.totalPower = ps.calculateVotingPowerLocked(preconf.seenValidators)
		preconf.mtx.Unlock()
	}
}

// calculateVotingPowerLocked calculates the total voting power for a set of
// validator addresses based on the current validator set.
// Must be called with ps.mtx held (at least read lock).
func (ps *PreconfirmationState) calculateVotingPowerLocked(validatorAddrs map[string]struct{}) int64 {
	if ps.validatorSet == nil {
		return 0
	}

	var totalPower int64
	for addr := range validatorAddrs {
		// Find the validator by address
		for _, val := range ps.validatorSet.Validators {
			if val.Address.String() == addr {
				totalPower += val.VotingPower
				break
			}
		}
	}

	return totalPower
}

// GetValidatorSetTotalPower returns the total voting power of the current validator set.
func (ps *PreconfirmationState) GetValidatorSetTotalPower() int64 {
	ps.mtx.RLock()
	defer ps.mtx.RUnlock()

	if ps.validatorSet == nil {
		return 0
	}

	return ps.validatorSet.TotalVotingPower()
}

// Size returns the number of transactions being tracked.
func (ps *PreconfirmationState) Size() int {
	ps.mtx.RLock()
	defer ps.mtx.RUnlock()

	return len(ps.txs)
}

// Reset clears all preconfirmation state.
func (ps *PreconfirmationState) Reset() {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	ps.txs = make(map[types.TxKey]*TxPreconfirmation)
	ps.logger.Debug("reset preconfirmation state")
}
