TX flow in the mempool

A node can receive a transaction in two ways:
Either by a user sending a transaction to the node, or by the node receiving a transaction from another node.

The node has a mempool, in which it stores all the transactions it has received.
The only reactor at the p2p level that gets involved in sending and
receiving transactions is the mempool reactor.
This reactor is installed on a specific channel ID, referred to by
`MempoolChannel` with the byte value of  `0x30`.
It is worth noting that there is a 1-1 mapping between reactors and their
mounted channels. This means that the mempool reactor is the only reactor
that is mounted on channel ID  `0x30`.


The total number of connected peers are limited by `max_num_inbound_peers`
or `max_num_outbound_peers` in the config.toml file. According to the
current default configs, the former is set to  `40` and the latter is set to
`10` (excluding persistent peers).

[//]: # (We may want to extract the max number of persistent peers as well)


The first step in the transaction life-cycle is the validity check.
If all checks pass successfully, then the message goes throughout the following
steps. The transaction is broadcast to all the connected and running peers
who support the
mempool channel ID. The broadcast happens regardless of the connection type
of the peer, being inbound or outbound.
When a transaction is sent to a peer, the mempool marks that peer to not to
send that transaction again.

If the transaction is received from another peer, then the validity check
takes place. Also, the peer is marked to not to send that transaction again.

If a peer is one height behind the height at which the transaction is
received, then the tx is sent to that peer. Otherwise, the mempool waits
until the peer catches up.


When a block is committed, the block transactions are removed from the
mempool and  remaining transactions are re-checked for their TTL
and are
removed from the mempool if they have been expired. ([ref](https://github.
com/celestiaorg/celestia-core/blob/367caa33ef5ab618ea357189e88044dbdbd17776
/state/execution.go#L324)). Additionally, they are checked for their
validity given the updated state. Any invalid transaction is removed.

[//]: # (Is it possible that the marks are erased and the transaction is
sent again? for example when the mempool is full and then gets erased)
[//]: # (Is a transaction resent after Recheck: NO)

The mempool reactor operates independent of the other reactors.
So, the consensus round, block height, does not affect its logic.
OR it does, when it rechecks all the transactions after every block is
committed and state has changed.

[?] Is there any cap on the number of transactions sent to another peer? No

The max size of a transaction: `MaxTxBytes:   1024 * 1024, // 1MB`
The mempool can accomodate up to `Size` `5000` many transactions with total
size of `MaxTxBytes=1GB`.
Each transaction can stay in the mempool for `TTLDuration` and `TTLNumBlocks`
after its insertion to the mempool.

[//]: # (Consider the case that one sends 1000 txs with size 1MB, filling up
the entire mempool size wise, in that case, we keep erasing the past
transactions. is it even possible? do we have a limit on the incoming
bandwidth? consider a mempool size of 1, and then make an example)

At any connection, there can be at most 2 instances of the same transaction.

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
**test configuration:**
`d`: Node degree (total incoming and outgoing connections)
transaction rate: `TNR` total number of transactions per second submitted to the
network
transaction rate: `TSR` total number of transactions per second submitted to the
network
`C`: total number of connections in the network

incoming traffic rate `itr`
outgoing traffic rate  `otr`
In the worst case scenario: a transaction is exchanged by the two ends of
connection simultaneously, contributing to both incoming and outgoing traffic.
In a network, with transaction rate `T` and a node with `d` degree, the
`itr` and `otr` are calculated as follows:
`itr = d * T`
`otr = d * T`
unique-transaction-rate `UTR` is the number of unique transactions per
second which should be `TNR`.

Desired network transaction throughput `network_tx_throughput` in bytes/sec
is capped by the `block_size` and `block_time` as follows:
`max_network_tx_throughput = block_size / block_time`

For a node, in order to be able to handle this throughput in the worst case,
the node will undertake the following traffic:
`itr = d * max_network_tx_throughput`
`otr = d * max_network_tx_throughput`

So the total bandwidth requirement for a node, just due to the mempool
operation is, `d * max_network_tx_throughput` for download and upload.

