# trace package

The `trace` package provides a decently fast way to store traces locally.

## Usage

change the config.toml to store traces in the .celestia-app/data/traces
directory.

```toml
# The tracer url token to use for remote event collection.
# If empty, remote event pulling is disabled.
trace_pull_address = ":26661"

# The tracer to use for collecting trace data.
trace_type = "local"

# The size of the batches that are sent to the database.
trace_push_batch_size = 1000

# The list of tables that are updated when tracing. All available tables and
# their schema can be found in the pkg/trace/schema package. It is represented as a
# comma separate string. For example: "consensus_round_state,mempool_tx".
tracing_tables = "consensus_round_state,mempool_tx"
```

To retrieve a table remotely using the pull based server, call the following
function:

```go
err := GetTable("http://1.2.3.4:26661/get_table", "mempool_tx", "directory to store the file")
if err != nil {
    return err
}
```

This will download and save the file to the specified directory in the
`table_name.jsonl` format.

To read the contents of the file, load it and pass it the Decode function.

```go
events, err := DecodeFile[schema.MempoolTx](downloadedFile)
if err != nil {
    return err
}
```
