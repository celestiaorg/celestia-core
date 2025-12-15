# Validator Set Inclusion Plan

Goal: allow header sync to advance across validator set changes by supplying the validator set needed to verify headers, while minimizing bandwidth and CPU overhead.

## Key insight: skipping verification

Under an honest majority assumption, we don't need to verify every commit. Instead:
- Use `VerifyCommitLightTrusting` to verify that 1/3+ of a trusted validator set signed a future header
- Chain linkage (`LastBlockID`) cryptographically binds all intermediate headers
- Since validator sets change incrementally (not wholesale), 1/3+ of validators typically persist across many changes
- This means we can skip verify once per batch (e.g., verify header 50, trust headers 1-49 via chain linkage)
- Only need the new validator set when our trusted set has <1/3 overlap with signers (rare)

## Protocol changes

### Proto message changes

Modify `proto/tendermint/headersync/types.proto`:

```protobuf
// SignedHeader pairs a header with its commit and optional validator set.
message SignedHeader {
  tendermint.types.Header header = 1;
  tendermint.types.Commit commit = 2;
  // ValidatorSet is included only when the validator set changed at this height
  // (i.e., header.NextValidatorsHash != header.ValidatorsHash at the previous height).
  // This is the validator set that will be used to verify subsequent headers.
  tendermint.types.ValidatorSet validator_set = 3;
}
```

After modifying the proto, regenerate Go code:
```bash
make proto-gen
```

### Message size estimate

For 50 headers with 100 validators:
- Header: ~700 bytes each -> 35 KB total
- Commit: ~7 KB each -> 350 KB total (only need to fully verify ~1 per batch)
- ValidatorSet: ~10 KB per change (typically 0-1 per batch)
- Total: ~400 KB typical, well under current 4 MB `MaxMsgSize` limit

## Implementation changes

### 1. Pool changes (`pool.go`)

Extend `SignedHeader` struct to include optional validator set:

```go
// SignedHeader pairs a header with its commit and optional validator set.
type SignedHeader struct {
    Header       *types.Header
    Commit       *types.Commit
    ValidatorSet *types.ValidatorSet // non-nil when validator set changed
}
```

No other pool changes needed - validator sets are stored alongside headers naturally.

### 2. Reactor changes (`reactor.go`)

#### 2.1 Replace `verifyHeader` with batch verification

Replace the current per-header `verifyHeader` method with batch-level verification:

```go
// verifyBatch verifies a batch of headers using skipping verification.
// Returns nil if the batch is valid, or an error describing the failure.
func (r *Reactor) verifyBatch(batch []*SignedHeader) error {
    if len(batch) == 0 {
        return errors.New("empty batch")
    }

    // Step 1: Skip verify last header
    lastHeader := batch[len(batch)-1]
    if err := r.skipVerifyHeader(lastHeader); err != nil {
        // <1/3 overlap - need to find intermediate trust anchor
        return r.verifyBatchWithIntermediateTrust(batch)
    }

    // Step 2: Verify chain linkage backwards from last to first
    if err := r.verifyChainLinkageBackward(batch); err != nil {
        return err
    }

    // Step 3: Update validator set for next batch
    r.updateValidatorSetFromBatch(batch)

    return nil
}

// skipVerifyHeader verifies that 1/3+ of our trusted validators signed the header.
func (r *Reactor) skipVerifyHeader(sh *SignedHeader) error {
    blockID := types.BlockID{
        Hash:          sh.Header.Hash(),
        PartSetHeader: sh.Commit.BlockID.PartSetHeader,
    }

    // Use 1/3 trust threshold
    trustLevel := cmtmath.Fraction{Numerator: 1, Denominator: 3}
    return types.VerifyCommitLightTrusting(
        r.chainID,
        r.currentValidators,
        sh.Commit,
        trustLevel,
    )
}

// verifyChainLinkageBackward verifies hash chain from last header back to first.
func (r *Reactor) verifyChainLinkageBackward(batch []*SignedHeader) error {
    // First, verify linkage to our last known header
    if r.lastHeader != nil {
        firstInBatch := batch[0]
        expectedHash := r.lastHeader.Hash()
        if !bytes.Equal(firstInBatch.Header.LastBlockID.Hash, expectedHash) {
            return fmt.Errorf("first header LastBlockID.Hash %X doesn't match previous header hash %X",
                firstInBatch.Header.LastBlockID.Hash, expectedHash)
        }
    }

    // Then verify internal chain linkage
    for i := len(batch) - 1; i > 0; i-- {
        current := batch[i]
        prev := batch[i-1]
        expectedHash := prev.Header.Hash()
        if !bytes.Equal(current.Header.LastBlockID.Hash, expectedHash) {
            return fmt.Errorf("header %d LastBlockID.Hash %X doesn't match header %d hash %X",
                current.Header.Height, current.Header.LastBlockID.Hash,
                prev.Header.Height, expectedHash)
        }
    }
    return nil
}

// updateValidatorSetFromBatch updates currentValidators if the batch contains
// a validator set change.
func (r *Reactor) updateValidatorSetFromBatch(batch []*SignedHeader) {
    // Scan backwards to find the last header with an attached validator set
    for i := len(batch) - 1; i >= 0; i-- {
        if batch[i].ValidatorSet != nil {
            r.currentValidators = batch[i].ValidatorSet
            r.Logger.Info("Updated validator set from batch",
                "height", batch[i].Header.Height,
                "validatorsHash", batch[i].ValidatorSet.Hash())
            return
        }
    }
}

// verifyBatchWithIntermediateTrust handles the rare case where <1/3 of trusted
// validators signed the last header. Searches for an intermediate header where
// we still have 1/3+ overlap.
func (r *Reactor) verifyBatchWithIntermediateTrust(batch []*SignedHeader) error {
    // Binary search or linear scan to find a header we can trust
    for i := len(batch) - 2; i >= 0; i-- {
        if err := r.skipVerifyHeader(batch[i]); err == nil {
            // Found a trustable header - verify linkage from there
            if err := r.verifyChainLinkageBackward(batch[:i+1]); err != nil {
                return err
            }
            // Update validator set from this point
            if batch[i].ValidatorSet != nil {
                r.currentValidators = batch[i].ValidatorSet
            }
            // Recursively verify the rest
            return r.verifyBatch(batch[i+1:])
        }
    }
    return fmt.Errorf("no header in batch has 1/3+ overlap with trusted validators")
}
```

#### 2.2 Update `respondGetHeaders` to attach validator sets

```go
func (r *Reactor) respondGetHeaders(msg *hsproto.GetHeaders, src p2p.Peer) {
    // ... existing code to load headers ...

    for h := msg.StartHeight; h < msg.StartHeight+count; h++ {
        meta := r.blockStore.LoadBlockMeta(h)
        if meta == nil {
            break
        }

        commit := r.blockStore.LoadSeenCommit(h)
        if commit == nil {
            commit = r.blockStore.LoadBlockCommit(h)
        }
        if commit == nil {
            break
        }

        headerProto := meta.Header.ToProto()
        commitProto := commit.ToProto()

        sh := &hsproto.SignedHeader{
            Header: headerProto,
            Commit: commitProto,
        }

        // Attach validator set if it changed at this height
        if h > 1 {
            prevMeta := r.blockStore.LoadBlockMeta(h - 1)
            if prevMeta != nil && !bytes.Equal(prevMeta.Header.NextValidatorsHash, meta.Header.ValidatorsHash) {
                // Validator set changed - attach the new validator set
                state, err := r.stateStore.Load()
                if err == nil {
                    vals, err := r.stateStore.LoadValidators(h)
                    if err == nil {
                        sh.ValidatorSet = vals.ToProto()
                    }
                }
            }
        }

        headers = append(headers, sh)
    }
    // ... send response ...
}
```

#### 2.3 Update `handleHeaders` to decode validator sets

```go
func (r *Reactor) handleHeaders(msg *hsproto.HeadersResponse, src p2p.Peer) {
    // ... existing validation ...

    signedHeaders := make([]*SignedHeader, 0, len(msg.Headers))
    for _, protoSH := range msg.Headers {
        header, err := types.HeaderFromProto(protoSH.Header)
        if err != nil {
            // ... handle error ...
        }
        commit, err := types.CommitFromProto(protoSH.Commit)
        if err != nil {
            // ... handle error ...
        }

        sh := &SignedHeader{
            Header: &header,
            Commit: commit,
        }

        // Decode validator set if present
        if protoSH.ValidatorSet != nil {
            valSet, err := types.ValidatorSetFromProto(protoSH.ValidatorSet)
            if err != nil {
                r.Logger.Error("Failed to convert validator set from proto", "peer", src.ID(), "err", err)
                r.Switch.StopPeerForError(src, err, r.String())
                return
            }
            sh.ValidatorSet = valSet
        }

        signedHeaders = append(signedHeaders, sh)
    }
    // ... continue with batch processing ...
}
```

#### 2.4 Add consensus handoff method

Add a method for consensus to update headersync's validator set:

```go
// UpdateValidatorSet updates the reactor's current validator set.
// Called by consensus when a new block is committed that changes the validator set.
func (r *Reactor) UpdateValidatorSet(validators *types.ValidatorSet) {
    r.mtx.Lock()
    defer r.mtx.Unlock()
    r.currentValidators = validators
    r.Logger.Info("Validator set updated by consensus", "hash", validators.Hash())
}
```

### 3. Consensus integration

In the consensus reactor (or state machine), after committing a block where the validator set changed, call:

```go
if !bytes.Equal(state.Validators.Hash(), state.NextValidators.Hash()) {
    headerSyncReactor.UpdateValidatorSet(state.NextValidators)
}
```

This ensures headersync has the correct validator set when catching up to tip.

## Verification algorithm summary

Given a batch of headers [H+1, H+2, ..., H+N] and trusted validator set VS at height H:

1. **Skip verify last header**: Try `VerifyCommitLightTrusting(VS, H+N.commit, 1/3)`.
   - If successful: H+N is trusted, proceed to step 2.
   - If failed (<1/3 overlap): Binary search for intermediate header with 1/3+ overlap, verify that, update VS from attached validator set, and retry remaining headers.

2. **Verify chain linkage**: Walk backwards from H+N to H+1, verifying each `header.LastBlockID.Hash == prev.Hash()`. O(1) hash comparison per header.

3. **Update validator set**: Find the last header in batch with attached `ValidatorSet`. Update `currentValidators` for next batch.

Typical case: one `VerifyCommitLightTrusting` call + N hash comparisons per batch.

## Security notes

- **Trust period (Phase 2)**: Add trusting-period and max-clock-drift checks once validator-set inclusion lands. The trusted height/hash obtained via state sync can seed this, so forks that leave the trusting window are rejected.
- **Evidence emission**: Light-client attack evidence is already handled by the evidence reactor; no extra wiring needed here. Add a TODO hook if we still want explicit integration from headersync error paths.
- **Hash binding**: The header hash already commits to `ValidatorsHash`; coupled with the `NextValidatorsHash` continuity check, this binds validator-set transitions. No separate link is required—just validate any provided validator set against the committed hash.

## Backward compatibility

Do NOT fall back to legacy behavior. This reactor has not reached production yet so there is no need to be backwards compatible with the existing headersync reactor.

## Test plan (consolidated)

### Properties and coverage
- **Chain linkage & validator hash binding**: 1, 2, 4, 5  
- **Validator-set authenticity (hash matches header)**: 2, 3  
- **Validator-set continuity (NextValidatorsHash)**: 4  
- **Overlap / skipping safety (<1/3 handling)**: 6  
- **Missing validator set at change height**: 5  
- **DoS / large validator set size bounds**: 7  
- **Multi-peer / partial responses / retries**: 10, 12  
- **Validator churn across batches**: 11  
- **Reconnect / progress after drop**: 13  
- **(Phase 2) Trust period / clock drift**: 8 (placeholder)

### Unit tests (`reactor_test.go`)

1. **TestReactor_SkipVerification_NoValidatorChange**
   - Batch of 50 headers with unchanged validator set
   - Verify only last header is signature-verified (`VerifyCommitLightTrusting`)
   - Verify chain linkage for all headers

2. **TestReactor_ValidatorSetChangeIncluded**
   - Validator change mid-batch with attached validator set
   - Require `validator_set.Hash() == header.ValidatorsHash`
   - Reactor updates `currentValidators` and continues verification

3. **TestReactor_ValidatorSetHashMismatch**
   - Attached validator set whose hash ≠ `ValidatorsHash`
   - Expect batch rejection and peer penalty

4. **TestReactor_NextValidatorsHashMismatch**
   - Prev header `NextValidatorsHash` differs from header `ValidatorsHash`
   - Expect rejection (continuity check)

5. **TestReactor_MissingValidatorSetAtChange**
   - Validator set changes but no set attached
   - Expect verification failure / batch redo until set available

6. **TestReactor_LessThanOneThirdOverlap_FindsAnchor**
   - Last header has <1/3 overlap with trusted set
   - Intermediate header has ≥1/3 and attached validator set
   - Reactor finds anchor, updates validators, completes batch

7. **TestReactor_LargeValidatorSetBound**
   - Oversized validator set (beyond consensus max) is rejected to limit DoS

8. **TestReactor_TrustPeriodEnforced (Phase 2)**
   - Placeholder: once trust-period logic lands, reject headers older than trusting period or too far in future

### Pool tests (`pool_test.go`)

9. **TestPool_SignedHeaderWithValidatorSetLifecycle**
   - Ensure `SignedHeader` stores optional validator set
   - Batch lifecycle with validator-set-bearing headers

### Integration tests (`reactor_integration_test.go`)

10. **TestHeaderSync_BasicAndMultiPeer**
    - Sync from multiple peers; one peer partial, one complete
    - Confirms catch-up and correct height tracking

11. **TestHeaderSync_ValidatorSetChangeAcrossBatches**
    - Validator set changes on and off batch boundaries
    - Verify correct validator selection after each change

12. **TestHeaderSync_MultipleValidatorSetChangesAndPartialResponses**
    - Peers differ: some have headers only, others have validator sets
    - Ensure missing sets trigger re-requests / alternate peers

13. **TestHeaderSync_ReconnectResume**
    - Disconnect mid-sync; on reconnect, resumes with preserved validator state

## Rollout steps

1. Define new proto messages/fields (add optional `ValidatorSet` to `SignedHeader`) and regen code.
2. Update `handleHeaders` to decode validator sets from proto.
3. Update `respondGetHeaders` to attach validator sets when `NextValidatorsHash` changes.
4. Implement batch verification (`verifyBatch`, `skipVerifyHeader`, `verifyChainLinkageBackward`).
5. Add `UpdateValidatorSet` method and wire up consensus handoff.
6. Add unit tests for skip verification logic.
7. Add integration tests for validator set changes.
8. Monitor message size in testnet; adjust `MaxMsgSize`/batch size if needed.
