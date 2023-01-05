# Compact Blocks Specification

- 19.12.2022 | Initial specification (@cmwaters)

## Outline

This document specifies a block propagation protocol known as "compact blocks". It builds directly on top of the content addressable transaction pool and expands on a similar notion of referencing data through compact hashes. Given that the transaction pool abstraction works to broadcast transactions to all nodes and not just directly to the next proposer, this mechanism can be leveraged whereby the proposer, instead of rebroadcasting the same data, can instead broadcast meta data that can be used to rebuild the block. This boils a consensus engine down to what it really is: a serialization protocol that should only be concerned with inclusion and ordering of transactions.

## Purpose

The objective of compact blocks, or block propagation protocols in general, is to optimize how quickly a proposer can communicate a set of transactions to the set of validators that can thereby verify and vote on the aforementioned transactions.

## Protocol Details

### Assembly

The first part of the protocol pertains to the construction of blocks. Transactions are collected from the mempool and passed to the application via the ABCI method `PrepareProposal`. Here the application is able to select the inclusion and ordering of transactions. This also means the application can add transactions which haven't yet been broadcasted. For any transaction that is new, the transaction pool MUST broadcast the full transaction to all connected nodes. The content addressable protocol will ensure these messages are propagated to all other nodes in the network.

The proposer MUST then send an array of tags each representing one transaction such that the transaction pool is capable of parsing the tags, retrieving missing transactions and ultimately reconstructing the block for the application to validate.  

> NOTE: This will often result in the same block proto format which uses `repeated bytes` for both transactions or transaction hashes

> NOTE: The protocol is not concerned with the way that the compact blocks are propagated around the network. They may be sent in whole or in parts.

### Reconstruction

Each node that receives a compact block will then attempt to reconstruct the full block so that it can be validated by the application and voted on by consensus. The transaction tags in the compact block are passed to the transaction pool. For each missing transaction, the pool checks to see if any of the peers has that transaction and then submit a `WantTx` message. A node MAY send multiple `WantTx` requests simultaneously to different peers as a measure of redundancy but should be mindful to not needlessly flood the network. The proposal phase is bounded by a locally configured timeout. Should the missing transactions not be available either due to faults in the network or byzantine behaviour by the proposer or a cabal of peers, the node MUST eventually timeout and cast a NIL prevote. 

### Block transaction tracing

As an optimization, implementations MAY leverage the finite size of transactions in a block to use positions themselves in the slice as a means of communicating what transactions the node has or wants. This SHOULD take the following format:

```proto
message HasBlockTxs {
	int64 height
	int32 round
  bytes has_bit_array = 1;
}

message WantBlockTxs {
	int64 height
	int32 round
  bytes want_bit_array = 2;
}
```

Here, each bit corresponds to the index of a transaction in the proposed block. The proposed block itself is uniquely identified by the height and round. Upon receiving a "compact block", the node's transaction pool forms and gossips a `HasBlockTxs` whereby for each bit, a 1 signals that the node has the transaction and a 0 indicates it does not. A node may request multiple txs simultaneously to a peer using `WantBlockTxs`  whereby a 1 indicates that they want the tx and a 0 indicates they do not.

### Syncing

Compact blocks impacts how block sync functions. For the blocks that use the compact block protocol, the hashes for each transaction must be computed and then the hash of those hashes matches against the supplied data hash in the header. 



## Implementation

This section illustrates the details of a possible implementation of the above specification in the context of the celestia network. It is likely that this will be separated from any final version of the spec.

We begin with a validator who starts a round as the proposer. Consensus calls the block executor to create a proposal block in `CreateProposalBlock`. The block executor calls `ReapMaxBytesMaxGas` to get the initial proposed transactions and passes it to the application to modify. It then calls a new method: `func PublishCompactBlock(data [][]byte) [][]byte`. Note, that it is the responsibility of the transaction pool to be aware of the internals of transaction representation; Consensus remains mostly unaware. `PublishCompactBlock` does three things:

- Loops through all transactions and for any new transaction, adds it to the mempool and immediately broadcasts it to all connected peers.
- Forms a `HasBlockTxs` which are all 1's (indicating it has all the transaction it is about to propose)
- Returns the hashes of each transaction, maintaining order, to consensus to be included as `Block.Txs`

For now, we leave the propagation mechanism that breaks the block into 64KB parts as it is. Given 32 byte hashes and 100 validators, a block with 1000 transactions will now be rougly 42.8 kb (i.e. 1 part). 

Now, jumping to the receiver of each block, after reassembling the compact block, they will call `func ConstructCompactBlock(ctx contex.Context, compactData [][]byte) [][]byte` by the mempool. The mempool loops through the hashes and determines what values are missing. It then checks these values against it's record of which peers have published either `SeenTx` or `HasBlockTxs`. It picks up to n random peers (for built-in redundancy) for each missing transaction and broadcasts a `WantTx` tracking the request and re-requesting after timeouts.

At the same time, it marks what transaction it already has and broadcasts it's own `HasBlockTxs` message to all peers. The first implementation does not make any use of `WantBlockTxs`.

If the mempool is able to fully reconstruct the block, it returns those transactions back to consensus (which will pass them to the application in `ProcessProposal`). The context will have a timeout matching the `ProposalTimeout`. If more than `ProposalTimeout` in duration is exceeded, the context will cancel and consensus will immediately cast a NIL prevote. 

In the mempool, the hash of the PFB refers to both the PFB and the Blobs together. These are always broadcast together and so similarily, with the hash of just the PFBs, it should be possible to gather both the Blobs and the Transaction and split them afterwards so that the extended square can be built and the data availabilty roots can be calculated and compared with those supplied by the proposer. 

Compact blocks should be an abstraction only the transaction pool is aware about. Apart from one or two new methods in consensus, nothing else should be modified. Other software that interacts with the block format should be unaltered (such as blocksync) and blocks should be stored in full as they currently are. It's important when adding large features that they can be rolled back in the event of an error. It is possible to build-in backwards compatibility by extending the config files. A node with compact blocks enabled could still process a proposer using full blocks and nodes with compact blocks disabled would always vote nil for compact blocks. We could extend `BlockID` to include a `Compact` boolean field to indicate whether the block should be treated in it's compact form or not. Alternatively we could use a new consensus parameter to indicate which type of block is being propagated but this lacks the ability for social consensus to quickly step in in the case of an emergency. 



## Limitations and Future Work

It's important to consider the tradeoffs this protocol makes in comparison to other algorithms. In particular, the current block propagation protocol in Tendermint which broadcasts complete transactions (potentially broken down into several parts). As the name suggests, compact blocks are far smaller than regular blocks, requiring a fixed size 32 bytes per transaction. The reductions in bandwidth are greater the larger the size of the transactions. One could even imagine grouping transactions together into clusters for even greater gains. This leverages off the fact that the underlying transaction dissemination protocol is all-to-all (i.e. all nodes gossip all transactions to all other nodes) as opposed to all-to-one (all nodes gossip their transactions solely to the proposer of the next height). In the case of the latter, broadcasting a set of transactions based upon their hashes would have a catastrophic impact to latency as each node would need to request almost all of the transactions again. An all-to-all approach gives us a higher probability that the node has all the transactions referenced in the compact block and depending on the recency of the transaction and the topology of the network, only a small amount of transactions will need to be requested.  This however still means that these nodes will incur an extra round trip hit to their latency. In summary, the question stands: can a reduction in the size of the block offset the additional round trip to request missing transactions.

### Catchup

Another limitation to be wary of is the interplay between catchup and consensus. Nodes finalize at different points in time. Upon finalizing, each node calls `Update` to the mempool which removes all the transactions that have been committed. This is not a problem when the entire block is broadcasted but may impact nodes that are behind because now the transactions have been purged from the mempools of the peers they are connected with. Either the mempool needs to keep the transactions around for a little longer or the blockstore needs to be used to now gossip those transactions. The former should be preferred.

### Compact Votes

The first version of the protocol purely focuses on transactions. In Tendermint, the votes for the previous block are always included. Similarly to transactions, it is likely that peers already have these votes. The proposer could simply represent the inclusion set as a `BitArray` (like `HasBlockTxs`) and require validators to request the votes they are missing before prevoting on the proposed block.
