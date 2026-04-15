- Deprecate the KV indexer: the default tx_index configuration is "null"
  instead of "kv". The "kv" indexer option still works but logs a deprecation
  warning at startup and will be removed in a future release.
- Deprecate the `tx`, `tx_search`, and `block_search` RPC endpoints. These
  endpoints still work but log deprecation warnings and will be removed in a
  future release.
- Deprecate KV indexer support in the `reindex-event` CLI command. The command
  still works with the KV indexer but logs a deprecation warning.
