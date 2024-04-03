# trace package

The `trace` package provides a decently fast way to store traces locally.

## Usage

change the config.toml to store traces in the .celestia-app/data/traces
directory.

```toml
# The tracer to use for event remote collection.
trace_type = "local"

# The size of the batches that are sent to the database.
trace_push_batch_size = 1000

# The list of tables that are updated when tracing. All available tables and
# their schema can be found in the pkg/trace/schema package. It is represented as a
# comma separate string. For example: "consensus_round_state,mempool_tx".
tracing_tables = "consensus_round_state,mempool_tx"
```
