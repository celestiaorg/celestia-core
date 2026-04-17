- Validate `PartSetHeader.Total` does not exceed
  `MaxBlockPartsCount` in `ValidateBasic()` to prevent
  OOM via oversized bit array allocation from malicious
  peer proposals.
