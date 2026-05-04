- Fix gRPC `BlockAPI.broadcastToListeners` holding the global mutex while
  sending to subscriber channels. A slow subscriber would block the
  broadcaster, starving every other subscriber and growing the EventBus
  subscription buffer until the process was OOM-killed. The broadcaster
  now snapshots listeners under the lock and does non-blocking sends
  outside it. Server-side gRPC keepalive params were also added so dead
  client connections get reaped. ([\#2967](https://github.com/celestiaorg/celestia-core/issues/2967))
