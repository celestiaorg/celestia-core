package types

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// TestReportedAttack_PrependByteToValidatorAddress tests the specific attack
// vector described in the security report: prepending 0xFF to a
// ValidatorAddress in an ExtendedCommit. This attack is caught during proto
// deserialization because CommitSig.ValidateBasic checks the address length.
func TestReportedAttack_PrependByteToValidatorAddress(t *testing.T) {
	height := int64(3)
	round := int32(1)

	// Create a valid ExtendedCommit.
	blockID := makeBlockIDRandom()
	voteSet, _, vals := randVoteSet(height, round, cmtproto.PrecommitType, 4, 1, true)
	extCommit, err := MakeExtCommit(blockID, height, round, voteSet, vals, time.Now(), true)
	require.NoError(t, err)
	require.NotEmpty(t, extCommit.ExtendedSignatures)

	// Apply the report's specific attack: prepend 0xFF to the first
	// validator's address. This makes the address 21 bytes instead of 20.
	poisonedProto := extCommit.ToProto()
	origAddr := poisonedProto.ExtendedSignatures[0].ValidatorAddress
	poisonedProto.ExtendedSignatures[0].ValidatorAddress = append([]byte{0xFF}, origAddr...)
	require.Len(t, poisonedProto.ExtendedSignatures[0].ValidatorAddress, crypto.AddressSize+1)

	// The attack is caught during deserialization because
	// CommitSig.ValidateBasic checks len(ValidatorAddress) == crypto.AddressSize.
	_, err = ExtendedCommitFromProto(poisonedProto)
	require.Error(t, err, "prepended-byte corruption must be rejected at deserialization")
}

// TestSameLengthAddressCorruption_PanicsOnRestart demonstrates the
// validation gap: if a corrupted ExtendedCommit with a same-length (20 byte)
// but wrong ValidatorAddress makes it into the blockstore, it causes a panic
// during vote set reconstruction (which happens on consensus restart via
// consensus/state.go:reconstructLastCommit).
//
// The blocksync reactor receives the ExtendedCommit from the peer separately
// from second.LastCommit. Before the fix, ExtendedCommit was only validated
// via EnsureExtensions (which only checks extension presence) but was NOT
// cross-validated against the already-verified second.LastCommit. A malicious
// peer could provide a valid block and LastCommit but a corrupted
// ExtendedCommit with wrong (but correctly sized) addresses.
func TestSameLengthAddressCorruption_PanicsOnRestart(t *testing.T) {
	const chainID = "test_chain_id"
	height := int64(3)
	round := int32(1)

	// Create a valid ExtendedCommit.
	blockID := makeBlockIDRandom()
	voteSet, valSet, vals := randVoteSet(height, round, cmtproto.PrecommitType, 4, 1, true)
	extCommit, err := MakeExtCommit(blockID, height, round, voteSet, vals, time.Now(), true)
	require.NoError(t, err)

	// Replace the first signature's ValidatorAddress with a different 20-byte
	// address. This keeps the length valid.
	poisoned := *extCommit
	sigs := make([]ExtendedCommitSig, len(extCommit.ExtendedSignatures))
	copy(sigs, extCommit.ExtendedSignatures)
	poisoned.ExtendedSignatures = sigs

	fakeAddr := make(crypto.Address, crypto.AddressSize)
	for i := range fakeAddr {
		fakeAddr[i] = 0xFF
	}
	poisoned.ExtendedSignatures[0].ValidatorAddress = fakeAddr

	// The poisoned ExtendedCommit passes ValidateBasic because the address
	// is the correct length.
	require.NoError(t, poisoned.ValidateBasic(),
		"same-length corrupted address should pass ValidateBasic")

	// EnsureExtensions also passes because it only checks extension presence.
	require.NoError(t, poisoned.EnsureExtensions(true),
		"same-length corrupted address should pass EnsureExtensions")

	// Proto round-trip also succeeds (this is the deserialization path).
	proto := poisoned.ToProto()
	recovered, err := ExtendedCommitFromProto(proto)
	require.NoError(t, err,
		"same-length corrupted address should survive proto round-trip")

	// But ToExtendedVoteSet panics because AddVote checks that the address
	// matches the validator at that index. This is the same code path that
	// runs in consensus/state.go:reconstructLastCommit on node restart.
	assert.Panics(t, func() {
		recovered.ToExtendedVoteSet(chainID, valSet)
	}, "ToExtendedVoteSet must panic on address mismatch — "+
		"a poisoned ExtendedCommit in the blockstore causes a permanent node crash on restart")
}

// TestExtendedCommitHashDetectsAddressCorruption verifies that comparing
// extCommit.ToCommit().Hash() against the verified LastCommit.Hash() detects
// ValidatorAddress corruption. This is the validation added in
// blocksync/reactor.go to cross-validate the ExtendedCommit before persisting.
func TestExtendedCommitHashDetectsAddressCorruption(t *testing.T) {
	height := int64(3)
	round := int32(1)

	blockID := makeBlockIDRandom()
	voteSet, _, vals := randVoteSet(height, round, cmtproto.PrecommitType, 4, 1, true)
	extCommit, err := MakeExtCommit(blockID, height, round, voteSet, vals, time.Now(), true)
	require.NoError(t, err)

	// The honest commit derived from the ExtendedCommit.
	honestCommit := extCommit.ToCommit()

	// Corrupt the ExtendedCommit's first ValidatorAddress with a same-length
	// but different address.
	poisoned := *extCommit
	sigs := make([]ExtendedCommitSig, len(extCommit.ExtendedSignatures))
	copy(sigs, extCommit.ExtendedSignatures)
	poisoned.ExtendedSignatures = sigs

	fakeAddr := make(crypto.Address, crypto.AddressSize)
	for i := range fakeAddr {
		fakeAddr[i] = 0xFF
	}
	poisoned.ExtendedSignatures[0].ValidatorAddress = fakeAddr

	// Derive a commit from the corrupted ExtendedCommit.
	corruptedCommit := poisoned.ToCommit()

	// The hash comparison catches the corruption because CommitSig includes
	// ValidatorAddress in its serialization.
	require.False(t, bytes.Equal(honestCommit.Hash(), corruptedCommit.Hash()),
		"corrupted ExtendedCommit must produce a different commit hash")

	// Sanity: an uncorrupted ExtendedCommit produces a matching hash.
	uncorruptedCommit := extCommit.ToCommit()
	require.True(t, bytes.Equal(honestCommit.Hash(), uncorruptedCommit.Hash()),
		"uncorrupted ExtendedCommit must produce the same commit hash")
}
