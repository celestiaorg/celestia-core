- Lock-protect the initial write to `BlockAPI.newBlockSubscription` in
  `StartNewBlockEventListener` so all writes to the field are consistently
  synchronized with `Stop` and `retryNewBlocksSubscription`.
  ([\#3005](https://github.com/celestiaorg/celestia-core/issues/3005))
