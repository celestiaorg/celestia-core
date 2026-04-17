- `[types]` Validate that `LightClientAttackEvidence.ConflictingBlock.SignedHeader`
  is non-nil in `ValidateBasic` to prevent a nil pointer panic during block
  deserialization. Backported from cometbft/cometbft#5757.
