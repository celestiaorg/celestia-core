# Unreleased Changes

## vX.X

Special thanks to external contributors on this release:

Friendly reminder, we have a [bug bounty program](https://hackerone.com/tendermint).

### BREAKING CHANGES

- CLI/RPC/Config

- Apps

- P2P Protocol

- Go API

- Blockchain Protocol

### FEATURES

### IMPROVEMENTS

- [statesync] \#5516 Check that all heights necessary to rebuild state for a snapshot exist before adding the snapshot to the pool. (@erikgrinaker)

### BUG FIXES

- [types] /#97 Fixes a typo that causes the row roots of the datasquare to be included in the DataAvailabilty header twice. (@evan-forbes)