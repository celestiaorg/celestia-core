- Fix `BlockAPI.Stop()` deadlocking the gRPC streaming API on shutdown.
  `Stop` acquired `blockAPI.Lock()` and then called `closeAllListeners`,
  which tried to acquire the same non-reentrant mutex. Renamed the helper
  to `closeAllListenersLocked` (caller now holds the lock). Also removed
  a `newBlockSubscription = nil` assignment that raced with
  `StartNewBlockEventListener`. Fixed the same reentrant-mutex pattern in
  `broadcastToListeners`: the recover path now calls a
  `removeHeightListenerLocked` helper instead of re-acquiring the lock.
  ([\#3003](https://github.com/celestiaorg/celestia-core/issues/3003))
