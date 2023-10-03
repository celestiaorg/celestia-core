## Mempool Protocol Description

The Celestia-core's p2p layer, which is a fork of CometBFT, consists of channels and reactors. Peers establish connections within specific channels, effectively forming peer-to-peer groups (each channel represents such a group). The transmission of messages within a channel is governed by the associated reactor, essentially containing the protocol rules for that channel.

One notable channel is the mempool channel, identified as [`MempoolChannel`](https://github.com/celestiaorg/celestia-core/blob/3f3b7cc57f5cfc5e846ce781a9a407920e54fb72/mempool/mempool.go#L14) with a specific channel ID of `0x30`. The mempool reactor manages the dissemination of transactions across the network. It's important to highlight that there's a direct correspondence between reactors and the channels they are connected to. Consequently, the mempool reactor is the exclusive reactor linked to the mempool channel with ID `0x30`. This document will provide an in-depth overview of the mempool reactor and its protocol, including an analysis of the mempool protocol's traffic rate.

## Mempool Reactor
A node can receive a transaction through one of two pathways: either a user initiates the transaction directly to the node, or the node acquires a transaction from another peer. Upon receiving a transaction, the following steps occur:

1. The transaction's validity is assessed, and if it passes the validation criteria, it is added to the mempool. Furthermore, the transaction's height is set to match the current block height.
2. **Peer Tracking**: In the event that the transaction originates from another peer, the sending peer is marked to prevent redundant transmission of the same transaction.
   Subsequently, there are two concurrent processes underway:
3. **Mempool Life-cycle**:
    - Transactions that find their way into the mempool remain there until one of two conditions is met: either the mempool reaches its capacity limit or a new block is committed.

    - When a block is committed:
        - the transactions within that block are removed from the mempool.
        - The remaining transactions are subjected to two checks:
            - their Time-to-Live (TTL) is examined, and any transactions that have expired are promptly removed from the mempool ([reference](https://github.com/celestiaorg/celestia-core/blob/367caa33ef5ab618ea357189e88044dbdbd17776/state/execution.go#L324)).
            - Next, the remaining transactions are re-evaluated for validity against the updated state. Any transactions that are found to be invalid are removed from the mempool.
4. **Broadcast Process**:
   For each peer and for every transaction residing in the mempool, the following actions are taken:
    - A copy of the transaction is dispatched to that peer if the peer
        -  is online
        - supports the mempool channel ID
        - has a height difference of one (meaning it lags behind the transaction by a single block). If the height difference is greater, a waiting period is observed to allow the peer to catch up.
    - **Peer Tracking**: Each transaction is sent to a peer only once, and the recipient peer is marked to prevent the retransmission of the same transaction.

## Constraints and  Configurations
The relevant constraints and configurations for the mempool are as follows ([ref](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L758)):
- `Size`: This parameter specifies the total number of transactions that the mempool can hold, with a maximum value of `5000`.
-  `MaxTxBytes`: The `MaxTxBytes` parameter defines the maximum size of the mempool in bytes, with a limit of `1GB`  by default consensus configs but is later modified on the celestia-app side to `128 * 128 * 482 = 7897088 = 7897.088 KB = 7.897 MB`.
-  `TTLNumBlocks` and `TTLDuration` : These settings determine the number of blocks and time after which a transaction is removed from the mempool if it has not been included in a block. The default is set to zero, however, on [celestia-app side](https://github.com/celestiaorg/celestia-app/blob/0d70807442ba0545058d353b44f6f9a583d3e11d/app/default_overrides.go#L209) these values are over-written to `5` and `5*15 s`, respectively.
-  `MaxTxSize`: The `MaxTxSize` parameter specifies the maximum size of an individual transaction, which is set to `1MB`.

For each connection, the following limits apply per channel ID ([ref](https://github.com/celestiaorg/celestia-core/blob/3f3b7cc57f5cfc5e846ce781a9a407920e54fb72/libs/flowrate/flowrate.go#L177)):

-  `SendRate`: The `SendRate` parameter enforces a default average sending rate of `5120000 B= 5MB/s`. It ensures that data is sent at this maximum rate. This parameter does not seem to be overwritten by the celestia-app.
- `RecvRate`: The `RecvRate` parameter enforces a default average receiving rate of `5120000 B= 5MB/s`. It ensures that data is received at this maximum rate. This parameter does not seem to be overwritten by the celestia-app.
- `MaxPacketMsgPayloadSize`: The `MaxPacketMsgPayloadSize` parameter sets the maximum payload size for packet messages to `1024` bytes.

<!-- TODO: I am currently investigating the impact of send and rec rate in the total  traffic at each node and per connection -->

Peer related configs ([ref](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L524)) that would be relevant to the traffic analysis are as follows:
- `max_num_inbound_peers` and `max_num_outbound_peers`: These parameters indicate the total number of inbound and outbound peers, respectively. The default values are `40` for inbound peers and `10` for outbound peers (excluding persistent peers).

<!-- Depending on the state of this [PR](https://github.com/celestiaorg/celestia-app/pull/2390) we may have further constraints on the bandwidth. -->

## Traffic Rate Analysis
In the analysis provided below, we consider the knowledge of the following network parameters
- `d`: Node degree (total incoming and outgoing connections)
<!-- - transaction rate: `transaction_rate` total number of transactions per second submitted to the network -->
- `transaction_rate` which specifies that total size of transactions in bytes per second submitted to the network.
- `C`: total number of connections in the network

Transactions are assumed to comply with the transaction size, are valid and are accepted by the mempool.
We also assume all the peers are up and running.

### Traffic Rate Analysis for a Node
We distinguish between the incoming and outgoing traffic rate, and denote them by  `incoming_traffic_rate` and  `outgoing_traffic_rate`, respectively.
Worst case scenario: a transaction is exchanged by the two ends of
connection simultaneously, contributing to both incoming and outgoing traffic.
In a network, with transaction rate `transaction_rate` and a node with `d` degree, the traffic rates are calculated as follows:
`incoming_traffic_rate = d * transaction_rate`
`outgoing_traffic_rate = d * transaction_rate`

These rates are further constrained by the channel send and receive rates, and bandwidth constraint `bw_limit` if any.
`incoming_traffic_rate = min(bw_limit / d, d * transaction_rate, SendRate)`
`outgoing_traffic_rate = min(bw_limit / d, d * transaction_rate, RecRate)`

Best case scenario: a transaction is exchanged only once, contributing to either incoming or outgoing traffic.
In a network, with transaction rate `transaction_rate` and a node with `d` degree, the node's traffic rate is capped by:
`traffic_rate (=incoming_traffic_rate + outgoing_traffic_rate) = d * transaction_rate`



### Impact of mempool on other network aspects
- **Block size**: One immediate impact of mempool, is the size of mempool on the block size. Clearly, block size can not exceed the mempool size. In the current setting, the mempool size is at max `7.897 MB` meaning Celestia blocks can get as large as that (excluding block header).
- **Network throughput**:  TBC
- **Block Time**: TBC

