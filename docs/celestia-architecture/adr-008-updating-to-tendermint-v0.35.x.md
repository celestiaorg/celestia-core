# ADR 008: Updating to tendermint v0.35.x 

## Changelog

- 2022-05-03: Initial document describing changes to tendermint v0.35.x

## Context

Building off of ADR 007, we have further distilled the necessary changes to tendermint and continued to move added logic to other repos. Specifically, we have moved generation of the data hash, efficient construction of the data square, and a message inclusion check to celestia-app via adopting two new methods from ABCI++. This document is to serve as a guide for the remaining changes made on top of tendermint v0.35.4.

### Changes to tendermint

#### Misc

- [update github templates](https://github.com/celestiaorg/celestia-core/pull/405)
- [update README.md](https://github.com/celestiaorg/celestia-core/pull/737/commits/be9039d4e0f5d876ec3d8d4521be3374172d7992)
- [updating to go 1.17](https://github.com/celestiaorg/celestia-core/pull/737/commits/6094b7338082d106f81da987dffa56eb540a675e)
- [adding the consts package](https://github.com/celestiaorg/celestia-core/pull/737/commits/fea8528b0177230b7e75396ae05f7a9b5da23741)

#### Changing the way the data hash is calculated

To enable data availability sampling, Celestia uses a proprietary data square format to encode its block data. The data hash is generated from this data square by calculating namespace merkle tree root over each row and column. In the following changes, we implement encoding and decoding of block data to the data square format and tooling to generate the data hash. More details over this design can be found in our (archived but still very useful) [specs repo](https://github.com/celestiaorg/celestia-specs)

- [Adding the Data Availability Header](https://github.com/celestiaorg/celestia-core/pull/737/commits/116b7af4000920030a373363487ef9a9f084e066)
- [Adding a wrapper for namespaced merkle trees](https://github.com/celestiaorg/celestia-core/pull/737/commits/eee8f352cb6a1687a9f6b470abe28bbd4eb66413)
- [Adding Messages and Evidence to the block data](https://github.com/celestiaorg/celestia-core/pull/737/commits/86df6529a7c0cc1112c34b6bf1b5364aa0518dec)
- [Adding share splitting and merging for block encoding](https://github.com/celestiaorg/celestia-core/pull/737/commits/bf2d8b46c1caf1fed52e7db9bf8aa6a9847d84ab)
- [Modifying MakeBlock to also accept Messages](https://github.com/celestiaorg/celestia-core/pull/737/commits/bb970a417356ab030c934ccd2bd39c9641af45f8)

#### Adding PrepareProposal and ProcessProposal ABCI methods from ABCI++

- [PrepareProposal](https://github.com/celestiaorg/celestia-core/pull/737/commits/07f9a05444db763c44ff81f564e7350ddf57e5a4)
- [ProcessProposal](https://github.com/celestiaorg/celestia-core/pull/737/commits/2c9552db09841f2bbebc1ec34653b2441def9f13)

more details on how we use these new methods in the app can be found in the [ABCI++ Adoption ADR](https://github.com/celestiaorg/celestia-app/blob/master/docs/architecture/ADR-001-ABCI%2B%2B.md).

#### Wrapping Malleated Transactions

Tendermint and the cosmos-sdk were not built to handle malleated transactions (txs that are submitted by the user, but modified by the block producer before being included in a block). While not a final solution, we have resorted to adding the hash of the original transaction (the one that is not modified by the block producer) to the modified one. This allows us to track the transaction in the event system and mempool.

- [Index malleated Txs](https://github.com/celestiaorg/celestia-core/pull/737/commits/a54e3599a5ef6b2ba8b63f586aed8185a3f59e4d)

#### Create NMT Inclusion Proofs for Transactions

Since the block data that is committed over is encoded as a data square and we use namespaced merkle trees to generate the row and column roots of that square, we have to create transaction inclusion proofs also using nmts and a data square. The problem is that the block data isn't stored as a square, so in order to generate the inclusion proofs, we have to regenerate a portion of the square. We do that here.

- [Create namespace merkle tree inclusion proofs for transactions included in the block](https://github.com/celestiaorg/celestia-core/pull/737/commits/01051aa5fef0693bf3bda801e39c80e5746b9c33)

#### Adding the DataCommitment RPC endpoint

This RPC endpoint is used by quantum gravity bridge orchestrators to create a commitment over the block data of a range of blocks.

- [Adding the DataCommitment RPC endpoint](https://github.com/celestiaorg/celestia-core/pull/737/commits/134eeefb7af41afe760d4adc5b22a9d55e36bc3e)