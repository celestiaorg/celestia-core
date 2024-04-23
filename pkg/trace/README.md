# trace package

The `trace` package provides a decently fast way to store traces locally.

## Usage

To enable the local tracer, add the following to the config.toml file:

```toml
# The tracer to use for collecting trace data.
trace_type = "local"

# The size of the batches that are sent to the database.
trace_push_batch_size = 1000

# The list of tables that are updated when tracing. All available tables and
# their schema can be found in the pkg/trace/schema package. It is represented as a
# comma separate string. For example: "consensus_round_state,mempool_tx".
tracing_tables = "consensus_round_state,mempool_tx"
```

Trace data will now be stored to the `.celestia-app/data/traces` directory, and
save the file to the specified directory in the `table_name.jsonl` format.

To read the contents of the file, open it and pass it the Decode function. This
returns all of the events in that file as a slice.

```go
events, err := DecodeFile[schema.MempoolTx](file)
if err != nil {
    return err
}
```

### Pull Based Event Collection

Pull based event collection is where external servers connect to and pull trace
data from the consensus node.

To use this, change the config.toml to store traces in the
.celestia-app/data/traces directory.

```toml
# The tracer pull address specifies which address will be used for pull based
# event collection. If empty, the pull based server will not be started.
trace_pull_address = ":26661"
```

To retrieve a table remotely using the pull based server, call the following
function:

```go
err := GetTable("http://1.2.3.4:26661", "mempool_tx", "directory to store the file")
if err != nil {
    return err
}
```

This stores the data locally in the specified directory.


### Push Based Event Collection

Push based event collection is where the consensus node pushes trace data to an
external server. At the moment, this is just an S3 bucket. To use this, two options are available:
#### Using push config file
Add the following to the config.toml file:

```toml
# TracePushConfig is the relative path of the push config.
# This second config contains credentials for where and how often to
# push trace data to. For example, if the config is next to this config,
# it would be "push_config.json".
trace_push_config = "{{ .Instrumentation.TracePushConfig }}"
```

The push config file is a JSON file that should look like this:

```json
{
    "bucket": "bucket-name",
    "region": "region",
    "access_key": "",
    "secret_key": "",
    "push_delay": 60 // number of seconds to wait between intervals of pushing all files
}
```

#### Using environment variables for s3 bucket
Alternatively, you can set the following environment variables:

```bash
export TRACE_PUSH_BUCKET_NAME=bucket-name
export TRACE_PUSH_REGION=region
export TRACE_PUSH_ACCESS_KEY=access-key
export TRACE_PUSH_SECRET_KEY=secret-key
export TRACE_PUSH_DELAY=push-delay
```
`bucket_name` , `region`, `access_key`, `secret_key` and `push_delay` are the s3 bucket name, region, access key, secret key and the delay between pushes respectively.
