# ADR 004: Data Availability Sampling Light Client

## Changelog

- 2021-05-03: Initial Draft

## Context

We decided to augment the existing [RPC-based Tendermint light client](https://github.com/cometbft/cometbft/blob/bc643b19c48495077e0394d3e21e1d2a52c99548/light/doc.go#L2-L126) by adding the possibility to additionally validate blocks by doing Data Availability Sampling (DAS).
In general, DAS gives light clients assurance that the data behind the block header they validated is actually available in the network and hence, that state fraud proofs could be generated.
See [ADR 002](adr-002-ipld-da-sampling.md) for more context on DAS.

A great introduction on the Tendermint light client (and light clients in general) can be found in this series of [blog posts](https://medium.com/tendermint/everything-you-need-to-know-about-the-tendermint-light-client-f80d03856f98) as well as this [paper](https://arxiv.org/abs/2010.07031).

This ADR describes the changes necessary to augment the existing Tendermint light client implementation with DAS from a UX as well as from a protocol perspective.

## Alternative Approaches

Ideally, the light client should not just request [signed headers](https://github.com/cometbft/cometbft/blob/bc643b19c48495077e0394d3e21e1d2a52c99548/light/doc.go#L35-L52) from [a few pre-configured peers](https://github.com/cometbft/cometbft/blob/bc643b19c48495077e0394d3e21e1d2a52c99548/light/setup.go#L51-L52) but instead also discover peers from a p2p network.
We will eventually implement this. For more context, we refer to this [issue](https://github.com/celestiaorg/celestia-core/issues/86).
This would require that the (signed) headers are provided via other means than the RPC.
See this [abandoned pull request](https://github.com/cometbft/cometbft/pull/4508) and [issue](https://github.com/cometbft/cometbft/issues/4456) in the Tendermint repository and also this [suggestion](https://github.com/celestiaorg/celestia-core/issues/86#issuecomment-831182564) by [@Wondertan](https://github.com/Wondertan) in this repository.

For some use-cases—like DAS light validator nodes, or the light clients of a Data Availability Layer that are run by full nodes of an Optimistic Rollup—it would even make sense that the light client (passively) participates in the consensus protocol to some extent; i.e. runs a subset of the consensus reactor to Consensus messages ([Votes](https://github.com/cometbft/cometbft/blob/bc643b19c48495077e0394d3e21e1d2a52c99548/types/vote.go#L48-L59) etc.) come in as early as possible.
Then light clients would not need to wait for the canonical commit to be included in the next [block](https://github.com/cometbft/cometbft/blob/bc643b19c48495077e0394d3e21e1d2a52c99548/types/block.go#L48).

For the RPC-based light client it could also make sense to add a new RPC endpoint to tendermint for clients to retrieve the [`DataAvailabilityHeader`](https://github.com/celestiaorg/celestia-core/blob/50f722a510dd2ba8e3d31931c9d83132d6318d4b/types/block.go#L52-L69) (DAHeader), or embed the DAHeader.
The [Commit](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/rpc/core/routes.go#L25) only contains the [SignedHeader](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/rpc/core/types/responses.go#L32-L36) (Header and Commit signatures).
Not all light clients will need the full DAHeader though (e.g. super-light-clients do not).


## Decision

For our MVP, we [decide](https://github.com/celestiaorg/celestia-core/issues/307) to only modify the existing RPC-endpoint based light client.
This is mostly because we want to ship our MVP as quickly as possible but independently of this it makes sense to provide a familiar experience for engineers coming from the Cosmos ecosystem.

We will later implement the above mentioned variants.
How exactly will be described in separate ADRs though.

## Detailed Design

From a user perspective very little changes:
the existing light client command gets an additional flag that indicates whether to run DAS or not.
Additionally, the light client operator can decide the number of successful samples to make to deem the block available (and hence valid).

In case DAS is enabled, the light client will need to:
1. retrieve the DAHeader corresponding to the data root in the Header
2. request a parameterizable number of random samples.

If the all sampling requests succeed, the whole block is available ([with some high enough probability](https://arxiv.org/abs/1809.09044)).

### UX

The main change to the light client [command](https://github.com/celestiaorg/celestia-core/blob/master/cmd/tendermint/commands/light.go#L32-L104) is to add in a new flag to indicate if it should run DAS or not.
Additionally, the user can choose the number of succeeding samples required for a block to be considered available.

```diff
===================================================================
diff --git a/cmd/tendermint/commands/light.go b/cmd/tendermint/commands/light.go
--- a/cmd/tendermint/commands/light.go	(revision 48b043014f0243edd1e8ebad8cd0564ab9100407)
+++ b/cmd/tendermint/commands/light.go	(date 1620546761822)
@@ -64,6 +64,8 @@
 	dir                string
 	maxOpenConnections int

+	daSampling     bool
+	numSamples     uint32
 	sequential     bool
 	trustingPeriod time.Duration
 	trustedHeight  int64
@@ -101,6 +103,10 @@
 	LightCmd.Flags().BoolVar(&sequential, "sequential", false,
 		"sequential verification. Verify all headers sequentially as opposed to using skipping verification",
 	)
+	LightCmd.Flags().BoolVar(&daSampling, "da-sampling", false,
+		"data availability sampling. Verify each header (sequential verification), additionally verify data availability via data availability sampling",
+	)
+	LightCmd.Flags().Uint32Var(&numSamples, "num-samples", 15, "Number of data availability samples until block data deemed available.")
 }
```

For the Data Availability sampling, the light client will have to run an IPFS node.
It makes sense to make this mostly opaque to the user as everything around IPFS can be [configured](https://github.com/ipfs/go-ipfs/blob/d6322f485af222e319c893eeac51c44a9859e901/docs/config.md) in the `$IPFS_PATH`.
This IPFS path should simply be a sub-directory inside the light client's [directory](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/cmd/tendermint/commands/light.go#L86-L87).
We can later add the ability to let users configure the IPFS setup more granular.

**Note:** DAS should only be compatible to sequential verification.
In case a light client is parametrized to run DAS and skipping verification, the CLI should return an easy-to-understand warning or even an error explaining why this does not make sense.

### Light Client Protocol with DAS

#### Light Store

The light client stores data in its own [badgerdb instance](https://github.com/celestiaorg/celestia-core/blob/50f722a510dd2ba8e3d31931c9d83132d6318d4b/cmd/tendermint/commands/light.go#L125) in the given directory:

```go
db, err := badgerdb.NewDB("light-client-db", dir)
```

While it is not critical for this feature, we should at least try to reuse that same DB instance for the local ipld store.
Otherwise, we introduce yet another DB instance; something we want to avoid, especially on the long run (see [#283](https://github.com/celestiaorg/celestia-core/issues/283)).
For the first implementation, it might still be simpler to create a separate DB instance and tackle cleaning this up in a separate pull request, e.g. together with other [instances]([#283](https://github.com/celestiaorg/celestia-core/issues/283)).

#### RPC

No changes to the RPC endpoints are absolutely required.
Although, for convenience and ease of use, we should either add the `DAHeader` to the existing [Commit](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/rpc/core/routes.go#L25) endpoint, or, introduce a new endpoint to retrieve the `DAHeader` on demand and for a certain height or block hash.

The first has the downside that not every light client needs the DAHeader.
The second explicitly reveals to full-nodes which clients are doing DAS and which not.

**Implementation Note:** The additional (or modified) RPC endpoint could work as a simple first step until we implement downloading the DAHeader from a given data root in the header.
Also, the light client uses a so called [`Provider`](https://github.com/cometbft/cometbft/blob/7f30bc96f014b27fbe74a546ea912740eabdda74/light/provider/provider.go#L9-L26) to retrieve [LightBlocks](https://github.com/cometbft/cometbft/blob/7f30bc96f014b27fbe74a546ea912740eabdda74/types/light.go#L11-L16), i.e. signed headers and validator sets.
Currently, only the [`http` provider](https://github.com/cometbft/cometbft/blob/7f30bc96f014b27fbe74a546ea912740eabdda74/light/provider/http/http.go#L1) is implemented.
Hence, as _a first implementation step_, we should augment the `Provider` and the `LightBlock` to optionally include the DAHeader (details below).
In parallel but in a separate pull request, we add a separate RPC endpoint to download the DAHeader for a certain height.

#### Store DataAvailabilityHeader

For full nodes to be able to serve the `DataAvailabilityHeader` without having to recompute it each time, it needs to be stored somewhere.
While this is independent of the concrete serving mechanism, it is more so relevant for the RPC endpoint.
There is ongoing work to make the Tendermint Store only store Headers and the DataAvailabilityHeader in [#218](https://github.com/celestiaorg/celestia-core/pull/218/) / [#182](https://github.com/celestiaorg/celestia-core/issues/182).

At the time writing this ADR, another pull request ([#312](https://github.com/celestiaorg/celestia-core/pull/312)) is in the works with a more isolated change that adds the `DataAvailabilityHeader` to the `BlockID`.
Hence, the DAHeader is [stored](https://github.com/celestiaorg/celestia-core/blob/50f722a510dd2ba8e3d31931c9d83132d6318d4b/store/store.go#L355-L367) along the [`BlockMeta`](https://github.com/celestiaorg/celestia-core/blob/50f722a510dd2ba8e3d31931c9d83132d6318d4b/types/block_meta.go#L11-L17) there.

For a first implementation, we could first build on top of #312 and adapt to the changed storage API where only headers and the DAHeader are stored inside tendermint's store (as drafted in #218).
A major downside of storing block data inside of tendermint's store as well as in the IPFS' block store is that is not only redundantly stored data but also IO intense work that will slow down full nodes.


#### DAS

The changes for DAS are very simple from a high-level perspective assuming that the light client has the ability to download the DAHeader along with the required data (signed header + validator set) of a given height:

Every time the light client validates a retrieved light-block, it additionally starts DAS in the background (once).
For a DAS light client it is important to use [sequential](https://github.com/cometbft/cometbft/blob/f366ae3c875a4f4f61f37f4b39383558ac5a58cc/light/client.go#L46-L53) verification and not [skipping](https://github.com/cometbft/cometbft/blob/f366ae3c875a4f4f61f37f4b39383558ac5a58cc/light/client.go#L55-L69) verification.
Skipping verification only works under the assumption that 2/3+1 of voting power is honest.
The whole point of doing DAS (and state fraud proofs) is to remove that assumption.
See also this related issue in the LL specification: [#159](https://github.com/celestiaorg/celestia-specs/issues/159).

Independent of the existing implementation, there are three ways this could be implemented:
1. the DAS light client only accepts a header as valid and trusts it after DAS succeeds (additionally to the tendermint verification), and it waits until DAS succeeds (or there was an error or timeout on the way)
2. (aka 1.5) the DAS light client stages headers where the tendermint verification passes as valid and spins up DAS sampling rotines in the background; the staged headers are committed as valid iff all routines successfully return in time
3. the DAS light client optimistically accepts a header as valid and trusts it if the regular tendermint verification succeeds; the DAS is run in the background (with potentially much longer timeouts as in 1.) and after the background routine returns (or errs or times out), the already trusted headers are marked as unavailable; this might require rolling back the already trusted headers

We note that from an implementation point of view 1. is not only the simplest approach, but it would also work best with the currently implemented light client design.
It is the approach that should be implemented first.

The 2. approach can be seen as an optimization where the higher latency DAS can be conducted in parallel for various heights.
This could speed up catching-up (sequentially) if the light client went offline (shorter than the weak subjectivity time window).

The 3. approach is the most general of all, but it moves the responsibility to wait or to rollback headers to the caller and hence is undesirable as it offers too much flexibility.


#### Data Structures

##### LightBlock

As mentioned above the LightBlock should optionally contain the DataAvailabilityHeader.
```diff
Index: types/light.go
===================================================================
diff --git a/types/light.go b/types/light.go
--- a/types/light.go	(revision 64044aa2f2f2266d1476013595aa33bb274ba161)
+++ b/types/light.go	(date 1620481205049)
@@ -13,6 +13,9 @@
 type LightBlock struct {
 	*SignedHeader `json:"signed_header"`
 	ValidatorSet  *ValidatorSet `json:"validator_set"`
+
+	// DataAvailabilityHeader is only populated for DAS light clients for others it can be nil.
+	DataAvailabilityHeader *DataAvailabilityHeader `json:"data_availability_header"`
 }
```

Alternatively, we could introduce a `DASLightBlock` that embeds a `LightBlock` and has the `DataAvailabilityHeader` as the only (non-optional) field.
This would be more explicit as it is a new type.
Instead, adding a field to the existing `LightBlock`is backwards compatible and does not require any further code changes; the new type requires `To`- and `FromProto` functions at least.

##### Provider

The [`Provider`](https://github.com/cometbft/cometbft/blob/7f30bc96f014b27fbe74a546ea912740eabdda74/light/provider/provider.go#L9-L26) should be changed to additionally provide the `DataAvailabilityHeader` to enable DAS light clients.
Implementations of the interface need to additionally retrieve the `DataAvailabilityHeader` for the [modified LightBlock](#lightblock).
Users of the provider need to indicate this to the provider.

We could either augment the `LightBlock` method with a flag, add a new method solely for providing the `DataAvailabilityHeader`, or, we could introduce a new method for DAS light clients.

The latter is preferable because it is the most explicit and clear, and it still keeps places where DAS is not used without any code changes.

Hence:

```diff
Index: light/provider/provider.go
===================================================================
diff --git a/light/provider/provider.go b/light/provider/provider.go
--- a/light/provider/provider.go	(revision 7d06ae28196e8765c9747aca9db7d2732f56cfc3)
+++ b/light/provider/provider.go	(date 1620298115962)
@@ -21,6 +21,14 @@
 	// error is returned.
 	LightBlock(ctx context.Context, height int64) (*types.LightBlock, error)

+	// DASLightBlock returns the LightBlock containing the DataAvailabilityHeader.
+	// Other than including the DataAvailabilityHeader it behaves exactly the same
+	// as LightBlock.
+	//
+	// It can be used by DAS light clients.
+	DASLightBlock(ctx context.Context, height int64) (*types.LightBlock, error)
+
+
 	// ReportEvidence reports an evidence of misbehavior.
 	ReportEvidence(context.Context, types.Evidence) error
 }
```
Alternatively, with the exact same result, we could embed the existing `Provider` into a new interface: e.g. `DASProvider` that adds this method.
This is completely equivalent as above and which approach is better will become more clear when we spent more time on the implementation.

Regular light clients will call `LightBlock` and DAS light clients will call `DASLightBlock`.
In the first case the result will be the same as for vanilla Tendermint and in the second case the returned `LightBlock` will additionally contain the `DataAvailabilityHeader` of the requested height.

#### Running an IPFS node

We already have methods to [initialize](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/cmd/tendermint/commands/init.go#L116-L157) and [run](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/node/node.go#L1449-L1488)  an IPFS node in place.
These need to be refactored such that they can effectively be for the light client as well.
This means:
1. these methods need to be exported and available in a place that does not introduce interdependence of go packages
2. users should be able to run a light client with a single command and hence most of the initialization logic should be coupled with creating the actual IPFS node and [made independent](https://github.com/celestiaorg/celestia-core/blob/cbf1f1a4a0472373289a9834b0d33e0918237b7f/cmd/tendermint/commands/init.go#L119-L120) of the `tendermint init` command

An example for 2. can be found in the IPFS [code](https://github.com/ipfs/go-ipfs/blob/cd72589cfd41a5397bb8fc9765392bca904b596a/cmd/ipfs/daemon.go#L239) itself.
We might want to provide a slightly different default initialization though (see how this is [overridable](https://github.com/ipfs/go-ipfs/blob/cd72589cfd41a5397bb8fc9765392bca904b596a/cmd/ipfs/daemon.go#L164-L165) in the ipfs daemon cmd).

We note that for operating a fully functional light client the IPFS node could be running in client mode [`dht.ModeClient`](https://github.com/libp2p/go-libp2p-kad-dht/blob/09d923fcf68218181b5cd329bf5199e767bd33c3/dht_options.go#L29-L30) but be actually want light clients to also respond to incoming queries, e.g. from other light clients.
Hence, they should by default run in [`dht.ModeServer`](https://github.com/libp2p/go-libp2p-kad-dht/blob/09d923fcf68218181b5cd329bf5199e767bd33c3/dht_options.go#L31-L32).
In an environment were any bandwidth must be saved, or, were the network conditions do not allow the server mode, we make it easy to change the default behavior.

##### Client

We add another [`Option`](https://github.com/cometbft/cometbft/blob/a91680efee3653e3de620f24eb8ddca1c95ce8f9/light/client.go#L43-L117) to the [`Client`](https://github.com/cometbft/cometbft/blob/a91680efee3653e3de620f24eb8ddca1c95ce8f9/light/client.go#L173) that indicates that this client does DAS.

This option indicates:
1. to do sequential verification and
2. to request [`DASLightBlocks`](#lightblock) from the [provider](#provider).

All other changes should only affect unexported methods only.

##### ValidateAvailability

In order for the light clients to perform DAS to validate availability, they do not need to be aware of the fact that an IPFS node is run.
Instead, we can use the existing [`ValidateAvailability`](https://github.com/celestiaorg/celestia-core/blame/master/p2p/ipld/validate.go#L23-L28) function (as defined in [ADR 002](adr-002-ipld-da-sampling.md) and implemented in [#270](https://github.com/celestiaorg/celestia-core/pull/270)).
Note that this expects an ipfs core API object `CoreAPI` to be passed in.
Using that interface has the major benefit that we could even change the requirement that the light client itself runs the IPFS node without changing most of the validation logic.
E.g., the IPFS node (with our custom IPLD plugin) could run in different process (or machine), and we could still just pass in that same `CoreAPI` interface.

Orthogonal to this ADR, we also note that we could change all IPFS readonly methods to accept the minimal interface they actually use, namely something that implements `ResolveNode` (and maybe additionally a `NodeGetter`).

`ValidateAvailability` needs to be called each time a header is validated.
A DAS light client will have to request the `DASLightBlock` for this as per above to be able to pass in a `DataAvailabilityHeader`.

#### Testing

Ideally, we add the DAS light client to the existing e2e tests.
It might be worth to catch up with some relevant changes from tendermint upstream.
In particular, [tendermint/tendermint#6196](https://github.com/cometbft/cometbft/pull/6196) and previous changes that it depends on.

Additionally, we should provide a simple example in the documentation that walks through the DAS light client.
It would be good if the light client logs some (info) output related to DAS to provide feedback to the user.

## Status

Proposed

## Consequences

### Positive

- simple to implement and understand
- familiar to tendermint / Cosmos devs
- allows trying out the MVP without relying on the [celestia-app](https://github.com/celestiaorg/celestia-app) (instead a simple abci app like a modified [KVStore](https://github.com/celestiaorg/celestia-core/blob/42e4e8b58ebc58ebd663c114d2bcd7ab045b1c55/abci/example/kvstore/README.md) app could be used to demo the DAS light client)

### Negative

- light client does not discover peers
- requires the light client that currently runs simple RPC requests only to run an IPFS node
- rpc makes it extremely easy to infer which light clients are doing DAS and which not
- the initial light client implementation might still be confusing to devs familiar to tendermint/Cosmos for the reason that it does DAS (and state fraud proofs) to get rid of the underlying honest majority assumption, but it will still do all checks related to that same honest majority assumption (e.g. download validator sets, Commits and validate that > 2/3 of them signed the header)

### Neutral

DAS light clients need to additionally obtain the DAHeader from the data root in the header to be able to actually do DAS.

## References

We have linked all references above inside the text already.
