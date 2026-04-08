- [mempool] \#2913 Add UserTxLatency histogram metric to track latency
  from when a user-submitted transaction enters the mempool to when it
  is included in a committed block. Uses explicit bucket boundaries with
  dense resolution around 13s for accurate p99.9 SLA measurement.
