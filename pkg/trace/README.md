# remote: push arbitrary trace level data to an influxdb instance

This package has code to create a client that can be used to push events to an
influxdb instance. It is used to collect trace data from many different nodes in
a network. If there is no URL in the config.toml, then the underlying client is
nil and no points will be written. The provided chainID and nodeID are used to
tag all points. The underlying client is exposed to allow for custom writes, but
the WritePoint method should be used for most cases, as it enforces the schema.

## Usage and Schema

To use this package, first create a new client using the `NewClient` function,
then pass that client to the relevant components that need to push events. After
that, you can use the `WritePoint` method to push events to influxdb. In the below
example, we're pushing a point in the consensus reactor to measure exactly when
each step of consensus is reached for each node.

```go
if cs.eventCollector.IsCollecting() {
		cs.eventCollector.WritePoint("consensus", map[string]interface{}{
			"roundData": []interface{}{rs.Height, rs.Round, rs.Step},
		})
}
```

Using this method enforces the typical schema, where we are tagging (aka
indexing) each point by the chain-id and the node-id, then adding the local time
of the creation of the event. If you need to push a custom point, you can use
the underlying client directly. See influxdb2.WriteAPI for more details.

### Schema

All points in influxdb are divided into a key value pair per field. These kvs
are indexed first by a "measurement", which is used as a "table" in other dbs.
Additional indexes can also be added, we're using the chain-id and node-id here.
This allows for us to quickly query for trace data for a specific chain and/or
node.

```flux
from(bucket: "e2e")
  |> range(start: -1h)
  |> filter(
    fn: (r) => r["_measurement"] == "consensus"
      and r.chain_id == "ci-YREG8X"
      and r.node_id == "0b529c309608172a29c49979394734260b42acfb"
    )
```



### e2e tests

To push events from e2e tests, we only need to specify the URL and the token via
the cli.

```bash
cd test/e2e
make && ./build/runner -f ./networks/ci.toml --influxdb-url=http://your-influx-ip:8086/ --influxdb-token="your-token"
```
