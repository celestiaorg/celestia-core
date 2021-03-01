# LAZY ADR 002: Sampling erasure coded Block chunks

## Changelog

- 26-2-2021: Created

## Context

In Tendermint's block gossiping each peer gossips random parts of block data to peers.
For LazyLedger, we need nodes (from light-clients to validators) to be able to sample rows/columns of the erasure coded
block (aka the extended data square) from the network.
This is necessary for Data Availability proofs.

![extended_square.png](img/extended_square.png)

A high-level, implementation-independent formalization of above mentioned sampling and Data Availability proofs can be found in:
[_Fraud and Data Availability Proofs: Detecting Invalid Blocks in Light Clients_](https://fc21.ifca.ai/papers/83.pdf).

For the time being, besides the academic paper, no other formalization or specification of the protocol exists.
Currently, the LazyLedger specification itself only describes the [erasure coding](https://github.com/lazyledger/lazyledger-specs/blob/master/specs/data_structures.md#erasure-coding)
and how to retrieve the extended data square from the block data.

This ADR:
- describes the high-level requirements
- defines the API that and how it can be used by different components of LazyLedger (block gossiping, block sync, DA proofs)
- documents decision on how to implement this.


The core data structures and the erasure coding of the block are already implemented in lazyledger-core ([#17], [#19], [#83]).
While there are no ADRs for these changes, we can refer to the LazyLedger specification in this case.
For this aspect, the existing implementation and specification should already be on par for the most part.
The exact arrangement of the data as described in this [rationale document](https://github.com/lazyledger/lazyledger-specs/blob/master/rationale/message_block_layout.md)
in the specification can happen at app-side of the ABCI boundary.
The latter was implemented in [lazyledger/lazyledger-app#21](https://github.com/lazyledger/lazyledger-app/pull/21)
leveraging a new ABCI method, added in [#110](https://github.com/lazyledger/lazyledger-core/pull/110).
This new method is a sub-set of the proposed ABCI changes aka [ABCI++](https://github.com/tendermint/spec/pull/254).

Mustafa Al-Bassam (@musalbas) implemented a [prototype](https://github.com/lazyledger/lazyledger-prototype)
whose main purpose is to realistically analyse the protocol.
Although the prototype does make any network requests and only operates locally, it currently is the most advanced implementation of the protocol.
It uses the [rsmt2d] library.

The implementation will essentially use IPFS' APIs. For reading (and writing) chunks
will use the IPLD [`DagService`](https://github.com/ipfs/go-ipld-format/blob/d2e09424ddee0d7e696d01143318d32d0fb1ae63/merkledag.go#L54),
more precisely the [`NodeGetter`](https://github.com/ipfs/go-ipld-format/blob/d2e09424ddee0d7e696d01143318d32d0fb1ae63/merkledag.go#L18-L27)
and [`NodeAdder`](https://github.com/ipfs/go-ipld-format/blob/d2e09424ddee0d7e696d01143318d32d0fb1ae63/merkledag.go#L29-L39).
This will be achieved by passing around a [CoreAPI](https://github.com/ipfs/interface-go-ipfs-core/blob/b935dfe5375eac7ea3c65b14b3f9a0242861d0b3/coreapi.go#L15)
object, which derive from the ipfs node which is created along a with a tendermint node (see [#152]).
This code snippet does exactly that (see the [go-ipfs documentation] for more examples):
````go
// This constructs an IPFS node instance
node, _ := core.NewNode(ctx, nodeOptions)
// This attaches the Core API to the constructed node
coreApi := coreapi.NewCoreAPI(node)
````

The above mentioned IPLD methods operate on so called [ipld.Nodes].
When computing the data root, we can pass in a [`NodeVisitor`](https://github.com/lazyledger/nmt/blob/b22170d6f23796a186c07e87e4ef9856282ffd1a/nmt.go#L22)
into the Namespaced Merkle Tree library to create these (each inner- and leaf-node in the tree becomes an ipld node).
As a peer that requests such an ipld node, the LazyLedger ipld plugin provides the [function](https://github.com/lazyledger/lazyledger-core/blob/ceb881a177b6a4a7e456c7c4ab1dd0eb2b263066/p2p/ipld/plugin/nodes/nodes.go#L175)
`NmtNodeParser` to transform the retrieved raw data back into an `ipld.Node`.

A more high-level description on the changes required to rip out the current block gossiping routine,
including changes to block storage-, RPC-layer, and potential changes to reactors is either handled in [LAZY ADR 001](./adr-001-block-propagation.md),
and/or in a few smaller, separate followup ADRs.

## Alternative Approaches

Instead of creating a full ipfs node object and passing it around as explained above
 - use API (http)
 - use ipld-light
 - use alternative client

Also, for better performance
 - use graph-sync, ipld selectors (ipld-prime)

Also, there is the idea, that nodes only receive the Header with the data root only
and, in an additional step/request, download the DA header using the library, too.
While this feature is not considered here, and we assume each node that uses this library has the DA header, this assumption
is likely to change when flesh out other parts of the system in more detail.
Note that this also means that light clients would still need to validate that the data root and merkelizing the DA header yield the same result.

## Decision

> This section records the decision that was made.
> It is best to record as much info as possible from the discussion that happened. This aids in not having to go back to the Pull Request to get the needed information.

> - TODO: briefly summarize github, discord, and slack discussions (?)
> - also mention Mustafa's prototype and compare both apis briefly (RequestSamples, RespondSamples, ProcessSamplesResponse)
> - mention [ipld experiments]




## Detailed Design

Add a package to the library that provides the following features:
 1. sample a given number of random row/col indices of extended data square given a DA header and indicate if successful or timeout/other error occurred.
 2. reconstruct the whole block from a given DA header
 3. get all messages of a particular namespace ID.

We mention 3. here mostly for completeness. Its details will be described / implemented in a separate ADR / PR.

Apart from the above mentioned features, we informally collect additional requirements:
- where randomness is needed, the randomness source should be configurable
- all replies by the network should be verified if this is not sufficiently covered by the used libraries already (IPFS)
- where possible, the requests to the network should happen in parallel (without DoSing the proposer for instance).

This library should be implemented as two new packages:

First, a sub-package should be added to the layzledger-core [p2p] package
which does not know anything about the core data structures (Block, DA header etc).
It handles the actual network requests to the IPFS network and operates on IPFS/IPLD objects directly and hence should live under [p2p/ipld].

Second, a high-level API that can "live" closer to the actual types, e.g., in a sub-package in [lazyledger-core/types]
or in a new top-level package `da`.

We first describe the high-level library here.
Two functions need to be added, and we describe them in detail inline in their
godoc comments below.

```go
// ValidateAvailability implements the protocol described in https://fc21.ifca.ai/papers/83.pdf.
// Specifically all steps of the the protocol described in section
// _5.2 Random Sampling and Network Block Recovery_ are carried out.
//
// In more detail it will first create numSamples random unique coordinates.
// Then, it will ask the network for the leaf data corresponding to these coordinates.
// Additionally to the number of requests, the caller can pass in a callback,
// which will be called on for each retrieved leaf with a verified Merkle proof.
//
// Among other use-cases, the callback can be useful to monitoring (progress), or,
// to process the leaf data the moment it was validated.
// The context can be used to provide a timeout.
// TODO: Should there be a constant = lower bound for #samples
func ValidateAvailability(
    ctx contex.Context,
    dah *DataAvailabilityHeader,
    numSamples int,
    leafSucessCb func(namespacedleaf []byte),
) error { /* ... */}

// RetrieveBlock can be used to recover the block Data.
// It will carry out a similar protocol as described for ValidateAvailability.
// The key difference is that it will sample enough chunks until it can recover the
// original data.
func RetrieveBlock(ctx contex.Context, dah *DataAvailabilityHeader) (types.Data, error) {/* ... */}

// PutLeaves takes the namespaced leaves from the extended data square and calls
// nodes.DataSquareRowOrColumnRawInputParser from the ipld plugin.
// The resulting ipld nodes are passed to a Batch calling AddMany:
// https://github.com/ipfs/go-ipld-format/blob/d2e09424ddee0d7e696d01143318d32d0fb1ae63/batch.go#L29
// Note, that this method could also return the row and column roots.
func PutLeaves(namespacedLeaves [][]byte) error
```

We now describe the lower-level library that will be used by above methods.
Again we provide more details inline in the godoc comments directly.

```go
// GetLeafData takes in a Merkle tree root transformed into a Cid
// and the leaf index to retrieve.
// Callers also need to pass in the total number of leaves of that tree
// to be able to create the path (this can also be achieved by traversing the tree until a leaf is reached).
func GetLeafData(
    ctx context.Context,
    rootCid cid.Cid,
    leafIndex uint32,
    totalLeafs uint32,
) ([]byte, error)
```



> This section does not need to be filled in at the start of the ADR, but must be completed prior to the merging of the implementation.
>
> Here are some common questions that get answered as part of the detailed design:
>
> - What are the user requirements?
>
> - What systems will be affected?
>
> - What new data structures are needed, what data structures will be changed?
>
> - What new APIs will be needed, what APIs will be changed?
>
> - What are the efficiency considerations (time/space)?
>
> - What are the expected access patterns (load/throughput)?
>
> - Are there any logging, monitoring or observability needs?
>
> - Are there any security considerations?
>
> - Are there any privacy considerations?
>
> - How will the changes be tested?
>
> - If the change is large, how will the changes be broken up for ease of review?
>
> - Will these changes require a breaking (major) release?
>
> - Does this change require coordination with the SDK or other?

## Status

> A decision may be "proposed" if it hasn't been agreed upon yet, or "accepted" once it is agreed upon. Once the ADR has been implemented mark the ADR as "implemented". If a later ADR changes or reverses a decision, it may be marked as "deprecated" or "superseded" with a reference to its replacement.

Proposed

## Consequences

> This section describes the consequences, after applying the decision. All consequences should be summarized here, not just the "positive" ones.

### Positive

 - simplicity & ease of implementation

### Negative

 - latency
 - being connected to the public IPFS network might be overkill if peers should in fact only care about a subset that participates in the LazyLedger protocol

### Neutral

## References

> Are there any relevant PR comments, issues that led up to this, or articles referenced for why we made the given design choice? If so link them here!

- https://docs.ipld.io/#nodes
- https://arxiv.org/abs/1809.09044
- https://fc21.ifca.ai/papers/83.pdf
- https://github.com/tendermint/spec/pull/254

- https://github.com/lazyledger/lazyledger-core/issues/85

[#17]: https://github.com/lazyledger/lazyledger-core/pull/17
[#19]: https://github.com/lazyledger/lazyledger-core/pull/19
[#83]: https://github.com/lazyledger/lazyledger-core/pull/83

[#152]: https://github.com/lazyledger/lazyledger-core/pull/152

[go-ipfs documentation]: https://github.com/ipfs/go-ipfs/tree/master/docs/examples/go-ipfs-as-a-library#use-go-ipfs-as-a-library-to-spawn-a-node-and-add-a-file
[ipld experiments]: https://github.com/lazyledger/ipld-plugin-experiments
[ipld.Nodes]: https://github.com/ipfs/go-ipld-format/blob/d2e09424ddee0d7e696d01143318d32d0fb1ae63/format.go#L22-L45

[rsmt2d]: https://github.com/lazyledger/rsmt2d


[p2p]: https://github.com/lazyledger/lazyledger-core/tree/master/p2p
[p2p/ipld]: https://github.com/lazyledger/lazyledger-core/tree/master/p2p/ipld
[lazyledger-core/types]:  https://github.com/lazyledger/lazyledger-core/tree/master/types


