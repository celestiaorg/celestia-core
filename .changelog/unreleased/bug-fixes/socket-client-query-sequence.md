- `[abci/client]` Add missing `QuerySequence` case to `resMatchesReq` in socket
  client, which caused the client to treat a valid `QuerySequence` response as
  unexpected and terminate the connection.
