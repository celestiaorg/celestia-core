# Celestia Specific Changelog

## Unreleased Changes
- Add .github/workflows/pending-changelog-checker.yml to enforce updating this file (`CELESTIA_CHANGELOG.md`) on commits to `master` branch. 

## v0.1

### BREAKING CHANGES

- Constants
    - [consts] \#83 and #530 Celestia specific consts were added in their own package

- Data Hash
    - [types] \#246 Spec compliant share splitting
    - [types] \#261 Spec compliant merge shares
    - [types] \#83 Transactions, intermediate state roots, evidence, and messages are now included in the block data. 
    - [da] \#83 and \#539 Introduction of the `DataAvailabilityHeader` and block data erasure
    - [types] \#539 the data hash now commits to the erasured block data instead of only the raw transactions

- App
    - [app] \#110 and #541 Introduction of an additional ABCI method, `PreprocessTxs`
