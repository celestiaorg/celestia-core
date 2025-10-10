package preconf

import (
	"fmt"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/log"
	protomemcat "github.com/cometbft/cometbft/proto/tendermint/mempool/cat"
	"github.com/cometbft/cometbft/types"
)

const (
	// PreconfirmationInterval defines how often validators broadcast preconfirmation messages.
	PreconfirmationInterval = 2 * time.Second
)

// PreconfirmationState manages the state of transaction preconfirmations.
// It tracks which validators have acknowledged seeing specific transactions.
type PreconfirmationState struct {
	mtx          sync.RWMutex
	txs          map[types.TxKey]*TxPreconfirmation
	validatorSet *types.ValidatorSet
	updateChan   chan *protomemcat.PreconfirmationMessage
	logger       log.Logger
	quit         chan struct{}

	// Signing and broadcasting
	privVal      types.PrivValidator
	chainID      string
	getTxHashes  func() []types.TxKey
	broadcastMsg func(*protomemcat.PreconfirmationMessage)
	ticker       *time.Ticker
	gossipedTxs  map[types.TxKey]struct{} // Track which transactions we've already gossiped
}

// TxPreconfirmation holds the set of validators that have preconfirmed a transaction.
type TxPreconfirmation struct {
	mtx            sync.RWMutex
	seenValidators map[string]struct{} // Key: Validator Address (hex string)
	totalPower     int64
}

// NewPreconfirmationState creates a new PreconfirmationState.
// It starts background goroutines to process preconfirmation messages and sign new ones.
// If privVal is nil, signing will be disabled but the state will still track preconfirmations from peers.
// The getTxHashes and broadcastMsg callbacks can be nil and set later via SetCallbacks.
func NewPreconfirmationState(
	logger log.Logger,
	validatorSet *types.ValidatorSet,
	privVal types.PrivValidator,
	chainID string,
	getTxHashes func() []types.TxKey,
	broadcastMsg func(*protomemcat.PreconfirmationMessage),
) *PreconfirmationState {
	ps := &PreconfirmationState{
		txs:          make(map[types.TxKey]*TxPreconfirmation),
		validatorSet: validatorSet,
		updateChan:   make(chan *protomemcat.PreconfirmationMessage, 100),
		logger:       logger.With("module", "preconf"),
		quit:         make(chan struct{}),
		privVal:      privVal,
		chainID:      chainID,
		getTxHashes:  getTxHashes,
		broadcastMsg: broadcastMsg,
		gossipedTxs:  make(map[types.TxKey]struct{}),
	}

	// Start the processing goroutine
	go ps.processUpdates()

	// Start the signing loop if we have a private validator
	if privVal != nil {
		ps.ticker = time.NewTicker(PreconfirmationInterval)
		go ps.signingLoop()
	}

	return ps
}

// SetCallbacks updates the getTxHashes and broadcastMsg callbacks for lazy initialization.
func (ps *PreconfirmationState) SetCallbacks(
	getTxHashes func() []types.TxKey,
	broadcastMsg func(*protomemcat.PreconfirmationMessage),
) {
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	ps.getTxHashes = getTxHashes
	ps.broadcastMsg = broadcastMsg
	ps.logger.Debug("updated preconfirmation callbacks")
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
	delete(ps.gossipedTxs, txKey)
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
	ps.gossipedTxs = make(map[types.TxKey]struct{})
	ps.logger.Debug("reset preconfirmation state")
}

// SendUpdate sends a preconfirmation message to the update channel for processing.
// This is called by the reactor after signing a message or receiving one from a peer.
func (ps *PreconfirmationState) SendUpdate(msg *protomemcat.PreconfirmationMessage) {
	select {
	case ps.updateChan <- msg:
	default:
		ps.logger.Error("preconfirmation update channel is full, dropping message")
	}
}

// Stop stops the processing and signing goroutines.
func (ps *PreconfirmationState) Stop() {
	close(ps.quit)
	if ps.ticker != nil {
		ps.ticker.Stop()
	}
}

// processUpdates is a goroutine that processes preconfirmation messages from the update channel.
func (ps *PreconfirmationState) processUpdates() {
	for {
		select {
		case <-ps.quit:
			return
		case msg := <-ps.updateChan:
			ps.processMessage(msg)
		}
	}
}

// processMessage processes a single preconfirmation message.
// It verifies the signature and updates the preconfirmation state for each transaction hash.
func (ps *PreconfirmationState) processMessage(msg *protomemcat.PreconfirmationMessage) {
	ps.mtx.RLock()
	validatorSet := ps.validatorSet
	ps.mtx.RUnlock()

	if validatorSet == nil {
		ps.logger.Debug("validator set is nil, skipping preconfirmation message")
		return
	}

	// Convert proto public key to crypto.PubKey
	pubKey, err := encoding.PubKeyFromProto(msg.PubKey)
	if err != nil {
		ps.logger.Error("failed to convert public key", "err", err)
		return
	}

	// Find the validator by public key
	valAddress := pubKey.Address()
	_, validator := validatorSet.GetByAddress(valAddress)
	if validator == nil {
		ps.logger.Debug("preconfirmation from unknown validator", "address", valAddress.String())
		return
	}

	// Verify the signature by reconstructing the signed message
	msgCopy := &protomemcat.PreconfirmationMessage{
		PubKey:    msg.PubKey,
		TxHashes:  msg.TxHashes,
		Timestamp: msg.Timestamp,
	}
	rawBytes, err := msgCopy.Marshal()
	if err != nil {
		ps.logger.Error("failed to marshal message for verification", "err", err)
		return
	}

	// Reconstruct the sign bytes using the same framing as SignRawBytes
	signBytes, err := types.RawBytesMessageSignBytes(ps.chainID, "preconfirmation", rawBytes)
	if err != nil {
		ps.logger.Error("failed to construct sign bytes for verification", "err", err)
		return
	}

	if !pubKey.VerifySignature(signBytes, msg.Signature) {
		ps.logger.Error("invalid signature on preconfirmation message", "validator", valAddress.String())
		return
	}

	// Update preconfirmation state for each transaction hash
	validatorAddrStr := valAddress.String()
	ps.mtx.Lock()
	defer ps.mtx.Unlock()

	for _, txHash := range msg.TxHashes {
		txKey, err := types.TxKeyFromBytes(txHash)
		if err != nil {
			ps.logger.Error("invalid tx hash in preconfirmation message", "err", err)
			continue
		}

		preconf, exists := ps.txs[txKey]
		if !exists {
			preconf = newTxPreconfirmation()
			ps.txs[txKey] = preconf
		}

		preconf.mtx.Lock()
		if _, seen := preconf.seenValidators[validatorAddrStr]; !seen {
			preconf.seenValidators[validatorAddrStr] = struct{}{}
			preconf.totalPower += validator.VotingPower
		}
		preconf.mtx.Unlock()
	}

	ps.logger.Debug("processed preconfirmation message",
		"validator", validatorAddrStr,
		"num_txs", len(msg.TxHashes),
		"voting_power", validator.VotingPower)
}

// signingLoop periodically signs and broadcasts preconfirmation messages.
func (ps *PreconfirmationState) signingLoop() {
	for {
		select {
		case <-ps.quit:
			return
		case <-ps.ticker.C:
			if err := ps.signAndBroadcast(); err != nil {
				ps.logger.Error("failed to sign and broadcast preconfirmation", "err", err)
			}
		}
	}
}

// signAndBroadcast gathers transaction hashes from the mempool,
// creates and signs a PreconfirmationMessage, sends it to the local update channel,
// and broadcasts it to peers.
func (ps *PreconfirmationState) signAndBroadcast() error {
	if ps.privVal == nil {
		return fmt.Errorf("private validator is not configured")
	}

	ps.mtx.RLock()
	getTxHashes := ps.getTxHashes
	broadcastMsg := ps.broadcastMsg
	ps.mtx.RUnlock()

	if getTxHashes == nil {
		ps.logger.Debug("getTxHashes callback not yet configured, skipping preconfirmation")
		return nil
	}

	allTxKeys := getTxHashes()
	if len(allTxKeys) == 0 {
		ps.logger.Debug("no transactions to preconfirm")
		return nil
	}

	// Filter out already-gossiped transactions
	ps.mtx.Lock()
	newTxKeys := make([]types.TxKey, 0, len(allTxKeys))
	for _, key := range allTxKeys {
		if _, alreadyGossiped := ps.gossipedTxs[key]; !alreadyGossiped {
			newTxKeys = append(newTxKeys, key)
			ps.gossipedTxs[key] = struct{}{}
		}
	}
	ps.mtx.Unlock()

	if len(newTxKeys) == 0 {
		ps.logger.Debug("no new transactions to preconfirm")
		return nil
	}

	txHashes := make([][]byte, len(newTxKeys))
	for i, key := range newTxKeys {
		keyBytes := key[:]
		txHashes[i] = keyBytes
	}

	pubKey, err := ps.privVal.GetPubKey()
	if err != nil {
		return fmt.Errorf("failed to get public key: %w", err)
	}

	protoPubKey, err := encoding.PubKeyToProto(pubKey)
	if err != nil {
		return fmt.Errorf("failed to convert public key to proto: %w", err)
	}

	msg := &protomemcat.PreconfirmationMessage{
		PubKey:    protoPubKey,
		TxHashes:  txHashes,
		Timestamp: time.Now(),
	}

	signBytes, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal preconfirmation message: %w", err)
	}

	// Sign using SignRawBytes which works with remote validators and KMS
	// This adds consistent framing that we'll verify on the other side
	signature, err := ps.privVal.SignRawBytes(ps.chainID, "preconfirmation", signBytes)
	if err != nil {
		return fmt.Errorf("failed to sign preconfirmation message: %w", err)
	}

	// Set the signature
	msg.Signature = signature

	// Send to our own update channel for processing
	ps.SendUpdate(msg)

	// Broadcast to peers if callback is configured
	if broadcastMsg != nil {
		broadcastMsg(msg)
	}

	ps.logger.Debug("signed and broadcast preconfirmation", "num_txs", len(txHashes))
	return nil
}
