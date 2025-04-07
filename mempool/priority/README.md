# Priority Mempool

The Priority Mempool is a specialized transaction pool for Celestia Core (based on CometBFT) that manages pending transactions with a focus on transaction prioritization. This document provides an overview of how the priority mempool works, its key components, and transaction flows.

## Core Concepts

The Priority Mempool prioritizes transactions based on their assigned priority values. When selecting transactions for inclusion in a block or when evicting transactions due to size constraints, priority values are crucial:

- **Higher-priority** transactions are chosen **first** for block inclusion
- **Lower-priority** transactions are evicted **sooner** when mempool is full

Within the mempool, transactions are ordered by time of arrival for gossiping to the rest of the network.

## Key Components

### 1. `TxMempool`

The main mempool structure that handles transaction storage, validation, and lifecycle:

```ascii
┌─────────────────────────────────────────┐
│              TxMempool                  │
├─────────────────────────────────────────┤
│ ┌─────────┐   ┌───────────┐  ┌────────┐ │
│ │  Cache  │   │  txByKey  │  │  txs   │ │
│ └─────────┘   └───────────┘  └────────┘ │
│                                         │
│ ┌───────────┐  ┌─────────────────────┐  │
│ │txBySender │  │    evictedTxs       │  │
│ └───────────┘  └─────────────────────┘  │
└─────────────────────────────────────────┘
```

- **Cache**: Tracks seen transactions to prevent duplicates
- **txs**: Linked list of valid transactions that passed CheckTx
- **txByKey**: Map of transaction keys to list elements for fast lookups
- **txBySender**: Map of sender IDs to transactions for sender-based filtering
- **evictedTxs**: Cache of recently evicted transactions

### 2. `WrappedTx`

Wrapper around raw transactions with additional metadata:

```ascii
┌───────────────────────────────┐
│          WrappedTx            │
├───────────────────────────────┤
│ tx        : Raw transaction   │
│ hash      : Transaction hash  │
│ height    : Initial check     │
│ timestamp : Entry time        │
│                               │
│ gasWanted : Gas requirement   │
│ priority  : Priority value    │
│ sender    : Sender identifier │
│ peers     : Source peer IDs   │
└───────────────────────────────┘
```

### 3. `Reactor`

Handles peer-to-peer communication for transaction broadcasting:

```ascii
┌───────────────────────────────┐
│           Reactor             │
├───────────────────────────────┤
│ mempool     : TxMempool ref   │
│ ids         : Peer ID tracker │
│ config      : Config settings │
│ traceClient : Tracer instance │
└───────────────────────────────┘
```

## Transaction Flow

### External Transaction Flow

```ascii
┌──────────────┐   1. Submit Tx   ┌───────────────┐
│              │----------------->│               │
│    Client    │                  │     Node      │
│              │<-----------------│               │
└──────────────┘  2. Tx Response  └───────┬───────┘
                                          │
                                          │ 3. Gossip to peers
                                          ▼
                              ┌─────────────────────────┐
                              │        Network          │
                              │    (Other Validators)   │
                              └─────────────────────────┘
```

1. Client submits transaction to a node
2. Node validates and returns response
3. If valid, node gossips transaction to peers

### Internal Transaction Processing

```ascii
                  ┌────────────────────────────────────────────────────┐
                  │                     TxMempool                      │
                  │                                                    │
┌─────────┐       │  ┌──────────┐    ┌───────────┐    ┌────────────┐  │
│         │       │  │  1. Size │    │ 2. PreCheck│   │3. Cache Check│ │
│   Tx    │──────────▶  Check   │───▶│   Hook    │───▶│              │ │
│         │       │  │          │    │           │    │              │ │
└─────────┘       │  └──────────┘    └───────────┘    └──────┬───────┘ │
                  │                                           │         │
                  │                                           │         │
                  │                                           ▼         │
                  │  ┌────────────┐   ┌───────────┐    ┌─────────────┐ │
                  │  │ 6. Update  │   │5. PostCheck│   │4. Application│ │
                  │  │  Mempool   │◀──│   Hook     │◀──│  CheckTx     │ │
                  │  │            │   │            │   │              │ │
                  │  └─────┬──────┘   └───────────┘    └──────────────┘ │
                  │        │                                             │
                  └────────┼─────────────────────────────────────────────┘
                           │
                           │  Full Mempool? 
                           ▼
                  ┌────────────────┐
                  │  Eviction      │
                  │  Logic         │
                  └────────────────┘
```

1. **Size Check**: Reject if transaction exceeds the configured max size
2. **PreCheck Hook**: Run optional validation hook
3. **Cache Check**: Check if transaction is already in cache or mempool
4. **Application CheckTx**: Application checks transaction, assigns priority
5. **PostCheck Hook**: Run optional post-validation hook
6. **Update Mempool**: Add transaction to mempool, possibly evicting lower-priority transactions if full

### Eviction Process

```ascii
┌───────────────────────────────────────────────────────────┐
│                      Full Mempool                         │
│  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  │
│  │Tx 1│  │Tx 2│  │Tx 3│  │Tx 4│  │Tx 5│  │Tx 6│  │Tx 7│  │
│  │p=10│  │p=25│  │p=7 │  │p=15│  │p=3 │  │p=2 │  │p=30│  │
│  └────┘  └────┘  └────┘  └────┘  └────┘  └────┘  └────┘  │
└───────────────────────────────────────────────────────────┘
                            ▲
                            │ New transaction (p=20)
                            │
                ┌───────────────────────────┐
                │ Find lowest priority txs  │
                │ until space is available  │
                └───────────────────────────┘
                            │
                            ▼
┌───────────────────────────────────────────────────────────┐
│                   Updated Mempool                         │
│  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  │
│  │Tx 1│  │Tx 2│  │Tx 3│  │Tx 4│  │New │  │Tx 7│  │    │  │
│  │p=10│  │p=25│  │p=7 │  │p=15│  │p=20│  │p=30│  │    │  │
│  └────┘  └────┘  └────┘  └────┘  └────┘  └────┘  └────┘  │
└───────────────────────────────────────────────────────────┘
                          Tx 5 & Tx 6 evicted (lowest priority)
```

When the mempool is full and a new transaction arrives with higher priority than existing transactions:

1. Identify lowest-priority transactions
2. Evict them until sufficient space is available
3. Add the new transaction to the mempool

### Block Proposal

```ascii
┌───────────────────────────────┐
│         Mempool               │
│                               │
│ ┌────┐┌────┐┌────┐┌────┐┌────┐│
│ │p=30││p=25││p=20││p=15││p=10││
│ └────┘└────┘└────┘└────┘└────┘│
└─────────────┬─────────────────┘
              │
              │ ReapMaxBytesMaxGas()
              │ or ReapMaxTxs()
              ▼
┌───────────────────────────────┐
│      Proposed Block            │
│ ┌────┐┌────┐┌────┐             │
│ │p=30││p=25││p=20│             │
│ └────┘└────┘└────┘             │
└───────────────────────────────┘
```

During block proposal:

1. `ReapMaxBytesMaxGas()` or `ReapMaxTxs()` is called
2. Transactions are selected in priority order
3. Selected transactions are included in the proposed block

## TTL Mechanisms

The Priority Mempool supports two mechanisms for transaction expiration:

1. **Time-based TTL**: Transactions expire after a configured duration
2. **Block-height TTL**: Transactions expire after a specific number of blocks

```ascii
┌───────────────────────────────────────────────────────────┐
│                         Mempool                           │
│                                                           │
│  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  ┌────┐  │
│  │Tx 1│  │Tx 2│  │Tx 3│  │Tx 4│  │Tx 5│  │Tx 6│  │Tx 7│  │
│  │h=10│  │h=10│  │h=11│  │h=11│  │h=12│  │h=12│  │h=13│  │
│  └────┘  └────┘  └────┘  └────┘  └────┘  └────┘  └────┘  │
└───────────────────────────────────────────────────────────┘

                     Current height = 20
                     TTLNumBlocks = 9
                     
┌───────────────────────────────────────────────────────────┐
│                    Updated Mempool                        │
│                                                           │
│  ┌────┐  ┌────┐  ┌────┐  ┌────┐                           │
│  │Tx 5│  │Tx 6│  │Tx 7│  │    │                           │
│  │h=12│  │h=12│  │h=13│  │    │                           │
│  └────┘  └────┘  └────┘  └────┘                           │
└───────────────────────────────────────────────────────────┘

                    Tx 1-4 expired (height < 11)
```

## Configuration Options

The Priority Mempool can be configured with several options:

- **Size**: Maximum number of transactions
- **MaxTxBytes**: Maximum transaction size
- **CacheSize**: Size of the transaction cache
- **TTLDuration**: Time-based expiration duration
- **TTLNumBlocks**: Block-height-based expiration limit

## Conclusion

The Priority Mempool provides a sophisticated transaction management system that ensures higher-priority transactions are included in blocks and maintained in the mempool while lower-priority transactions are evicted when necessary. This design helps optimize block space usage and transaction processing efficiency.
