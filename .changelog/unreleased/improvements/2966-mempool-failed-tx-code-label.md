- [mempool] \#2966 Add a `code` label to the `cometbft_mempool_failed_txs`
  Prometheus counter so failed-tx counts can be broken down by ABCI response
  code, with `precheck` / `postcheck` sentinels for node-level filter
  rejections. Also demote the per-tx "failed tx" logs added in #2965 from
  Error to Debug to keep them out of the default-level log stream.
