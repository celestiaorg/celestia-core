# Content Addressable Transaction Pool Specification

- 01.12.2022 | Initial specification (@cmwaters)
- 09.12.2022 | Add Push/Pull mechanics (@cmwaters)

### Outline

This document specifies the properties, design, and implementation of a content-addressable transaction pool (CAT). This protocol is intended as an alternative to the FIFO and Priority mempools currently built-in to the Tendermint consensus protocol. The term content-addressable here indicates that each transaction is identified by a smaller, unique tag (in this case a sha256 hash). These tags are broadcast among the transactions as a means of more compactly indicating which peers have which transactions. Tracking what each peer has aims at reducing the amount of duplication. In a network without content tracking, a peer may receive as many duplicate transactions as peers connected to. The tradeoff here, therefore, is that the transactions are significantly larger than the tag such that the sum of the data saved sending what would be duplicated transactions is larger than the sum of sending each peer a tag.

### Purpose

The objective of such a protocol is to transport transactions from the author (usually a client) to a proposed block, optimizing both latency and throughput i.e., how quickly can a transaction be proposed (and committed) and how many transactions can be transported into a block at once.

Typically the mempool serves to receive inbound transactions via an RPC endpoint, gossip them to all nodes in the network (regardless of whether they are capable of proposing a block or not), and stage groups of transactions to both consensus and the application to be included in a block.

### Assumptions

The following are assumptions inherited from existing Tendermint mempool protocols:

- `CheckTx` should be seen as a simple gatekeeper to what transactions enter the pool to be gossiped and staged. It is non-deterministic: one node may reject a transaction that another node keeps.
- Applications implementing `CheckTx` are responsible for replay protection (i.e., the same transaction being present in multiple blocks). The mempool ensures that within the same block, no duplicate transactions can exist.
- The underlying p2p layer guarantees eventually reliable broadcast. A transaction needs only be sent once to eventually reach the target peer.

### Messages

The CAT protocol extends on the existing mempool implementations by introducing two new protobuf messages:

```protobuf
message SeenTx {
  bytes tx_key = 1;
  uint64 sequence = 2;
  bytes signer = 3;
}

message WantTx {
  bytes tx_key = 1;
}
```

Both `SeenTx` and `WantTx` contain the sha256 hash of the raw transaction bytes. `SeenTx` additionally carries the expected account `sequence` and the `signer` bytes derived from the application response so receiving peers can maintain per-signer ordering when deciding whether to request the transaction. The byte slice of the `tx_key` MUST have a length of 32. A `sequence` of `0` or an empty `signer` indicates that the information was unavailable and MUST NOT be treated as authoritative.

Both messages are sent across a new channel with the ID: `byte(0x31)`. This enables cross-compatibility as discussed in greater detail below.

> **Note:**
> The term `SeenTx` is used over the more common `HasTx` because the transaction pool contains sophisticated eviction logic. TTL's, higher priority transactions and reCheckTx may mean that a transaction pool *had* a transaction but does not have it anymore. Semantically it's more appropriate to use `SeenTx` to imply not the presence of a transaction but that the node has seen it and dealt with it accordingly.

### Outbound logic

A node in the protocol has two distinct modes: "broadcast" and "request/response". When a node receives a transaction via RPC (or specifically through `CheckTx`), it assumed that it is the only recipient from that client and thus will immediately send that transaction, after validation, to all connected peers. Afterward, only "request/response" is used to disseminate that transaction to everyone else.

> **Note:**
> Given that one can configure a mempool to switch off broadcast, there are no guarantees when a client submits a transaction via RPC and no error is returned that it will find its way into a proposer's transaction pool.

A `SeenTx` is broadcasted to a bounded set of "sticky" peers upon receiving a "new" transaction from a peer. Implementations currently cap this fanout at fifteen peers. Sticky peers are selected deterministically per signer with rendezvous (highest-random-weight) hashing: each connected peer is scored by hashing the tuple `(namespace="cat/sticky/v1", local salt, signer, peerID)` with SHA-256, taking the highest 64-bit value as the top-ranked peers. The local salt is derived from the node's peer ID (or another operator-provided value) so that each node chooses a consistent but distinct subset of peers per signer. This keeps repeated gossip for the same signer on the same connections and reduces redundant duplicates while still ensuring coverage. The transaction pool does not need to track every unique inbound transaction, therefore "new" is identified as:

- The node does not currently have the transaction
- The node did not recently reject the transaction or has recently seen the same transaction committed (subject to the size of the cache)
- The node did not recently evict the transaction (subject to the size of the cache)

Given these criteria, it is feasible, yet unlikely that a node receives two `SeenTx` messages from the same peer for the same transaction.

A `SeenTx` MAY be sent for each transaction currently in the transaction pool when a connection with a peer is first established. The same sticky peer selection is applied so that each signer continues to map to a stable subset of peers. This acts as a mechanism for syncing pool state across peers.

The `SeenTx` message MUST only be broadcasted after validation and storage. Although it is possible that a node later drops a transaction under load shedding, a `SeenTx` should give as strong guarantees as possible that the node can be relied upon by others that don't yet have the transaction to obtain it.

> **Note:**
> Inbound transactions submitted via the RPC do not trigger a `SeenTx` message as it is assumed that the node is the first to see the transaction and by gossiping it to others it is implied that the node has seen the transaction.

A `WantTx` message is always sent point to point and never broadcasted. A `WantTx` MUST only be sent after receiving a `SeenTx` message from that peer. There is one exception which is that a `WantTx` MAY also be sent by a node after receiving an identical `WantTx` message from a peer that had previously received the nodes `SeenTx` but which after the lapse in time, did no longer exist in the nodes transaction pool. This provides an optional synchronous method for communicating that a node no longer has a transaction rather than relying on the defaulted asynchronous approach, which is to wait for a period of time and try again with a new peer.

`WantTx` must be tracked. A node SHOULD not send multiple `WantTx`s to multiple peers for the same transaction at once but wait for a period that matches the expected network latency before rerequesting the transaction to another peer.

### Inbound logic

Transaction pools are solely run in-memory; thus when a node stops, all transactions are discarded. To avoid the scenario where a node restarts and does not receive transactions because other nodes recorded a `SeenTx` message from their previous run, each transaction pool should track peer state based **per connection** and not per `NodeID`.

Upon receiving a `Txs` message:

- Check whether it is in response to a request or simply an unsolicited broadcast
- Validate the tx against current resources and the applications `CheckTx`
- If rejected or evicted, mark accordingly
- If successful, send a `SeenTx` message to the selected sticky peers excluding the original sender so that only a bounded subset relays the new transaction.

Upon receiving a `SeenTx` message:

- It should mark the peer as having seen the message.
- If the node has recently rejected that transaction, it SHOULD ignore the message.
- If the node already has the transaction, it SHOULD ignore the message.
- If the node does not have the transaction but recently evicted it, it MAY choose to rerequest the transaction if it has adequate resources now to process it.
- If the node has not seen the transaction or does not have any pending requests for that transaction, it MUST evaluate the provided `(signer, sequence)` pair before requesting it:
    - If there is already a transaction from that signer in the local mempool, the node uses the locally tracked sequences to check whether the new transaction is the next expected sequence. When the sequence is greater than expected, the node defers the request and queues the `SeenTx` until the expected sequence is available.
    - If there is no transaction from that signer in the local mempool, the node queries the application mempool state for the next sequence before issuing a `WantTx`. This avoids requesting a transaction before the prerequisite sequence is present.
    - If the expected sequence is greater than the advertised sequence, the node SHOULD mark the transaction as stale and drop the queued `SeenTx`.
- Once the expected sequence matches the advertised sequence, the node requests the transaction from the peer with a `WantTx`. This ordering enables clients to submit sequential transactions without waiting for previous ones to be committed.

The "next expected sequence" is the first gap in the locally tracked contiguous sequence numbers for that signer: if the mempool has seen sequences `N, N+1, …, M`, then the expectation is `M+1`, or the lowest missing value if a gap exists. When no local transactions exist (or a gap remains unfilled), the node resolves the expectation by querying the application through `QuerySequence`.

Upon receiving a `WantTx` message:

- If it has the transaction, it MUST respond with a `Txs` message containing that transaction.
- If it does not have the transaction, it MAY respond with an identical `WantTx` or rely on the timeout of the peer that requested the transaction to eventually ask another peer.

### Compatibility

CAT has Go API compatibility with the existing two mempool implementations. It implements both the `Reactor` interface required by Tendermint's P2P layer and the `Mempool` interface used by `consensus` and `rpc`. CAT is currently network compatible with existing implementations (by using another channel), but the protocol is unaware that it is communicating with a different mempool and that `SeenTx` and `WantTx` messages aren't reaching those peers thus it is recommended that the entire network use CAT.
