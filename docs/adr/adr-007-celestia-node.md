# ADR for Devnet Celestia Node

## Context

**// TODO talk about how we decided this initial design approach at the offsite, blah blah**

This ADR describes a basic pre-devnet design for a "Celestia Node" that was decided at the August 2021 Kyiv offsite that will ideally be completed in early November 2021 and tested in the first devnet.

The goal of this design is to get a basic structure of "Celestia Node" interoperating with a "Celestia Core" consensus node by November 2021 (devnet). 

After basic interoperability on devnet, there will be an effort to merge consensus functionality into the "Celestia Node" design as a modulor service that can be added on top of the basic functions of a "Celestia Node".

## Decision

A "Celestia Node" will be distinctly different than a "Celestia Core" node in the initial implementation for devnet, with plans to merge consensus core functionality into the general design of a "Celestia Node", just added as an additional `ConsensusService` on the node.

**For devnet, a `light` Celestia Node must be able to do the following:**
* propagate relevant block information (in the form of `ExtendedHeader`s and `FraudProof`s to its "Celestia Node" peers
* verify `ExtendedHeader`s
* perform sampling and serve sample requests as well as `SharesByNamespace` requests
* request and serve `State` to get `AccountBalance` in order to submit transactions


**For devnet, a `full` Celestia Node must be able to do everything a `light` Celestia Node does, in addition to the following:**
* receive blocks from a "Celestia Core" node by subscribing to `NewBlockEvents` using the `/block` RPC endpoint of "Celestia Core" node
* erasure code the block / verify erasure coding
* generate a `DataAvailabilityHeader`

## Detailed Design

The Celestia Node will have a central `Node` data structure around which all services will be focused. A user will be able to initialise a Celestia Node as either a `full` or `light` node. Upon start, the `Node` gets created, configured and will also configure services to register on the `Node` based on the node type (`light` or `full`).

A `full` node encompasses the functionality of a `light` node along with additional services that allow it to interact with a Celestia Core node.

A `light` node will provide the following services: 
* `ExtendedHeaderService`
    * `ExtendedHeaderExchange`
    * `ExtendedHeaderSub`
    * `ExtendedHeaderStore`
* `FraudProofService`
    * `FraudProofSub`
    * `FraudProofStore`
* `ShareService`
    * `ShareExchange`
    * `ShareStore`
* `StateService` *(optional for devnet)*
    * `StateExchange`
* `TransactionService` *(dependent on `StateService` implementation, but optional for devnet)*
    * `SubmitTx`

A `full ` node will provide the following services: 
* `ExtendedHeaderService`
    * `ExtendedHeaderExchange`
    * `ExtendedHeaderSub`
    * `ExtendedHeaderStore`
* `FraudProofService`
    * `FraudProofGeneration`
    * `FraudProofSub`
    * `FraudProofStore`
* `ShareService`
    * `ShareExchange`
    * `ShareStore`
* `BlockService`
    * `BlockErasureCoding` 
    * `BlockExchange`
    * `BlockStore`

## Consequences

// maybe the fact that since eventually celestia core nodes and celestia nodes will have to communicate on a p2p level vs via plain rpc, it will be annoying to change the interface?

## Status

Proposed

## References

@TODO 

