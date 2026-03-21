- `[types]` Fix uint32 overflow in `RowProof.Validate()` that allowed an
  empty proof to validate against any data root. Reject empty row proofs
  and use uint64 arithmetic to prevent overflow when computing the
  expected number of rows.
