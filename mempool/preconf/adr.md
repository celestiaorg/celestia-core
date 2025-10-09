# ADR: Preconfirmations in the CAT Mempool

**Status**: Proposed

## Context

Users and applications require faster feedback on the likelihood of a transaction's inclusion in a block. The CAT (Content-Addressable Transaction) mempool is designed for efficient transaction dissemination. A "preconfirmation" mechanism built on top of the CAT mempool can provide a probabilistic guarantee that a transaction will be included in a subsequent block, based on the voting power of validators who have seen and acknowledged the transaction.

This ADR proposes a system for validators to gossip about the transactions they have in their CAT mempool, allowing clients to track the cumulative voting power that has "seen" a specific transaction before it is committed to a block.

## Decision

We will introduce a new preconfirmation system within the CAT mempool that leverages the existing P2P network and validator signing capabilities.

1.  **New Message**: A new `PreconfirmationMessage` will be created. Validators will sign a batch of transaction hashes from their mempool using their private validator key (`privval`).
2.  **P2P Propagation**: These messages will be gossiped on a new, dedicated P2P channel (`PreconfirmationChannel`) within the CAT mempool reactor.
3.  **State Management**: A new `PreconfirmationState` struct will be added to the CAT mempool to track seen transaction hashes and the corresponding voting power. This state will be updated asynchronously to minimize lock contention on the core mempool data structures.
4.  **Validator Set**: The `PreconfirmationState` will require access to the current validator set to map public keys to voting power. This will be provided by the consensus reactor.
5.  **Client-Facing Changes**: The `TxStatus` query will be updated to include the total voting power that has preconfirmed a transaction, giving users near real-time feedback.

## Consequences

### Positive

*   **Improved User Experience**: Users get faster, probabilistic feedback on their transactions.
*   **New Application Capabilities**: Enables applications that can act on transactions with a high degree of confidence before they are formally committed.
*   **Minimal Core Consensus Changes**: The mechanism is an overlay on the CAT mempool and does not alter the core consensus or block production rules.

### Negative

*   **Increased Network Traffic**: The new `PreconfirmationMessage` will add traffic to the P2P network, although this can be mitigated by batching hashes.
*   **Increased Complexity**: Adds a new component and state management logic to the CAT mempool, requiring careful implementation to avoid performance bottlenecks.
*   **No Hard Guarantees**: Preconfirmations are probabilistic and do not provide the same finality as block inclusion. This distinction must be clear to users.

---

# Specification

## 1. Overview

The preconfirmation system works as follows:

1.  Every 2 seconds, each validator gathers the hashes of transactions currently in its CAT mempool.
2.  The validator signs a message containing these hashes, its public key, and a timestamp using its `privval`.
3.  This signed `PreconfirmationMessage` is broadcast over a new `PreconfirmationChannel`.
4.  Peers receiving this message validate the signature and the public key against the current validator set.
5.  Upon successful validation, the receiving node updates its local `PreconfirmationState`. This state holds a mapping of transaction hashes to the cumulative voting power of validators that have acknowledged it.
6.  The `PreconfirmationState` is updated asynchronously via an internal channel to avoid blocking the CAT mempool's primary operations.
7.  When a user queries a transaction's status, the system combines the CAT mempool's knowledge with the `PreconfirmationState` to provide the total preconfirmation voting power.

## 2. New Components & State

### `mempool/cat/preconf/state.go`

A new struct `PreconfirmationState` will be created within the `cat` package to manage the preconfirmation data. It will be owned and initialized by the CAT `TxPool`.

```go
// PreconfirmationState manages the state of transaction preconfirmations.
type PreconfirmationState struct {
    mtx           sync.Mutex
    txs           map[types.TxKey]*TxPreconfirmation
    validatorSet  *types.ValidatorSet
    updateChan    chan types.PreconfirmationMessage
    logger        log.Logger
}

// TxPreconfirmation holds the set of validators that have preconfirmed a transaction.
type TxPreconfirmation struct {
    mtx             sync.Mutex
    seenValidators  map[string]struct{} // Key: Validator Public Key Address
    totalPower      int64
}
```

*   **`txs`**: A map from a transaction key to a `TxPreconfirmation` struct, which tracks which validators have seen the tx.
*   **`validatorSet`**: A pointer to the current validator set. This will be updated by the consensus reactor whenever the validator set changes. A callback mechanism will be implemented for the consensus reactor to push updates to the `PreconfirmationState`.
*   **`updateChan`**: A buffered channel to receive `PreconfirmationMessage`s from the CAT mempool reactor. A dedicated goroutine will process these messages to update the `txs` map, ensuring that mempool and RPC queries are not blocked.

## 3. New Messages

### `proto/tendermint/mempool/cat/preconf.proto`

A new Protobuf message will be defined for preconfirmations.

```protobuf
syntax = "proto3";
package tendermint.mempool.cat;

import "google/protobuf/timestamp.proto";
import "tendermint/crypto/keys.proto";

message PreconfirmationMessage {
  bytes signature = 1;
  tendermint.crypto.PublicKey pub_key = 2;
  repeated bytes tx_hashes = 3;
  google.protobuf.Timestamp timestamp = 4;
}
```

*   **`signature`**: The validator's signature over the marshaled `tx_hashes` and `timestamp`.
*   **`pub_key`**: The public key of the validator, used to verify the signature and map to voting power.
*   **`tx_hashes`**: A list of transaction hashes the validator has in its mempool.
*   **`timestamp`**: The time at which the message was created. This helps peers gauge the freshness of the information and can be used to discard old messages.

The signature will be created by the validator's `PrivValidator`. The signed payload will be the SHA256 hash of the protobuf-marshaled `PreconfirmationMessage` with the `signature` field omitted.

## 4. P2P Propagation

A new P2P channel, `PreconfirmationChannel`, will be added to the CAT reactor.

*   **Channel ID**: `0x31` (example, to be confirmed to avoid collision with `MempoolWantsChannel` and `MempoolDataChannel`)
*   **Message Type**: `PreconfirmationMessage`

The CAT mempool reactor will be responsible for handling messages on this channel.

## 5. Mempool Integration

### `mempool/cat/reactor.go`

*   The CAT reactor will subscribe to the `PreconfirmationChannel`.
*   Upon receiving a `PreconfirmationMessage`, the reactor will perform initial validation (e.g., message size limits) and then pass it to the `PreconfirmationState`'s update channel for asynchronous processing.
*   A ticker will be added to the CAT reactor. Every 2 seconds, it will trigger a function to:
    1. Get the list of transaction hashes from its local CAT mempool.
    2. Create a `PreconfirmationMessage`.
    3. Sign the message using the node's `PrivValidator`.
    4. Broadcast the message on the `PreconfirmationChannel`.

### `mempool/cat/preconf/state.go` (Processing Goroutine)

A goroutine started by `NewPreconfirmationState` will range over the `updateChan`:

1.  Receive a `PreconfirmationMessage`.
2.  Verify the validator's signature.
3.  Check if the `pub_key` corresponds to a validator in the current `validatorSet`. If not, discard.
4.  For each `tx_hash` in the message:
    *   Acquire a lock on the `PreconfirmationState`.
    *   Look up the `TxPreconfirmation` for the hash. If it doesn't exist, create it.
    *   Acquire a lock on the `TxPreconfirmation`.
    *   Add the validator's address to `seenValidators`. If it's a new addition, add the validator's voting power to `totalPower`.
    *   Release locks.

## 6. RPC & User Interface (`TxStatus`)

### `rpc/core/mempool.go`

The `Tx` RPC endpoint will be modified to include preconfirmation data. The `TxStatus` struct in `types/mempool.go` will be augmented.

```go
// in types/mempool.go
type TxStatus struct {
    Tx             Tx    `json:"tx"`
    TxKey          TxKey `json:"tx_key"`
    Height         int64 `json:"height"`
    // ... existing fields
    PreconfirmationVotingPower int64 `json:"preconfirmation_voting_power"`
    PreconfirmedProportion     float64 `json:"preconfirmed_proportion"`
}
```

When a client calls the `/tx` endpoint, the RPC method will:
1.  Fetch the transaction details from the CAT mempool as usual.
2.  Query the CAT mempool's `PreconfirmationState` to get the `totalPower` for that transaction hash.
3.  Query the `PreconfirmationState` for the total voting power of the current validator set.
4.  Populate the new fields in the `TxStatus` response.

## 7. Performance Considerations

*   **Asynchronous Updates**: The use of `updateChan` is critical. It decouples the P2P message handling from the state updates, preventing the CAT reactor from blocking while waiting for locks.
*   **Lock Granularity**: The design uses a lock on the main `PreconfirmationState` map and finer-grained locks on individual `TxPreconfirmation` structs. This reduces contention when different transactions are being updated simultaneously.
*   **Batching**: Validators send a single message with multiple hashes every 2 seconds. This is more efficient than sending one message per transaction. The 2-second interval is configurable and can be tuned based on network performance.
*   **Memory Management**: The `PreconfirmationState` needs a garbage collection mechanism. When a transaction is included in a block and removed from the CAT mempool, its corresponding entry in the `PreconfirmationState` should be pruned. This can be triggered by listening to `EventDataTx` events or via a periodic cleanup routine.

---

# Implementation Plan (Phased by PRs)

This feature will be developed in a series of pull requests to allow for incremental review and testing.

USE TABLE DRIVEN TESTS WHERE POSSIBLE

### PR 1: Core State and Initialization

*   **Goal**: Introduce the core `PreconfirmationState` struct and wire it into the CAT mempool's lifecycle, without implementing signing or networking.
*   **Key Changes**:
    *   Create `mempool/cat/preconf/state.go` containing the `PreconfirmationState` and `TxPreconfirmation` structs.
    *   Add a `PreconfirmationState` field to the CAT `TxPool` in `mempool/cat/pool.go`.
    *   In `node/setup.go`, instantiate `PreconfirmationState` during CAT mempool creation. This involves wiring in access to the `PrivValidator` and establishing a callback mechanism with the consensus reactor to receive validator set updates.
    *   When a new transaction is added to the CAT mempool, create a corresponding (empty) `TxPreconfirmation` entry for it in the `PreconfirmationState`.
*   **Testing Strategy**:
    *   **Unit Tests (`mempool/cat/preconf/state_test.go`)**:
        *   Test the initialization of `PreconfirmationState`.
        *   Mock `types.ValidatorSet` and call the update function directly to test validator set updates.
        *   Verify that adding/removing transactions from the state works as expected.
    *   **Integration Tests (`mempool/cat/pool_test.go`)**:
        *   Ensure `PreconfirmationState` is correctly initialized when the CAT `TxPool` is created.
        *   Verify that adding a transaction to the pool also creates a corresponding entry in the `PreconfirmationState`.

### PR 2: Message Definition and Signing

*   **Goal**: Define the `PreconfirmationMessage` and implement the logic for validators to periodically sign the hashes of transactions in their mempool.
*   **Key Changes**:
    *   Create the new directory `proto/tendermint/mempool/cat`.
    *   Define `PreconfirmationMessage` in `proto/tendermint/mempool/cat/preconf.proto`.
    *   Add a 2-second ticker to the CAT reactor (`mempool/cat/reactor.go`).
    *   Implement the function triggered by the ticker to gather transaction hashes, create the `PreconfirmationMessage`, and sign it using the node's `PrivValidator`.
*   **Testing Strategy**:
    *   **Unit Tests (`mempool/cat/reactor_test.go`)**:
        *   Use a mock `PrivValidator` to test the signing logic.
        *   Create a test reactor with a populated mempool and manually trigger the signing function.
        *   Assert that the `Sign` method on the mock `PrivValidator` is called with the correct, deterministically serialized payload.
        *   Verify the structure and content of the generated `PreconfirmationMessage`.

### PR 3: P2P Gossiping and Processing

*   **Goal**: Broadcast the signed messages over the P2P network and implement the logic for receiving nodes to process them.
*   **Key Changes**:
    *   In `node/setup.go`, add the new `PreconfirmationChannel` to the list of channels for the CAT reactor.
    *   In `mempool/cat/reactor.go`, implement the logic to broadcast the signed `PreconfirmationMessage` on the new channel.
    *   Implement the `Receive` handler in the CAT reactor for messages on the `PreconfirmationChannel`. This handler will perform basic validation and forward the message to the `PreconfirmationState`'s update channel.
    *   Implement the asynchronous processing goroutine in `mempool/cat/preconf/state.go`. This routine will read from the update channel, verify the message's signature, check the signer against the current validator set, and update the preconfirmation state for each transaction hash.
*   **Testing Strategy**:
    *   **Unit Tests (`mempool/cat/preconf/state_test.go`)**:
        *   Test the asynchronous processing logic by sending valid and invalid `PreconfirmationMessage`s to the update channel.
        *   Verify correct state changes for valid messages (voting power aggregation).
        *   Ensure invalid messages (bad signature, unknown validator) are discarded.
    *   **Integration Tests (`mempool/cat/reactor_test.go`)**:
        *   Set up an integration test with two connected CAT reactors.
        *   Have one peer broadcast a `PreconfirmationMessage`.
        *   Verify the second peer receives the message and correctly updates its internal `PreconfirmationState`.

### PR 4: RPC Endpoint and Client-Facing Changes

*   **Goal**: Expose the aggregated preconfirmation voting power to end-users via the RPC.
*   **Key Changes**:
    *   Add the `PreconfirmationVotingPower` and `PreconfirmedProportion` fields to the `TxStatus` struct in `types/mempool.go`.
    *   Modify the `Tx` RPC endpoint in `rpc/core/mempool.go` to query the preconfirmation data.
    *   The RPC handler will access the `PreconfirmationState` from the CAT mempool, calculate the voting power and proportion, and include it in the response.
*   **Testing Strategy**:
    *   **Unit Tests (`rpc/core/mempool_test.go`)**:
        *   Mock the CAT mempool and its `PreconfirmationState`.
        *   Set up a test case where a transaction has a known amount of preconfirmed voting power.
        *   Call the `Tx` RPC handler and assert that the JSON response contains the correct `preconfirmation_voting_power` and `preconfirmed_proportion` values.
    *   **E2E Tests (`test/e2e/`)**:
        *   Create a new end-to-end test scenario.
        *   In the test, send a transaction, wait for it to be preconfirmed by a majority of validators, and then call the `/tx` RPC endpoint.
        *   Verify that the response correctly reflects the preconfirmation status before the transaction is included in a block.
