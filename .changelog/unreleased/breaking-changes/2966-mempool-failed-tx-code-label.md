- [mempool] \#2966 The `cometbft_mempool_failed_txs` Prometheus counter
  gains a new `code` label that splits failures by ABCI response code, with
  `precheck` / `postcheck` sentinels for node-level filter rejections.
  Existing recording rules and panels that aggregate (e.g.
  `sum by (instance) (rate(...))`) continue to return the same totals,
  but unaggregated `rate(...)` queries now produce one series per
  `(instance, code)` instead of one per `instance`. Operators using the
  raw counter without aggregation should review their dashboards and
  alerts. Also demotes the per-tx "failed tx" logs added in #2965 from
  Error to Debug.
