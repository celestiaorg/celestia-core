package preconf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/types"
)

// createTestValidatorSet creates a test validator set with the specified number of validators.
func createTestValidatorSet(numVals int) *types.ValidatorSet {
	vals := make([]*types.Validator, numVals)
	for i := 0; i < numVals; i++ {
		privKey := ed25519.GenPrivKey()
		pubKey := privKey.PubKey()
		vals[i] = types.NewValidator(pubKey, 10) // Each validator has 10 voting power
	}
	return types.NewValidatorSet(vals)
}

// getTxKey creates a test transaction key from a string.
func getTxKey(s string) types.TxKey {
	tx := types.Tx(s)
	return tx.Key()
}

func TestNewPreconfirmationState(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)

	state := NewPreconfirmationState(logger, valSet)

	require.NotNil(t, state)
	assert.NotNil(t, state.txs)
	assert.NotNil(t, state.validatorSet)
	assert.NotNil(t, state.logger)
	assert.Equal(t, 0, state.Size())
	assert.Equal(t, int64(30), state.GetValidatorSetTotalPower()) // 3 validators * 10 voting power
}

func TestAddTx(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet)

	txKey1 := getTxKey("test-tx-1")
	txKey2 := getTxKey("test-tx-2")

	// Add first transaction
	state.AddTx(txKey1)
	assert.Equal(t, 1, state.Size())
	assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey1))

	// Add second transaction
	state.AddTx(txKey2)
	assert.Equal(t, 2, state.Size())
	assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey2))

	// Adding the same transaction again should not increase size
	state.AddTx(txKey1)
	assert.Equal(t, 2, state.Size())
}

func TestRemoveTx(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet)

	txKey1 := getTxKey("test-tx-1")
	txKey2 := getTxKey("test-tx-2")

	// Add transactions
	state.AddTx(txKey1)
	state.AddTx(txKey2)
	assert.Equal(t, 2, state.Size())

	// Remove first transaction
	state.RemoveTx(txKey1)
	assert.Equal(t, 1, state.Size())
	assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey1))

	// Remove second transaction
	state.RemoveTx(txKey2)
	assert.Equal(t, 0, state.Size())

	// Removing a non-existent transaction should not cause issues
	state.RemoveTx(getTxKey("non-existent"))
	assert.Equal(t, 0, state.Size())
}

func TestReset(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet)

	// Add multiple transactions
	for i := 0; i < 5; i++ {
		state.AddTx(getTxKey("test-tx-" + string(rune(i))))
	}
	assert.Equal(t, 5, state.Size())

	// Reset should clear all transactions
	state.Reset()
	assert.Equal(t, 0, state.Size())
}

func TestUpdateValidatorSet(t *testing.T) {
	logger := log.TestingLogger()
	valSet1 := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet1)

	assert.Equal(t, int64(30), state.GetValidatorSetTotalPower())

	// Update to a new validator set with different voting power
	valSet2 := createTestValidatorSet(5)
	state.UpdateValidatorSet(valSet2)

	assert.Equal(t, int64(50), state.GetValidatorSetTotalPower())
}

func TestCalculateVotingPowerLocked(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet)

	// Get validator addresses
	val1Addr := valSet.Validators[0].Address.String()
	val2Addr := valSet.Validators[1].Address.String()
	val3Addr := valSet.Validators[2].Address.String()

	tests := []struct {
		name          string
		seenVals      map[string]struct{}
		expectedPower int64
	}{
		{
			name: "one validator",
			seenVals: map[string]struct{}{
				val1Addr: {},
			},
			expectedPower: 10,
		},
		{
			name: "two validators",
			seenVals: map[string]struct{}{
				val1Addr: {},
				val2Addr: {},
			},
			expectedPower: 20,
		},
		{
			name: "all three validators",
			seenVals: map[string]struct{}{
				val1Addr: {},
				val2Addr: {},
				val3Addr: {},
			},
			expectedPower: 30,
		},
		{
			name: "unknown validator address ignored",
			seenVals: map[string]struct{}{
				val1Addr:          {},
				val2Addr:          {},
				"unknown-address": {},
			},
			expectedPower: 20,
		},
		{
			name:          "empty set",
			seenVals:      map[string]struct{}{},
			expectedPower: 0,
		},
		{
			name: "only unknown validators",
			seenVals: map[string]struct{}{
				"unknown-1": {},
				"unknown-2": {},
			},
			expectedPower: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			power := state.calculateVotingPowerLocked(tt.seenVals)
			assert.Equal(t, tt.expectedPower, power)
		})
	}
}

func TestUpdateValidatorSetRecalculates(t *testing.T) {
	logger := log.TestingLogger()
	valSet1 := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet1)

	txKey := getTxKey("test-tx")
	state.AddTx(txKey)

	// Manually add a validator to the transaction's seen list
	val1Addr := valSet1.Validators[0].Address.String()
	state.mtx.Lock()
	preconf := state.txs[txKey]
	preconf.mtx.Lock()
	preconf.seenValidators[val1Addr] = struct{}{}
	preconf.totalPower = 10
	preconf.mtx.Unlock()
	state.mtx.Unlock()

	assert.Equal(t, int64(10), state.GetTotalVotingPower(txKey))

	// Create a new validator set where the same validator has different power
	newVals := make([]*types.Validator, 1)
	privKey := ed25519.GenPrivKey()
	pubKey := privKey.PubKey()
	newVals[0] = types.NewValidator(pubKey, 20)
	valSet2 := types.NewValidatorSet(newVals)

	// Update validator set - this should recalculate voting powers
	state.UpdateValidatorSet(valSet2)

	// The voting power should now be 0 because val1 is not in the new set
	assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey))
}

func TestNilValidatorSet(t *testing.T) {
	logger := log.TestingLogger()
	state := NewPreconfirmationState(logger, nil)

	txKey := getTxKey("test-tx")
	state.AddTx(txKey)

	// With nil validator set, voting power should be 0
	assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey))
	assert.Equal(t, int64(0), state.GetValidatorSetTotalPower())
}

func TestConcurrentAccess(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(10)
	state := NewPreconfirmationState(logger, valSet)

	// Test concurrent adds and removes
	const numGoroutines = 10
	const numOpsPerGoroutine = 100

	done := make(chan bool, numGoroutines*2)

	// Concurrent adds
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < numOpsPerGoroutine; j++ {
				txKey := getTxKey("tx-" + string(rune(id*numOpsPerGoroutine+j)))
				state.AddTx(txKey)
			}
			done <- true
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < numOpsPerGoroutine; j++ {
				_ = state.Size()
				_ = state.GetValidatorSetTotalPower()
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to finish
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}

	// Should have added all transactions
	assert.Equal(t, numGoroutines*numOpsPerGoroutine, state.Size())
}
