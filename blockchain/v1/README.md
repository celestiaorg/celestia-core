## Mempool Protocol Description

The p2p latyer of Celestia-core which is a fork of CometBFT, is comprised of channels and reactors. Peers form a network around the channels they support. Message propagation in a channel is dictated by the reactor associated with that channel. In other words, a reactor holds the protocol logic for that channel. One of such channels is the mempool channel, which is reffered to by `MempoolChannel` and holds a specific channel ID of `0x30`. The mempool reactor is responsible for the propagation of transactions in the network. It is worth noting that there is a 1-1 mapping between reactors and their
mounted channels. This means that the mempool reactor is the only reactor
that is mounted on the memppol channel ID  `0x30`. In this document, we describe the mempool reactor and its protocol. We also provide an anlysis around the traffic rate of the mempool protocol.

## Mempool Reactor
A node can receive a transaction in two ways:
Either by a user sending a transaction to the node, or by the node receiving a transaction from another node.
1. When a transaction is received:
2. its validity is checked, and if it is valid, it is added to the mempool. The transaction's height is set to the current block height.
3. If the transaction is received from another peer, the sending peer is
   marked so that the transaction is not sent to that peer again.
2. The transactions is added to the mempool. At this point there will be two concurrent processes:
   3. **Mempool life-cycle**:
      3. Transactions remain the mempool until a block is committed, at which point:
         4. the block transactions are removed from the mempool
         5. remaining transactions are re-checked for their TTL
            and are removed from the mempool if they have been expired. ([ref](https://github.
            com/celestiaorg/celestia-core/blob/367caa33ef5ab618ea357189e88044dbdbd17776
            /state/execution.go#L324)).
         6. remaining transactions are checked for their validity given the updated state. Any invalid transaction is removed.
   3. **Broadcast process**: For every peer and for every transaction in the mempool, the following takes place:
      1. The peer is sent a copy of the transaction if:
         3. peer is running
         4. supports the mempool channel ID.
         5. The max block height difference between the peer and the tx is one.
            Otherwise, there will be a waiting time to let the peer catches up.
      1. Each transaction is sent only once and the receving peer is marked
         as not to send that transaction again.

<!--- The first step in the transaction life-cycle is the validity check.
If all checks pass successfully, then the message goes throughout the following
steps. The transaction is broadcast to all the connected and running peers
who support the
mempool channel ID. The broadcast happens regardless of the connection type
of the peer, being inbound or outbound.
When a transaction is sent to a peer, the mempool marks that peer to not to
send that transaction again.
If the transaction is received from another peer, then the validity check
takes place. Also, the peer is marked to not to send that transaction again.
When a block is committed, the block transactions are removed from the
mempool and  remaining transactions are re-checked for their TTL
and are removed from the mempool if they have been expired. ([ref](https://github.
com/celestiaorg/celestia-core/blob/367caa33ef5ab618ea357189e88044dbdbd17776
/state/execution.go#L324)). Additionally, they are checked for their
validity given the updated state. Any invalid transaction is removed.
[//]: # (Is it possible that the marks are erased and the transaction is
sent again? for example when the mempool is full and then gets erased)
[//]: # (Is a transaction resent after Recheck: NO)
[?] Is there any cap on the number of transactions sent to another peer? No
[//]: # (Consider the case that one sends 1000 txs with size 1MB, filling up
the entire mempool size wise, in that case, we keep erasing the past
transactions. is it even possible? do we have a limit on the incoming
bandwidth? consider a mempool size of 1, and then make an example) --->


## Configurations
The total number of connected peers are limited by `max_num_inbound_peers`
or `max_num_outbound_peers` in the config.toml file. According to the
current default configs, the former is set to  `40` and the latter is set to
`10` (excluding persistent peers).

The max size of a transaction: `MaxTxBytes:   1024 * 1024, // 1MB`
The mempool can accomodate up to `Size` `5000` many transactions with total
size of `MaxTxBytes=1GB`.
Each transaction can stay in the mempool for `TTLDuration` and `TTLNumBlocks`
after its insertion to the mempool.

Each p2p connection is also subject to the following limits per channel:
```markdown

SendRate:                defaultSendRate,= int64(512000) // 500KB/s [?] How it
is enforced?
RecvRate:                defaultRecvRate, = int64(512000) // 500KB/
MaxPacketMsgPayloadSize: defaultMaxPacketMsgPayloadSize, // 1024
FlushThrottle:           defaultFlushThrottle,
PingInterval:            defaultPingInterval,
PongTimeout:             defaultPongTimeout,
```
ref https://github.com/celestiaorg/celestia-core/blob
/3f3b7cc57f5cfc5e846ce781a9a407920e54fb72/libs/flowrate/flowrate.go#L177
(rate in this function is the send or rec rate)


## Traffic Rate Analysis
At any connection, there can be at most 2 instances of the same transaction.

**test configuration:**
`d`: Node degree (total incoming and outgoing connections)
transaction rate: `TNR` total number of transactions per second submitted to the
network
transaction rate: `TSR` total number of transactions per second submitted to the
network
`C`: total number of connections in the network

We assume all the transactions comply with the trnasaction size limit as
specified in the mempool config.
We assume all the transactions are valid and are accepted by the mempool.

incoming traffic rate `itr`
outgoing traffic rate  `otr`
In the worst case scenario: a transaction is exchanged by the two ends of
connection simultaneously, contributing to both incoming and outgoing traffic.
In a network, with transaction rate `T` and a node with `d` degree, the
`itr` and `otr` are calculated as follows:
`itr = min(bw_req / d, d * T, channel_recv_rate)`
`otr = min(bw_req / d, d * T, channel_recv_rate)`

unique-transaction-rate `UTR` is the number of unique transactions per
second which should be `TNR`.

Desired network transaction throughput `network_tx_throughput` in bytes/sec
is capped by the `block_size` and `block_time` as follows:
`max_network_tx_throughput = block_size / block_time`

For a node, in order to be able to handle this throughput in the worst case,
the node will undertake the following traffic:
`itr = d * max_network_tx_throughput`
`otr = d * max_network_tx_throughput`

So the minimum bandwidth requirement for a node, just due to the mempool
operation is, `d * max_network_tx_throughput` for download and upload.

If we set the bw requirement to `bw_req`, then the `nextwork_tx_throughput`
is at most `bw_req / d` bytes/sec.
