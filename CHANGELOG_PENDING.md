# Unreleased Changes

## vX.X

Special thanks to external contributors on this release:

Friendly reminder, we have a [bug bounty program](https://hackerone.com/tendermint).

### BREAKING CHANGES

- CLI/RPC/Config
  - [config] \#5598 The `test_fuzz` and `test_fuzz_config` P2P settings have been removed. (@erikgrinaker)
  - [config] \#5728 `fast_sync = "v1"` is no longer supported (@melekes)
  - [cli] \#5772 `gen_node_key` prints JSON-encoded `NodeKey` rather than ID and does not save it to `node_key.json` (@melekes)
  - [cli] \#5777 use hypen-case instead of snake_case for all cli comamnds and config parameters

- Apps
  - [ABCI] \#5447 Remove `SetOption` method from `ABCI.Client` interface
  - [ABCI] \#5447 Reset `Oneof` indexes for  `Request` and `Response`.

- P2P Protocol

- Go API
  - [abci/client, proxy] \#5673 `Async` funcs return an error, `Sync` and `Async` funcs accept `context.Context` (@melekes)
  - [p2p] Removed unused function `MakePoWTarget`. (@erikgrinaker)
  - [libs/bits] \#5720 Validate `BitArray` in `FromProto`, which now returns an error (@melekes)

- [libs/os] Kill() and {Must,}{Read,Write}File() functions have been removed. (@alessio)

- Blockchain Protocol

### FEATURES

### IMPROVEMENTS

### BUG FIXES

- [rpc/jsonrpc/server] \#6191 Correctly unmarshal `RPCRequest` when data is `null` (@melekes)
