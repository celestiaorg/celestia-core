- `[statesync]` Guard against nil syncer dereference in Reactor.Receive
  when processing oversized SnapshotsResponse messages while no state sync
  is in progress.
