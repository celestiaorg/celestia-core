# P2P Channel Values

This document lists all the P2P channel identifiers used by the various reactors in celestia-core.

## Channel Overview

| Channel Name | Hex Value | Decimal Value | Reactor | File | Description |
|--------------|-----------|---------------|---------|------|-------------|
| PexChannel | 0x00 | 0 | PEX | `p2p/pex/pex_reactor.go:21` | Peer exchange protocol |
| StateChannel | 0x20 | 32 | Consensus | `consensus/reactor.go:31` | Consensus state messages |
| DataChannel | 0x21 | 33 | Consensus | `consensus/reactor.go:32` | Consensus data messages |
| VoteChannel | 0x22 | 34 | Consensus | `consensus/reactor.go:33` | Vote messages |
| VoteSetBitsChannel | 0x23 | 35 | Consensus | `consensus/reactor.go:34` | Vote set bits messages |
| MempoolChannel | 0x30 | 48 | Mempool | `mempool/mempool.go:13` | Basic mempool channel |
| MempoolDataChannel | 0x31 | 49 | Mempool CAT | `mempool/cat/reactor.go:27` | CAT mempool data (SeenTx and blob messages) |
| MempoolWantsChannel | 0x32 | 50 | Mempool CAT | `mempool/cat/reactor.go:30` | CAT mempool wants messages |
| EvidenceChannel | 0x38 | 56 | Evidence | `evidence/reactor.go:17` | Evidence messages |
| BlocksyncChannel | 0x40 | 64 | Blocksync | `blocksync/reactor.go:22` | Block synchronization |
| DataChannel | 0x50 | 80 | Propagation | `consensus/propagation/reactor.go:33` | Propagation data messages |
| WantChannel | 0x51 | 81 | Propagation | `consensus/propagation/reactor.go:36` | Propagation want messages |
| SnapshotChannel | 0x60 | 96 | State Sync | `statesync/reactor.go:21` | State sync snapshot messages |
| ChunkChannel | 0x61 | 97 | State Sync | `statesync/reactor.go:23` | State sync chunk messages |

## Notes

- Channel values are defined as `byte` constants in their respective reactor files
- The mempool data channel you mentioned is actually 0x31 (49 in decimal), not 40 or 80
- There are two different `DataChannel` constants: one for consensus (0x21) and one for propagation (0x50)
- The CAT (Content Addressable Transaction) mempool uses separate channels for data and wants
- All channels use hexadecimal byte values to avoid conflicts
