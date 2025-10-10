package preconf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/log"
	protomemcat "github.com/cometbft/cometbft/proto/tendermint/mempool/cat"
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

	state := NewPreconfirmationState(logger, valSet, nil, "", nil, nil)
	defer state.Stop()

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
	state := NewPreconfirmationState(logger, valSet, nil, "", nil, nil)
	defer state.Stop()

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
	state := NewPreconfirmationState(logger, valSet, nil, "", nil, nil)

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
	state := NewPreconfirmationState(logger, valSet, nil, "", nil, nil)

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
	state := NewPreconfirmationState(logger, valSet1, nil, "", nil, nil)

	assert.Equal(t, int64(30), state.GetValidatorSetTotalPower())

	// Update to a new validator set with different voting power
	valSet2 := createTestValidatorSet(5)
	state.UpdateValidatorSet(valSet2)

	assert.Equal(t, int64(50), state.GetValidatorSetTotalPower())
}

func TestCalculateVotingPowerLocked(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	state := NewPreconfirmationState(logger, valSet, nil, "", nil, nil)

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
	state := NewPreconfirmationState(logger, valSet1, nil, "", nil, nil)

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
	state := NewPreconfirmationState(logger, nil, nil, "", nil, nil)

	txKey := getTxKey("test-tx")
	state.AddTx(txKey)

	// With nil validator set, voting power should be 0
	assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey))
	assert.Equal(t, int64(0), state.GetValidatorSetTotalPower())

	// Test that UpdateValidatorSet doesn't panic with nil
	state.UpdateValidatorSet(nil)
	assert.Equal(t, int64(0), state.GetValidatorSetTotalPower())
}

func TestConcurrentAccess(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(10)
	state := NewPreconfirmationState(logger, valSet, nil, "", nil, nil)

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

// TestProcessMessage tests the asynchronous processing of preconfirmation messages.
func TestProcessMessage(t *testing.T) {
	logger := log.TestingLogger()
	privVal := types.NewMockPV()
	pubKey, _ := privVal.GetPubKey()

	// Create a validator set with our test validator
	val := types.NewValidator(pubKey, 10)
	valSet := types.NewValidatorSet([]*types.Validator{val})

	state := NewPreconfirmationState(logger, valSet, nil, "test-chain", nil, nil)
	defer state.Stop()

	// Add a transaction to track
	txKey := getTxKey("test-tx")
	state.AddTx(txKey)

	t.Run("valid message", func(t *testing.T) {
		// Create and sign a valid preconfirmation message
		msg := &protomemcat.PreconfirmationMessage{
			TxHashes: [][]byte{txKey[:]},
		}

		// Sign it properly
		signedMsg := signPreconfMessage(t, msg, privVal, "test-chain")

		// Process the message
		state.processMessage(signedMsg)

		// Verify voting power was updated
		power := state.GetTotalVotingPower(txKey)
		assert.Equal(t, int64(10), power)
	})

	t.Run("invalid signature", func(t *testing.T) {
		// Create a message with an invalid signature
		pubKeyObj, _ := privVal.GetPubKey()
		protoPubKey, _ := encoding.PubKeyToProto(pubKeyObj)

		msg := &protomemcat.PreconfirmationMessage{
			TxHashes:  [][]byte{txKey[:]},
			Signature: []byte("invalid-signature"),
			PubKey:    protoPubKey,
			Timestamp: testTimestamp(),
		}

		// Reset the transaction's preconfirmation state
		state.RemoveTx(txKey)
		state.AddTx(txKey)

		// Process the message - should be rejected
		state.processMessage(msg)

		// Verify voting power was NOT updated
		power := state.GetTotalVotingPower(txKey)
		assert.Equal(t, int64(0), power)
	})

	t.Run("unknown validator", func(t *testing.T) {
		// Create a message from an unknown validator
		unknownPrivVal := types.NewMockPV()
		msg := &protomemcat.PreconfirmationMessage{
			TxHashes: [][]byte{txKey[:]},
		}

		signedMsg := signPreconfMessage(t, msg, unknownPrivVal, "test-chain")

		// Reset the transaction's preconfirmation state
		state.RemoveTx(txKey)
		state.AddTx(txKey)

		// Process the message - should be rejected because validator is not in set
		state.processMessage(signedMsg)

		// Verify voting power was NOT updated
		power := state.GetTotalVotingPower(txKey)
		assert.Equal(t, int64(0), power)
	})

	t.Run("empty tx hashes", func(t *testing.T) {
		// Create a message with no transaction hashes
		msg := &protomemcat.PreconfirmationMessage{
			TxHashes: [][]byte{},
		}

		signedMsg := signPreconfMessage(t, msg, privVal, "test-chain")

		// Process the message - should not error, just do nothing
		state.processMessage(signedMsg)
	})
}

// TestSendUpdate tests the SendUpdate method which sends messages to the update channel.
func TestSendUpdate(t *testing.T) {
	logger := log.TestingLogger()
	privVal := types.NewMockPV()
	pubKey, _ := privVal.GetPubKey()

	val := types.NewValidator(pubKey, 10)
	valSet := types.NewValidatorSet([]*types.Validator{val})

	state := NewPreconfirmationState(logger, valSet, nil, "test-chain", nil, nil)
	defer state.Stop()

	txKey := getTxKey("test-tx")
	state.AddTx(txKey)

	// Create and sign a message
	msg := &protomemcat.PreconfirmationMessage{
		TxHashes: [][]byte{txKey[:]},
	}
	signedMsg := signPreconfMessage(t, msg, privVal, "test-chain")

	// Send the message
	state.SendUpdate(signedMsg)

	// Give the goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Verify the message was processed
	power := state.GetTotalVotingPower(txKey)
	assert.Equal(t, int64(10), power)
}

// TestVotingPowerAggregation tests that voting power from multiple validators is correctly aggregated.
func TestVotingPowerAggregation(t *testing.T) {
	logger := log.TestingLogger()

	// Create 3 validators with different voting powers
	privVal1 := types.NewMockPV()
	privVal2 := types.NewMockPV()
	privVal3 := types.NewMockPV()

	pubKey1, _ := privVal1.GetPubKey()
	pubKey2, _ := privVal2.GetPubKey()
	pubKey3, _ := privVal3.GetPubKey()

	val1 := types.NewValidator(pubKey1, 10)
	val2 := types.NewValidator(pubKey2, 20)
	val3 := types.NewValidator(pubKey3, 30)

	valSet := types.NewValidatorSet([]*types.Validator{val1, val2, val3})

	state := NewPreconfirmationState(logger, valSet, nil, "test-chain", nil, nil)
	defer state.Stop()

	txKey := getTxKey("test-tx")
	state.AddTx(txKey)

	// Send preconfirmation from validator 1
	msg1 := &protomemcat.PreconfirmationMessage{
		TxHashes: [][]byte{txKey[:]},
	}
	signedMsg1 := signPreconfMessage(t, msg1, privVal1, "test-chain")
	state.SendUpdate(signedMsg1)
	time.Sleep(50 * time.Millisecond)

	power := state.GetTotalVotingPower(txKey)
	assert.Equal(t, int64(10), power)

	// Send preconfirmation from validator 2
	msg2 := &protomemcat.PreconfirmationMessage{
		TxHashes: [][]byte{txKey[:]},
	}
	signedMsg2 := signPreconfMessage(t, msg2, privVal2, "test-chain")
	state.SendUpdate(signedMsg2)
	time.Sleep(50 * time.Millisecond)

	power = state.GetTotalVotingPower(txKey)
	assert.Equal(t, int64(30), power) // 10 + 20

	// Send preconfirmation from validator 3
	msg3 := &protomemcat.PreconfirmationMessage{
		TxHashes: [][]byte{txKey[:]},
	}
	signedMsg3 := signPreconfMessage(t, msg3, privVal3, "test-chain")
	state.SendUpdate(signedMsg3)
	time.Sleep(50 * time.Millisecond)

	power = state.GetTotalVotingPower(txKey)
	assert.Equal(t, int64(60), power) // 10 + 20 + 30

	// Sending duplicate from validator 1 should not increase power
	state.SendUpdate(signedMsg1)
	time.Sleep(50 * time.Millisecond)

	power = state.GetTotalVotingPower(txKey)
	assert.Equal(t, int64(60), power) // Still 60
}

// TestSetCallbacks tests the SetCallbacks method.
func TestSetCallbacks(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	privVal := types.NewMockPV()

	// Create state without callbacks
	state := NewPreconfirmationState(logger, valSet, privVal, "test-chain", nil, nil)
	defer state.Stop()

	// Verify callbacks are nil
	assert.Nil(t, state.getTxHashes)
	assert.Nil(t, state.broadcastMsg)

	// Set callbacks
	getTxHashesCalled := false
	broadcastMsgCalled := false

	getTxHashes := func() []types.TxKey {
		getTxHashesCalled = true
		return []types.TxKey{getTxKey("test-tx")}
	}

	broadcastMsg := func(msg *protomemcat.PreconfirmationMessage) {
		broadcastMsgCalled = true
	}

	state.SetCallbacks(getTxHashes, broadcastMsg)

	// Verify callbacks were set
	assert.NotNil(t, state.getTxHashes)
	assert.NotNil(t, state.broadcastMsg)

	// Trigger signing to verify callbacks are called
	err := state.signAndBroadcast()
	require.NoError(t, err)

	assert.True(t, getTxHashesCalled)
	assert.True(t, broadcastMsgCalled)
}

// Helper functions

func testTimestamp() time.Time {
	return time.Now()
}

func signPreconfMessage(t *testing.T, msg *protomemcat.PreconfirmationMessage, privVal types.PrivValidator, chainID string) *protomemcat.PreconfirmationMessage {
	t.Helper()

	// Get public key
	pubKey, err := privVal.GetPubKey()
	require.NoError(t, err)

	// Convert to proto
	protoPubKey, err := encoding.PubKeyToProto(pubKey)
	require.NoError(t, err)

	// Set fields
	msg.PubKey = protoPubKey
	msg.Timestamp = testTimestamp()

	// Create a copy WITHOUT the signature for signing (to match processMessage verification)
	msgCopy := &protomemcat.PreconfirmationMessage{
		PubKey:    msg.PubKey,
		TxHashes:  msg.TxHashes,
		Timestamp: msg.Timestamp,
	}

	// Marshal for signing
	rawBytes, err := msgCopy.Marshal()
	require.NoError(t, err)

	// Sign using SignRawBytes (matching the pattern in signAndBroadcast)
	signature, err := privVal.SignRawBytes(chainID, "preconfirmation", rawBytes)
	require.NoError(t, err)

	// Set signature
	msg.Signature = signature

	return msg
}

// TestSignAndBroadcast tests the signing logic for preconfirmation messages.
func TestSignAndBroadcast(t *testing.T) {
	t.Run("sign with transactions", func(t *testing.T) {
		logger := log.TestingLogger()
		valSet := createTestValidatorSet(3)
		privVal := types.NewMockPV()

		// Track what was broadcast
		var broadcastedMsg *protomemcat.PreconfirmationMessage
		broadcastMsg := func(msg *protomemcat.PreconfirmationMessage) {
			broadcastedMsg = msg
		}

		// Create a slice that we can modify
		txKeys := []types.TxKey{
			getTxKey("test-tx-1"),
			getTxKey("test-tx-2"),
			getTxKey("test-tx-3"),
			getTxKey("test-tx-4"),
			getTxKey("test-tx-5"),
		}

		getTxHashes := func() []types.TxKey {
			return txKeys
		}

		state := NewPreconfirmationState(logger, valSet, privVal, "test-chain", getTxHashes, broadcastMsg)
		defer state.Stop()

		// Call the signing function
		err := state.signAndBroadcast()
		require.NoError(t, err)

		// Verify that a message was broadcast with the correct number of transactions
		require.NotNil(t, broadcastedMsg)
		assert.Equal(t, 5, len(broadcastedMsg.TxHashes))
		assert.NotEmpty(t, broadcastedMsg.Signature)
		assert.NotNil(t, broadcastedMsg.PubKey)
	})

	t.Run("sign empty mempool", func(t *testing.T) {
		logger := log.TestingLogger()
		valSet := createTestValidatorSet(3)
		privVal := types.NewMockPV()

		getTxHashes := func() []types.TxKey {
			return []types.TxKey{}
		}

		broadcastMsg := func(msg *protomemcat.PreconfirmationMessage) {}

		state := NewPreconfirmationState(logger, valSet, privVal, "test-chain", getTxHashes, broadcastMsg)
		defer state.Stop()

		err := state.signAndBroadcast()
		require.NoError(t, err)
	})

	t.Run("no private validator", func(t *testing.T) {
		logger := log.TestingLogger()
		valSet := createTestValidatorSet(3)

		getTxHashes := func() []types.TxKey {
			return []types.TxKey{getTxKey("test-tx")}
		}

		state := NewPreconfirmationState(logger, valSet, nil, "test-chain", getTxHashes, nil)
		defer state.Stop()

		err := state.signAndBroadcast()
		require.Error(t, err)
		require.Contains(t, err.Error(), "private validator is not configured")
	})

	t.Run("no getTxHashes callback", func(t *testing.T) {
		logger := log.TestingLogger()
		valSet := createTestValidatorSet(3)
		privVal := types.NewMockPV()

		state := NewPreconfirmationState(logger, valSet, privVal, "test-chain", nil, nil)
		defer state.Stop()

		err := state.signAndBroadcast()
		require.NoError(t, err) // Should handle gracefully
	})
}

// TestGossipDeduplication tests that transactions are only gossiped once.
// This verifies the "gossip once" logic in signAndBroadcast.
func TestGossipDeduplication(t *testing.T) {
	logger := log.TestingLogger()
	valSet := createTestValidatorSet(3)
	privVal := types.NewMockPV()

	// Track which transactions were broadcast
	var broadcastedTxs [][]byte
	var broadcastCount int

	txKeys := []types.TxKey{
		getTxKey("test-tx-1"),
		getTxKey("test-tx-2"),
		getTxKey("test-tx-3"),
	}

	getTxHashes := func() []types.TxKey {
		return txKeys
	}

	broadcastMsg := func(msg *protomemcat.PreconfirmationMessage) {
		broadcastCount++
		broadcastedTxs = append(broadcastedTxs, msg.TxHashes...)
	}

	state := NewPreconfirmationState(logger, valSet, privVal, "test-chain", getTxHashes, broadcastMsg)
	defer state.Stop()

	// First call to signAndBroadcast - should gossip all transactions
	err := state.signAndBroadcast()
	require.NoError(t, err)

	// Verify all transactions were broadcast
	assert.Equal(t, 1, broadcastCount)
	assert.Equal(t, 3, len(broadcastedTxs))

	// Reset broadcast tracking
	broadcastedTxs = nil

	// Second call to signAndBroadcast - should NOT gossip any transactions (deduplication)
	err = state.signAndBroadcast()
	require.NoError(t, err)

	// Verify NO transactions were broadcast this time
	assert.Equal(t, 1, broadcastCount) // Should still be 1
	assert.Equal(t, 0, len(broadcastedTxs))

	// Add a new transaction
	newTxKey := getTxKey("test-tx-4")
	txKeys = append(txKeys, newTxKey)

	// Third call to signAndBroadcast - should ONLY gossip the new transaction
	err = state.signAndBroadcast()
	require.NoError(t, err)

	// Verify only the new transaction was broadcast
	assert.Equal(t, 2, broadcastCount) // Should now be 2
	assert.Equal(t, 1, len(broadcastedTxs))
}

// TestGarbageCollection tests the realistic lifecycle of add, preconfirm, and remove.
// This verifies that RemoveTx properly cleans up both the preconfirmation state
// and the gossiped transactions map, preventing memory leaks.
func TestGarbageCollection(t *testing.T) {
	logger := log.TestingLogger()
	privVal := types.NewMockPV()
	pubKey, _ := privVal.GetPubKey()

	val := types.NewValidator(pubKey, 10)
	valSet := types.NewValidatorSet([]*types.Validator{val})

	state := NewPreconfirmationState(logger, valSet, nil, "test-chain", nil, nil)
	defer state.Stop()

	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "add, preconfirm, and remove single transaction",
			test: func(t *testing.T) {
				txKey := getTxKey("test-tx-1")

				// Step 1: Add transaction
				state.AddTx(txKey)
				assert.Equal(t, 1, state.Size())
				assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey))

				// Step 2: Preconfirm transaction
				msg := &protomemcat.PreconfirmationMessage{
					TxHashes: [][]byte{txKey[:]},
				}
				signedMsg := signPreconfMessage(t, msg, privVal, "test-chain")
				state.SendUpdate(signedMsg)
				time.Sleep(50 * time.Millisecond) // Allow processing

				// Verify preconfirmation was recorded
				assert.Equal(t, int64(10), state.GetTotalVotingPower(txKey))

				// Step 3: Remove transaction
				state.RemoveTx(txKey)

				// Verify complete cleanup
				assert.Equal(t, 0, state.Size())
				assert.Equal(t, int64(0), state.GetTotalVotingPower(txKey))

				// Verify gossipedTxs was also cleaned up
				state.mtx.RLock()
				_, inGossipedTxs := state.gossipedTxs[txKey]
				state.mtx.RUnlock()
				assert.False(t, inGossipedTxs, "transaction should be removed from gossipedTxs")
			},
		},
		{
			name: "add, preconfirm multiple validators, and remove",
			test: func(t *testing.T) {
				// Create multiple validators
				privVal1 := types.NewMockPV()
				privVal2 := types.NewMockPV()
				privVal3 := types.NewMockPV()

				pubKey1, _ := privVal1.GetPubKey()
				pubKey2, _ := privVal2.GetPubKey()
				pubKey3, _ := privVal3.GetPubKey()

				val1 := types.NewValidator(pubKey1, 10)
				val2 := types.NewValidator(pubKey2, 20)
				val3 := types.NewValidator(pubKey3, 30)

				multiValSet := types.NewValidatorSet([]*types.Validator{val1, val2, val3})
				multiState := NewPreconfirmationState(logger, multiValSet, nil, "test-chain", nil, nil)
				defer multiState.Stop()

				txKey := getTxKey("test-tx-2")

				// Add transaction
				multiState.AddTx(txKey)
				assert.Equal(t, 1, multiState.Size())

				// Preconfirm from all three validators
				for _, pv := range []types.PrivValidator{privVal1, privVal2, privVal3} {
					msg := &protomemcat.PreconfirmationMessage{
						TxHashes: [][]byte{txKey[:]},
					}
					signedMsg := signPreconfMessage(t, msg, pv, "test-chain")
					multiState.SendUpdate(signedMsg)
				}
				time.Sleep(100 * time.Millisecond)

				// Verify total voting power
				assert.Equal(t, int64(60), multiState.GetTotalVotingPower(txKey))

				// Remove transaction
				multiState.RemoveTx(txKey)

				// Verify complete cleanup
				assert.Equal(t, 0, multiState.Size())
				assert.Equal(t, int64(0), multiState.GetTotalVotingPower(txKey))

				// Verify internal state is clean
				multiState.mtx.RLock()
				_, exists := multiState.txs[txKey]
				_, inGossiped := multiState.gossipedTxs[txKey]
				multiState.mtx.RUnlock()

				assert.False(t, exists, "transaction should not exist in txs map")
				assert.False(t, inGossiped, "transaction should not exist in gossipedTxs map")
			},
		},
		{
			name: "lifecycle with multiple transactions",
			test: func(t *testing.T) {
				multiState := NewPreconfirmationState(logger, valSet, nil, "test-chain", nil, nil)
				defer multiState.Stop()

				// Add multiple transactions
				txKeys := []types.TxKey{
					getTxKey("test-tx-3"),
					getTxKey("test-tx-4"),
					getTxKey("test-tx-5"),
				}

				for _, txKey := range txKeys {
					multiState.AddTx(txKey)
				}
				assert.Equal(t, 3, multiState.Size())

				// Preconfirm all transactions
				for _, txKey := range txKeys {
					msg := &protomemcat.PreconfirmationMessage{
						TxHashes: [][]byte{txKey[:]},
					}
					signedMsg := signPreconfMessage(t, msg, privVal, "test-chain")
					multiState.SendUpdate(signedMsg)
				}
				time.Sleep(100 * time.Millisecond)

				// Verify all are preconfirmed
				for _, txKey := range txKeys {
					assert.Equal(t, int64(10), multiState.GetTotalVotingPower(txKey))
				}

				// Remove transactions one by one
				for i, txKey := range txKeys {
					multiState.RemoveTx(txKey)
					expectedSize := len(txKeys) - i - 1
					assert.Equal(t, expectedSize, multiState.Size())

					// Verify removed transaction is cleaned up
					multiState.mtx.RLock()
					_, exists := multiState.txs[txKey]
					_, inGossiped := multiState.gossipedTxs[txKey]
					multiState.mtx.RUnlock()

					assert.False(t, exists)
					assert.False(t, inGossiped)
				}

				// Verify complete cleanup
				assert.Equal(t, 0, multiState.Size())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.test)
	}
}
