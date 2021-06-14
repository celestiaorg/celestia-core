# Lazyledger ADR - Data Availability Module and Components

## Changelog

* 10.06.2021 - Created

## Context
The core of LazyLedger technology relies on Data Availability schema. For the moment of ADR creation, most DA pieces and primitives like `nmt` and `rsmt2d` are already implemented. Furthermore, prototype implementation over the network exists as well. However, it still has several unfilled gaps:
* Slow data retreivability - Validators needs to get data faster and proactively.
* Fine-grained control over dependencies and 3rd party protocols.
* Implementation modularity - similar to `nmt` and `rsmt2d`, as it's meant to be used outside of `ll-core` as well.
* Generic data types and abstractions - contribute to modularity and configurable composition of DA implementations.

The goal of this ADR is to provide a detailed design on how to resolve issues above.

## Detailed Design

### Package
Data Availability related logic is place in `lazyledger-core/p2p/ipld`. The name and location was good for original purpose, as that's a p2p stuff that
uses IPLD in it's core. However, currently it makes sense to generalize and rename the package to `da`(short for DataAvailability) or even move it in it's own
repository, what could be done later with ease if package is properly designed.

### Data Types
Currently, `DAHeader` and block's `Data` are defined in `ll-core/types`. However, with modularity in mind and plan for DA to be used outside of `ll-core`, e.g. in `optimint`, it now make sense to move them to new `da` package. Also, `Data` contains information unrelated to DA in general like Txs, so it makes sense for DA to rely only on `NamespacedShare`, leaving the process of preparing shares of any data to user.


### Interface
```go
// DataAvailability defines an interface for Data Availability related operations over the network.
// The goal of this interface is to become general enough to later abstract over other DA schemas, e.g. Kate comms, along with different networking shemas.
// NOTE: Put and Get relies on DAHeader which is used to address and verify integrity of data, and its solely a user responsibility to manage header persistance
//  and transportation between peers interested in data.
type DataAvailability interface {
	// Put serializes Data, computes DAHeader and makes it available over the network.
	Put(context.Context, *types.Block) (*types.DataAvailabilityHeader, error)

	// Get efficiently retrieves block's Data using given DAHeader from the network.
	Get(context.Context, *types.DataAvailabilityHeader) (*types.Data, error)

	// Validate performs data availability validation over the network.
	// If succeeds, the data is proven to be available for specified validation options.
	Validate(context.Context, *types.DataAvailabilityHeader, ...ValidationOption) error
}
```

### Pull DA Components
Pull DA is the first implementation of DA interface. We already implemented it partially. The main idea behind it, is in its name - pull. That is, pull DA
pulls Data by requesting it from the "network" mainly using [kDHT from IPFS/libp2p](https://github.com/libp2p/go-libp2p-kad-dht).

LL's specific DHT usage is described in high-level [here](https://github.com/lazyledger/lazyledger-core/issues/395).

All pull related types and logic is going to be implemented in it's own package `dah/pull`

#### DAProvider
For DHT related operation a `DAProvider` is introduced with further semantics:
```go
type DAProvider interface {
    // Provide announces to the network that the node calling it hosts data addressed by given DAHeader.
    // Announcement lives for 24h by default.
    Provide(context.Context, DAHeader) error
    // Reprovide periodically(every 24h by default) executes Provide over given DAHeader.
    // It persists reporvide requirement in datastore to continue reproviding after app restarts.
    Reprovide(DAHeader) error
    // Unprovide prevents further reprovides for a given DAHeader
    Unprovide(DAHeader) error
}
```

// TODO: Add to specs as well
We also define several system-wide parameters related to Provider and DHT:
* `provideValidity=24h` - lifespan of a providing record.
* `kBucketSize=20` - configures bucket size of the kademlia routing table.
* `resiliency=3` - the number of peers closest to a target that must have responded in order for a given DHT query to complete.
* `alpha=32` - configures the number of concurrent requests for a given DHT query.
* `protocolPrefix="/celestia"` - protocol prefix to distinguish our DHT network

#### PullDA
The DA implementation itself.

##### Put
As from the method description of the interface, pull version of `Put` ensures `Data` is available on the network. In terms of "pulling" that means requestable
and retreivable by any peer from the node calling the `Put`.

Logic simply exucutes `Provider.Provide` and `Provider.Reprovide` for every DA Header column and row roots. This ensures capabilities described above initially and over time.

##### Get

### Push DA Components
Push DA is a new DA implementation meant to solve slow data retreivability problem by proactively broadcasting content rather than requesting it as in pull
approach. For broadcasting data to network the plan is to rely on [libp2p PubSub protocol](https://github.com/libp2p/go-libp2p-pubsub) which provides simple interface and strong guarantees for content delivery, while managing peer exchange and network topoly formation.

PubSub provides a notion of topics what allows peers to exchange data contextualized with a specific string topic. Push DA will allow setting custom topic for users. For LL we will use the `/celestia` topic.

#### PushDA

##### Put
`Put` in push schema takes all the shares of an extended square, encodes every share along with a `(col;row)` coordinate together with `DataHash` and publishes them to the `/celestia` topic.

Alternatives/options besides pushing all the shares:
* Pushing only real data excluding redundant.
* Pushing whole rows or columns.

Arguments against above alternatives:
* Without pushing redundant data we lose important data avalability assumptions. Note that Push schema can be used independently from pull schema, but if used in
  composition, the optimization to propagate only real data can be applied, as then Push side will be backed by Pull which always tracks redundant data. But by
  default all the data must be broadcasted.
* Without both redundant data and sample level broadcast granularity, we lose ability to recover whole block sooner than later, as sticking to row
  broadcasting prevents recovery by column and vice-versa.
