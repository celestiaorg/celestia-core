# blobtx-scan

Checks whether replacing celestia-core's legacy BlobTx decoder with go-square's
would change any historical transaction hash. It fetches every block in a range
over RPC and runs both decoders on each transaction. Any disagreement is written
to a findings file: for that block, the new decoder would compute a different
`Tx.Hash`, and therefore a different `Header.DataHash`.

The tool is its own Go module, pinned to celestia-core `v0.42.4` (which still
has the legacy decoder) and go-square `v4.1.0`.

## Run it on an archive node

Run it on the archive host against its local RPC. It only reads over RPC, so the
node keeps running. Check `earliest_block_height` in `/status` first: it must be
the chain's first height, otherwise the scan doesn't cover all of history.

```sh
cd scripts/blobtx-scan
go build -o blobtx-scan .
./blobtx-scan -rpc http://127.0.0.1:26657 -workers 32 \
  -out mainnet-findings.jsonl -checkpoint mainnet.checkpoint
```

The scan resumes from the checkpoint file if it is interrupted. The checkpoint
also stores the chain, start height, stride and totals so far, and the scan
refuses a checkpoint written with different settings. When it finishes, it
prints a summary for the whole range. `findings: 0` means both decoders unwrap every historical
transaction to the same bytes. `-stride N` samples every Nth height for a quick
check.
