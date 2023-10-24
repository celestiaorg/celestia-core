# ADR 009: Content addressable transaction pool

## Changelog

- 2023-01-11: Initial Draft (@cmwaters)

## Context

One of the criterias of success for Celestia as a reliable data availability layer is the ability to handle large transactional throughput. A component that plays a significant role in this is the mempool. It's purpose is to receive transactions from clients and broadcast them to all other nodes, eventually reaching the next block proposer who includes it in their block. Given Celestia's aggregator-like role whereby larger transactions, i.e. blobs, are expected to dominate network traffic, a content-addressable algorithm, common in many other [peer-to-peer file sharing protocols](https://en.wikipedia.org/wiki/InterPlanetary_File_System), could be far more beneficial than the current transaction-flooding protocol that Tendermint currently uses.

This ADR describes the content addressable transaction protocol and through a comparative analysis with the existing gossip protocol, presents the case for it's adoption in Celestia.

## Decision

Use a content addressable transaction pool for disseminating transaction to nodes within the Celestia Network

## Detailed Design

The core idea is that each transaction can be referenced by a key, generated through a cryptographic hash function that reflects the content of the transaction. Nodes signal to one another which transactions they have via this key, and can request transactions they are missing through the key. This reduces the amount of duplicated transmission compared to a system which blindly sends received transactions to all other connected peers (as we will see in the consequences section).

Full details on the exact protocol can be found in the [spec](../../mempool/cat/spec.md). Here, the document focuses on the main deciding points around the architecture:

- It is assumed clients submit transactions to a single node in the network. Thus a node that receives a transaction through RPC will immediately broadcast it to all connected peers.
- The new messages: `SeenTx` and `WantTx`, are broadcast over a new mempool channel `byte(0x31)` for backwards compatibility and to distinguish priorities. Nodes running the other mempools will not receive these messages and will be able to operate normally. Similarly, the interfaces used by Tendermint are not modified in any way, thus a node operator can easily switch between mempool versions.
- Transaction gossiping takes priority over these "state" messages as to avoid situations where we receive a `SeenTx` and respond with a `WantTx` while the transaction is still queued in the nodes p2p buffer.
- The node only sends `SeenTx` to nodes that haven't yet seen the transaction, using jitter (with an upper bound of 100ms) to stagger when `SeenTx`s are broadcast to avoid messages being sent at once.
- `WantTx`s are sent to one peer at a time. A timeout is used to deem when a peer is unresponsive and the `WantTx` should be sent to another peer. This is currently set to 200ms (an estimation of network round trip time). It is not yet configurable but we may want to change that in the future.
- A channel has been added to allow the `TxPool` to feed validated txs to the `Reactor` to be sent to all other peers.

A series of new metrics have been added to monitor effectiveness:

- SuccessfulTxs: number of transactions committed in a block (to be used as a baseline)
- AlreadySeenTxs: transactions that are received more than once
- RequestedTxs: the number of initial requests for a transaction
- RerequestedTxs: the numer of follow up requests for a transaction. If this is high, it may indicate that the request timeout is too short.

The CAT pool has had numerous unit tests added. It has been tested in the local e2e networks and put under strain in large, geographically dispersed 100 node networks.

## Alternative Approaches

A few variations on the design were prototyped and tested. An early implementation experimented with just `SeenTx`s. All nodes would gossip `SeenTx` upon receiving a valid tx. Nodes would not relay received transactions to peers that had sent them a `SeenTx`. However, in many cases this would lead to a node sending a tx to a peer before it was able to receive the `SeenTx` that the node had just sent. Even with a higher priority, a large amount of duplication still occurred.

Another trick was tested which involved adding a `From` field to the `SeenTx`. Nodes receiving the `SeenTx` would use the `NodeID` in `From` to check if they were already connected to that peer and thus could expect a transaction from them soon instead of immediately issuing a `WantTx`. In large scale tests, this proved to be surprisingly less efficient. This might be because a `SeenTx` rarely arrives from another node before the initial sender has broadcast to everyone. It may also be because in the testnets, each node was only connected to 10 other nodes, decreasing the chance that the node was actually connected to the original sender. The `From` field also added an extra 40 bytes to the `SeenTx` message. In the chart below, this experiment is shown as CAT2.

## Status

Implemented

## Consequences

To validate its effectiveness, the protocol was benchmarked against existing mempool implementations. This was done under close-to-real network environments which used [testground](https://github.com/testground/testground) and the celestia-app binary (@ v0.11.0) to create 100 validator networks. The network would then be subjected to PFBs from 1100 light nodes at 4kb per transaction. The network followed 15 second blocktimes with a maximum block size of roughly 8MB (these were being filled). This was run for 10 minutes before being torn down. The collected data was aggregated across the 100 nodes and is as follows:

| Version | Average Bandwidth | Standard Deviation | Finalized Bandwidth |
|-----|-----|------|------|
| v0 | 982.66MB/s | 113.91MB/s | 11MB/s |
| v1 | 999.89MB/s | 133.24MB/s | 11MB/s |
| v2 (CAT) | 98.90MB/s | 18.95MB/s | 11MB/s |
| v2 (CAT2) | 110.28MB/s | 33.49MB/s | 11MB/s |

> Finalized bandwidth is the amount of bytes finalized by consensus per second whereas the other measurements are per node.

Rather than just expressing the difference in bytes, this can also be viewed by the factor of duplication (i.e. the amount of times a transaction is received by a node)

| Version | v0 | v1 | v2 (CAT) | v2 (CAT2) |
| --------|----|----|----------|-----------|
| Duplication | 17.61x | 17.21x | 1.75x | 1.85x |


This, of course, comes at the cost of additional message overhead and there comes a point where the transactions are small enough that the reduction in duplication doesn't outweigh the extra state messages.


### Positive

- Reduction in network bandwidth.
- Cross compatible and therefore easily reversible.

### Negative

- Extra network round trip when not directly receiving a transaction.
- Greater complexity than a simple flooding mechanism.

### Neutral

- Allows for compact blocks to be implemented as it depends on the push pull functionality.

## Ongoing work

This section describes further work that may be subsequently undertaken in this area. The first is transaction bundling. If a node is subject to a lot of transactions from clients, instead of sending them off immediately one-by-one, it may wait for a fixed period (~100ms) and bundle them all together. The set of transactions can now be represented as a single key. This increases the content to key ratio and thus improves the performance of the protocol.

An area of further exploration is the concept of neighborhoods. Variations of this idea are present in both [GossipSub](https://github.com/libp2p/specs/blob/master/pubsub/gossipsub/gossipsub-v1.0.md#gossipsub-the-gossiping-mesh-router) and Solana's Turbine. The concept entails shaping the network typology into many neighborhoods or sections where a node can be seen as strongly connected to nodes in their neighbourhood and weakly connected to peers in other neighborhoods. The idea behind a more structured topology is to make the broadcasting more directed.

Outside of protocol development, work can be done to more accurately measure the performance. Both protocols managed to sustain 15 second block times with mostly full blocks i.e. same output throughput. This indicates that the network was being artificially constrained. Either of these constraints need to be lifted (ideally max square size) so we are able to measure the underlying network speed.

## References

- [Content-addressable transaction pool spec](../../mempool/cat/spec.md)
