# ADR 011: Sequence and priority ordering CAT mempool

## Changelog

- 2025-09-04: Initial draft

## Status

Proposed

## Context

The CAT mempool previously ordered transactions strictly based on priority (fee / gas). This is incongruent with the replay protection mechanism which depends on the order of transactions by a signer having a monotonically increasing sequence number. This becomes more apparent with multiple transactions by the same signer in the same block.

We need a mempool ordering algorithm that respects per-signer sequencing constraints while still maximising revenue through choosing the highest paid tranasctions (per byte or gas used).

## Decision

Group transactions by signer into signer-bundled sets ("tx sets") and order based on average weighted priority per set. Within tx set order transactions in ascending sequence order. Block building and recheckTx iterates through transactions in those order. This will preserve sequence ordering per signer while maximising tx fees per block.

Key aspects:

- Signer-bundled sets: A `txSet` holds transactions from a single signer. The set tracks:
    - `txs`: maintained in ascending sequence order; ties broken by earlier arrival timestamp.
    - `aggregatedPriority`: gas-weighted average of member transaction priorities, floored to `int64`.
    - `bytes`, `firstTimestamp`, and running accumulators `totalGasWanted` and `weightedPrioritySum`.
- Global ordering of sets: `orderedTxSets` is kept sorted by `(aggregatedPriority desc, firstTimestamp asc)`. This preserves FIFO between sets of equal aggregated priority.
- Within-set ordering: Transactions are iterated in ascending sequence, guaranteeing that a later sequence from the same signer is never submitted before an earlier one.
- Eviction policy when full: Compute the new set aggregated priority if the incoming transaction were added (`aggregatedPriorityAfterAdd`). Identify victim sets with aggregated priority strictly below the incoming set. Evict transactions from victim sets starting from the end (highest sequence numbers first) until enough bytes are freed. If insufficient bytes can be freed, reject the incoming transaction.
- TTL purge: Expiration by height/time removes transactions from sets and reorders or removes empty sets accordingly.

## Detailed Design

- Data structures (in `mempool/cat/store.go`):
    - `txSet`:
        - `signerKey []byte` and `signer []byte` labels
        - `txs []*wrappedTx` kept in ascending `sequence` order; equal sequences ordered by earlier `timestamp`.
        - `aggregatedPriority int64`
        - `bytes int64`
        - `firstTimestamp time.Time`
        - `totalGasWanted int64`, `weightedPrioritySum int64`
    - `store`:
        - `setsBySigner map[string]*txSet`
        - `orderedTxSets []*txSet` (sorted by weighted average priority)
        - transaction index, byte accounting, reservation tracking

- Aggregation:
    - On insert: update aggregated priority `totalGasWanted += gasWanted`, `weightedPrioritySum += priority * gasWanted`, then `aggregatedPriority = weightedPrioritySum / totalGasWanted` (integer division). Aggregated priority is relative to the amount of gasWanted per transaction. Transactions with more gasWanted will have larger weighting. Alternatively we could have used transaction size.
    - On remove: reverse the updates and recompute `aggregatedPriority` (or zero if empty).

- Ordering:
    - Global: `orderedTxSets` uses binary search insertion to maintain ordering by `(aggregatedPriority desc, firstTimestamp asc)`.
    - Intra-set: `addTxToSet` uses binary search by `(sequence asc, timestamp asc)`.
    - Iteration: block production reaping and recheckTx iterates sets in `orderedTxSets` order and then iterates `txs` per set.

- Eviction:
    - When full, remove lowest txSets starting from the latest sequence and moving towards an earlier sequence

- TTL purge:
    - TTL will remove an entire transaction set (i.e. everything from a specific signer) since all following transactions would also become invalidated

### Invariants and Guarantees

- Within a signer, transactions are never emitted out of ascending sequence order.
- Across signers, ordering is by set weighted average priority (desc), with earlier `firstTimestamp` breaking ties (FIFO between equal-priority sets).
- Weighted average priority updates are consistent with gas-weighted means as transactions are added/removed.
- Eviction never removes lower sequence numbers in a set before removing higher ones, preserving the ability to submit remaining sequences correctly.

### API and Internal Changes

- Internal store API:
    - Added: `getOrderedTxs()` returns a copy of all transactions in correct emitted order.
    - Added: `aggregatedPriorityAfterAdd(*wrappedTx) int64` to predict set priority with a candidate tx.
    - Added/Renamed: `getTxSetsBelowPriority(priority int64) ([]*txSet, int64)` returns sets with aggregated priority below the threshold and their cumulative bytes. Used for evicting lower paying transactions.
    - Removed legacy per-tx ordered list APIs (`deleteOrderedTx`, etc.).

- TxPool eviction path now operates on signer sets using aggregated priority and evicts from the tail of victim sets.

External mempool interface (CometBFT `mempool.Mempool`) remains unchanged.

### Increasing fees under congestion

Rather than replace by fee, users can improve the likeness of their transaction landing by increasing the gas price of later transactions, thus raising the aggregated priority. Given sequence awareness, a replace by fee protocol may easily be supported in the future.

## Testing

Unit tests were added/updated in `mempool/cat`:

- Priority:
    - `TestAggregatedPriorityWeightedByGas`
    - `TestAggregatedPriorityAfterAdd`
- Ordering:
    - `TestIntraSetOrderingBySequenceThenTimestamp`
    - `TestInterSetOrderingByAggregatedPriorityAndTimestamp`
- Eviction and reaping semantics validated via `TestTxPool_Eviction` and existing reaping tests, adjusted to set-based eviction.
- TTL purging validated to keep ordering and aggregation consistent after removals.

## Backwards Compatibility

- External RPC and mempool interfaces remain unchanged.
- No changes to ABCI interface (although signer and sequence need to be populated in order for this to work)
- Internal store test helpers and function names changed (e.g., `getTxSetsBelowPriority`); tests were updated accordingly.


