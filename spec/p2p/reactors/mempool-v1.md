## Mempool Reactor Overview

The Celestia-core's p2p layer, which is a fork of CometBFT, consists of channels and reactors. Peers establish connections within specific channels, effectively forming peer-to-peer groups (each channel represents such a group). The transmission of messages within a channel is governed by the associated reactor, essentially containing the protocol rules for that channel.

One notable channel is the mempool channel, identified as [`MempoolChannel`](https://github.com/celestiaorg/celestia-core/blob/3f3b7cc57f5cfc5e846ce781a9a407920e54fb72/mempool/mempool.go#L14) with a specific channel ID of [`0x30`](https://github.com/celestiaorg/celestia-core/blob/3f3b7cc57f5cfc5e846ce781a9a407920e54fb72/mempool/mempool.go#L14). The mempool reactor manages the dissemination of transactions across the network. It's important to highlight that there's a direct correspondence between reactors and the channels they are connected to. Consequently, the mempool reactor is the exclusive reactor linked to the mempool channel with ID `0x30`. This document will provide an overview of the protocol implemented by the mempool v1 reactor along with its traffic analysis.

## Transaction Flow and Lifecycle in a Node
A node can receive a transaction through one of two pathways: either a user initiates the transaction directly to the node, or the node acquires a transaction from another peer. Upon receiving a transaction, the following steps occur:

1. The transaction's validity is assessed, and if it passes the validation criteria, it is added to the mempool. Furthermore, the transaction's height is set to match the current block height.
2. **Peer Tracking**: In the event that the transaction originates from another peer, the sending peer is marked to prevent redundant transmission of the same transaction.
   Subsequently, there are two concurrent processes underway:
3. **Mempool Life-cycle**:
    - Transactions that find their way into the mempool remain there until one of two conditions is met: either the mempool reaches its capacity limit or a new block is committed.

    - When a [block is committed](https://github.com/celestiaorg/celestia-core/blob/367caa33ef5ab618ea357189e88044dbdbd17776/state/execution.go#L324):
        - the transactions within that block that are successfully delivered to the app are removed from the mempool ([ref](https://github.com/celestiaorg/celestia-core/blob/993c1228977f206c80cb0f87ac1d4f002826e904/mempool/v1/mempool.go#L418)). They are also placed in the mempool cache ([ref](https://github.com/celestiaorg/celestia-core/blob/993c1228977f206c80cb0f87ac1d4f002826e904/mempool/v1/mempool.go#L411-L412)).
        - The remaining transactions are subjected to two checks:
            - their Time-to-Live (TTL) is examined ([ref](https://github.com/celestiaorg/celestia-core/blob/993c1228977f206c80cb0f87ac1d4f002826e904/mempool/v1/mempool.go#L421)), and any transactions that have expired are promptly removed from the mempool ([ref](https://github.com/celestiaorg/celestia-core/blob/993c1228977f206c80cb0f87ac1d4f002826e904/mempool/v1/mempool.go#L743)).
            - Next, the remaining transactions are re-evaluated for validity against the updated state ([ref](https://github.com/celestiaorg/celestia-core/blob/993c1228977f206c80cb0f87ac1d4f002826e904/mempool/v1/mempool.go#L429-L430)) duo to the mempool [`recheck` config](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L708) that is set to `true` ([ref](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L761)). Any transactions that are found to be invalid are removed from the mempool.
4. **Broadcast Process**:
   For each peer and for every transaction residing in the mempool, the following actions are taken ([ref](https://github.com/celestiaorg/celestia-core/blob/64cd9ab7c67c945d755fb4fbd5afb2d352874eea/mempool/v1/reactor.go#L244)):
    - A copy of the transaction is dispatched to that peer if the peer
        -  is online
        - supports the mempool channel ID ([ref](https://github.com/celestiaorg/celestia-core/blob/ad660fee8f186d6f7e5e567ea23ea813f5038d90/p2p/peer.go#L319))
        - has a height difference of one (meaning it lags behind the transaction by a single block). If the height difference is greater, a waiting period is observed to allow the peer to catch up ([ref](https://github.com/celestiaorg/celestia-core/blob/64cd9ab7c67c945d755fb4fbd5afb2d352874eea/mempool/v1/reactor.go#L286-L289)).
    - **Peer Tracking**: Each transaction is sent to a peer only once, and the recipient peer is marked to prevent the retransmission of the same transaction ([ref](https://github.com/celestiaorg/celestia-core/blob/64cd9ab7c67c945d755fb4fbd5afb2d352874eea/mempool/v1/reactor.go#L304)).

## Constraints and  Configurations
The relevant constraints and configurations for the mempool are as follows ([ref](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L758)):
- `Size`: This parameter specifies the total number of transactions that the mempool can hold, with a maximum value of `5000`.
-  `MaxTxsBytes`: The `MaxTxsBytes` parameter defines the maximum size of the mempool in bytes, with a limit of `1GB`  by default.
- `MaxTxBytes`: The `MaxTxBytes` parameter specifies the maximum size of an individual transaction, which is set to `1MB`.
-  `TTLNumBlocks` and `TTLDuration` : These settings determine the number of blocks and time after which a transaction is removed from the mempool if it has not been included in a block. The default is set to zero, however, on [celestia-app side](https://github.com/celestiaorg/celestia-app/blob/ccfb3e5e87d05d75a92ad85ab199d4f0c4879a0a/app/default_overrides.go#L221-L222) these values are over-written to `5` and `5*15 s`, respectively.

For each peer to peer connection, the following limits apply (on the aggregate traffic rate of all the channels) ([ref](https://github.com/celestiaorg/celestia-core/blob/3f3b7cc57f5cfc5e846ce781a9a407920e54fb72/libs/flowrate/flowrate.go#L177)):

-  `SendRate`: The `SendRate` parameter enforces a default average sending rate of [`5120000 B= 5MB/s`](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L615). It ensures that data is sent at this maximum rate. This parameter does not seem to be overwritten by the celestia-app.
- `RecvRate`: The `RecvRate` parameter enforces a default average receiving rate of [`5120000 B= 5MB/s`](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L616). It ensures that data is received at this maximum rate. This parameter does not seem to be overwritten by the celestia-app.
- `MaxPacketMsgPayloadSize`: The `MaxPacketMsgPayloadSize` parameter sets the maximum payload size for packet messages to `1024` bytes.

<!-- TODO: I am currently investigating the impact of send and rec rate in the total  traffic at each node and per connection. It looks like that this is the average rate, but not necessarily a hard limit i.e., the rate may exceed this value but then the excess is amortized over the next period  -->
<!-- Depending on the state of this [PR](https://github.com/celestiaorg/celestia-app/pull/2390) we may have further constraints on the bandwidth. -->

P2P configs ([ref](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L524)) that would be relevant to the traffic analysis are as follows:
- [`max_num_inbound_peers` and `max_num_outbound_peers`](https://github.com/celestiaorg/celestia-core/blob/37f950717381e8d8f6393437624652693e4775b8/config/config.go#L604-L605): These parameters indicate the total number of inbound and outbound peers, respectively. The default values are `40` for inbound peers and `10` for outbound peers ([excluding persistent peers](https://github.com/celestiaorg/celestia-core/blob/2f93fc823f17c36c7090f84694880c85d3244764/config/config.go#L553-L554)).
<!-- The allocation of max_num_inbound_peers and max_num_outbound_peers across various connection types requires clarification. For instance, whether max_num_inbound_peers includes both unconditional peers and persistent peers or not. -->

## Traffic Rate Analysis
In the analysis provided below, we consider the knowledge of the following network parameters
- `d`: Node degree (total incoming and outgoing connections)
<!-- - transaction rate: `transaction_rate` total number of transactions per second submitted to the network -->
- `transaction_rate` which specifies that total size of transactions in bytes per second submitted to the network.
- `C`: total number of connections in the network

Transactions are assumed to comply with the transaction size, are valid and are accepted by the mempool. We also assume all the peers are up and running.

### Traffic Rate Analysis for a Node
We distinguish between the incoming and outgoing traffic rate, and denote them by  `incoming_traffic_rate` and  `outgoing_traffic_rate`, respectively.
- **Worst case scenario**: a transaction is exchanged by the two ends of
connection simultaneously, contributing to both incoming and outgoing traffic.
In a network, with transaction rate `transaction_rate` and a node with `d` degree, the traffic rates are calculated as follows:
`incoming_traffic_rate = d * transaction_rate`
`outgoing_traffic_rate = d * transaction_rate`

These max rates are further constrained by the `SendRate` and `RecRate`.
`incoming_traffic_rate = d * min(transaction_rate, SendRate)`
`outgoing_traffic_rate = d * min(transaction_rate, RecRate)`

- **Best case scenario**: a transaction is exchanged only once, contributing to either incoming or outgoing traffic. This is because both ends of the connection keep track of the transactions they have seen on a connection (whether via sending or receiving). If one peer sends a transaction before the other, they both mark it as sent/received, ensuring they do not redundantly transmit it to each other.
In a network, with transaction rate `transaction_rate` and a node with  degree of `d`, the node's traffic rate in best case would be:
`traffic_rate (=incoming_traffic_rate + outgoing_traffic_rate) = d * transaction_rate`

We can draw the following conclusions (to be extended and verified):
- With a known given transaction rate `transaction_rate`, a node's (in + out) traffic rate should range from `d * transaction_rate` to `2 * d * transaction_rate`.
- To handle a particular `transaction_rate` (network throughput), the node's `SendRate` and `RecRate` should be at least `transaction_rate` to handle the worst case scenario (this is only to undertake the load incurred by the mempool reactor).


