# Tracing

This package provides tracing functionality for CometBFT. It allows tracing various events and writing them to local files.

## Usage

To enable the local tracer, add the following to the config.toml file:

```toml
# The tracer to use for collecting trace data.
trace_type = "local"

# The size of the cache for each table. Data is constantly written to disk,
# but if this is hit data past this limit is ignored.
trace_push_batch_size = 1000

# The list of tables that are updated when tracing. All available tables and
# their schema can be found in the libs/trace/schema package. It is represented as a
# comma separate string. For example: "consensus_round_state,mempool_tx".
tracing_tables = ""
```

You can then create a local tracer like this:

```go
tracer, err := trace.NewLocalTracer(cfg, logger, chainID, nodeID)
if err != nil {
    return err
}
```

## Event Collection

Collect the events after the data collection is completed by simply transfering
the files however you see fit. For example, using the `scp` command:

```bash
scp -r user@host:/path/to/.celestia-app/data/traces /path/to/local/directory
```

or using aws s3 (after setting up the aws cli ofc):

```bash
aws s3 cp /path/to/.celestia-app/data/traces s3://<bucket-name>/<prefix> --recursive
```