# trace: push arbitrary trace level data to an influxdb instance

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
client.WritePoint(RoundStateTable, map[string]interface{}{
		HeightFieldKey: height,
		RoundFieldKey:  round,
		StepFieldKey:   step.String(),
})
```

Using this method enforces the typical schema, where we are tagging (aka
indexing) each point by the chain-id and the node-id, then adding the local time
of the creation of the event. If you need to push a custom point, you can use
the underlying client directly. See `influxdb2.WriteAPI` for more details.

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
    fn: (r) => r["_measurement"] == "consensus_round_state"
      and r.chain_id == "ci-YREG8X"
      and r.node_id == "0b529c309608172a29c49979394734260b42acfb"
    )
```

We can easily retrieve all fields in a relatively standard table format by using
the pivot `fluxQL` command.

```flux
from(bucket: "mocha")
  |> range(start: -1h)
  |> filter(fn: (r) => r._measurement == "consensus_round_state")
  |> pivot(rowKey:["_time"], columnKey: ["_field"], valueColumn: "_value")
```

### Querying Data Using Python

Python can be used to quickly search for and isolate specific patterns.

```python
from influxdb_client import InfluxDBClient
from influxdb_client.client.write_api import SYNCHRONOUS

client = InfluxDBClient(url="http://your-influx-url:8086/", token="your-influx-token", org="celestia")

query_api = client.query_api()

def create_flux_table_query(start, bucket, measurement, filter_clause):
    flux_table_query = f'''
    from(bucket: "{bucket}")
      |> range(start: {start})
      |> filter(fn: (r) => r._measurement == "{measurement}")
      {filter_clause}
      |> pivot(rowKey:["_time"], columnKey: ["_field"], valueColumn: "_value")
    '''
    return flux_table_query

query = create_flux_table_query("-1h", "mocha", "consenus_round_state", "")
result = query_api.query(query=query)
```

### Running a node with remote tracing on

Tracing will only occur if an influxdb URL in specified either directly in the
`config.toml` or as flags provided to the start sub command.

#### Configure in the `config.toml`

```toml
#######################################################
###       Instrumentation Configuration Options     ###
#######################################################
[instrumentation]

...

# The URL of the influxdb instance to use for remote event
# collection. If empty, remote event collection is disabled.
trace_push_url = "http://your-influx-ip:8086/"

# The influxdb token to use for remote event collection.
trace_auth_token = "your-token"

# The influxdb bucket to use for remote event collection.
trace_db = "tracer"

# The influxdb org to use for event remote collection.
trace_type = "noop"

# The size of the batches that are sent to the database.
trace_push_batch_size = 20

# The list of tables that are updated when tracing. All available tables and
# their schema can be found in the pkg/trace/schema package.
tracing_tables = ["consensus_round_state", "mempool_tx", ]

```

or

```sh
celestia-appd start --influxdb-url=http://your-influx-ip:8086/ --influxdb-token="your-token"
```

### e2e tests

To push events from e2e tests, we only need to specify the URL and the token via
the cli.

```bash
cd test/e2e
make && ./build/runner -f ./networks/ci.toml --influxdb-url=http://your-influx-ip:8086/ --influxdb-token="your-token"
```
