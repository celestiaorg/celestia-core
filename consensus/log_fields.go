package consensus

import (
	"encoding/hex"
	"strings"

	"github.com/cometbft/cometbft/types"
)

// voteLogFields returns a redacted set of log key/values for a vote.
//
// NOTE: Do not log the full vote struct, as it includes signatures and (possibly)
// vote extensions.
func voteLogFields(prefix string, v *types.Vote) []any {
	p := strings.TrimSpace(prefix)
	if p == "" {
		p = "vote_"
	}

	if v == nil {
		return []any{p + "present", false}
	}

	var blockHashShort string
	if len(v.BlockID.Hash) > 0 {
		// 6 bytes -> 12 hex chars: enough to correlate without dumping full hash
		n := 6
		if len(v.BlockID.Hash) < n {
			n = len(v.BlockID.Hash)
		}
		blockHashShort = hex.EncodeToString(v.BlockID.Hash[:n])
	}

	return []any{
		p + "present", true,
		p + "height", v.Height,
		p + "round", v.Round,
		p + "type", v.Type,
		p + "validator_index", v.ValidatorIndex,
		p + "validator_address", hex.EncodeToString(v.ValidatorAddress),
		p + "block_hash", blockHashShort,
		p + "sig_len", len(v.Signature),
		p + "ext_len", len(v.Extension),
		p + "ext_sig_len", len(v.ExtensionSignature),
	}
}

// commitSigLogFields returns a redacted set of log key/values for a commit
// signature.
//
// NOTE: Do not log the full commit signature, as it includes the signature bytes.
func commitSigLogFields(prefix string, s types.CommitSig) []any {
	p := strings.TrimSpace(prefix)
	if p == "" {
		p = "commit_sig_"
	}

	return []any{
		p + "block_id_flag", s.BlockIDFlag,
		p + "validator_address", hex.EncodeToString(s.ValidatorAddress),
		p + "signature_len", len(s.Signature),
		p + "timestamp", s.Timestamp,
	}
}
