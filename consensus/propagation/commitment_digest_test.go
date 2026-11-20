package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// signCompactBlockFull signs cb in the format used before app version 12.
func signCompactBlockFull(t *testing.T, cb *proptypes.CompactBlock, signer types.MockPV) []byte {
	signBytes, err := cb.SignBytes()
	require.NoError(t, err)
	sig, err := signer.SignRawBytes(TestChainID, CompactBlockUID, signBytes)
	require.NoError(t, err)
	return sig
}

// signCompactBlockDigest signs cb in the format used from app version 12.
func signCompactBlockDigest(t *testing.T, cb *proptypes.CompactBlock, signer types.MockPV) []byte {
	signBytes, err := cb.DigestSignBytes()
	require.NoError(t, err)
	sig, err := signer.SignRawBytes(TestChainID, CompactBlockDigestUID, signBytes)
	require.NoError(t, err)
	return sig
}

// withTamperedParityPartHash returns a copy of cb whose last parity part hash
// is changed. Every signed field, and the signature, is kept.
func withTamperedParityPartHash(cb *proptypes.CompactBlock) *proptypes.CompactBlock {
	partsHashes := make([][]byte, len(cb.PartsHashes))
	copy(partsHashes, cb.PartsHashes)

	last := len(partsHashes) - 1
	tamperedHash := make([]byte, len(partsHashes[last]))
	copy(tamperedHash, partsHashes[last])
	tamperedHash[0] ^= 0xFF
	partsHashes[last] = tamperedHash

	return &proptypes.CompactBlock{
		Proposal:    cb.Proposal,
		LastLen:     cb.LastLen,
		BpHash:      cb.BpHash,
		Blobs:       cb.Blobs,
		PartsHashes: partsHashes,
		Signature:   cb.Signature,
	}
}

// TestNewCompactBlockSignPayload ensures the signature format switches from
// the full sign bytes to the digest at app version 12.
func TestNewCompactBlockSignPayload(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })
	cb, _, _, _ := testCompactBlock(t, sm, pv, 1, 0)

	fullSignBytes, err := cb.SignBytes()
	require.NoError(t, err)
	digestSignBytes, err := cb.DigestSignBytes()
	require.NoError(t, err)

	testCases := []struct {
		name              string
		appVersion        uint64
		expectedUniqueID  string
		expectedSignBytes []byte
	}{
		{"app version 11 signs the full sign bytes", 11, CompactBlockUID, fullSignBytes},
		{"app version 12 signs the digest", 12, CompactBlockDigestUID, digestSignBytes},
		{"app version 13 signs the digest", 13, CompactBlockDigestUID, digestSignBytes},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			signPayload, err := newCompactBlockSignPayload(cb, tc.appVersion)
			require.NoError(t, err)
			require.Equal(t, tc.expectedUniqueID, signPayload.uniqueID)
			require.Equal(t, tc.expectedSignBytes, signPayload.signBytes)
		})
	}
}

// TestValidateCompactBlockGatesSignatureFormat ensures a node only accepts
// the signature format of its app version, so that old and new formats are
// never both valid at one height.
func TestValidateCompactBlockGatesSignatureFormat(t *testing.T) {
	reactors, _ := createTestReactors(1, defaultTestP2PConf(), false, "")
	r := reactors[0]

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	signer := types.NewMockPV()
	r.SetConsensusState(1, 0, signer.PrivKey.PubKey())

	cb := signedCompactBlock(t, sm, pv, signer, 1, 0)
	fullSignature := signCompactBlockFull(t, cb, signer)
	digestSignature := signCompactBlockDigest(t, cb, signer)

	r.SetAppVersion(11)
	cb.Signature = fullSignature
	require.NoError(t, r.validateCompactBlock(cb))
	cb.Signature = digestSignature
	require.ErrorContains(t, r.validateCompactBlock(cb), "invalid signature")

	r.SetAppVersion(12)
	cb.Signature = digestSignature
	require.NoError(t, r.validateCompactBlock(cb))
	cb.Signature = fullSignature
	require.ErrorContains(t, r.validateCompactBlock(cb), "invalid signature")
}

// TestProposeBlockSignsDigestFromAppVersion12 ensures the proposer sends a
// small digest to its signer from app version 12, however many transactions
// the block has.
func TestProposeBlockSignsDigestFromAppVersion12(t *testing.T) {
	reactors, _ := createTestReactors(1, defaultTestP2PConf(), false, "")
	proposer := reactors[0]
	proposer.SetAppVersion(12)

	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	prop, partSet, _, metaData := createTestProposal(t, sm, pv, 1, 0, 500, 1000)
	require.NoError(t, proposer.ProposeBlock(prop, partSet, metaData))

	cb, _, _, has := proposer.getAllState(1, 0, false)
	require.True(t, has)

	digestSignBytes, err := cb.DigestSignBytes()
	require.NoError(t, err)
	require.Less(t, len(digestSignBytes), 512)

	signedBytes, err := types.RawBytesMessageSignBytes(TestChainID, CompactBlockDigestUID, digestSignBytes)
	require.NoError(t, err)
	require.True(t, mockPubKey.VerifySignature(signedBytes, cb.Signature))
}

// TestHandleCompactBlockRejectsTamperedPartsHashes ensures a compact block
// with a valid digest signature but a changed part hash is not applied. The
// digest signature does not cover the part hashes, so the signed roots must
// catch it.
func TestHandleCompactBlockRejectsTamperedPartsHashes(t *testing.T) {
	reactors, _ := createTestReactors(2, defaultTestP2PConf(), false, "")
	receiver, tamperer := reactors[0], reactors[1]
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	receiver.SetAppVersion(12)
	receiver.SetConsensusState(2, 0, mockPubKey)

	cb, _, _, _ := testCompactBlock(t, sm, pv, 2, 0)
	cb.Signature = signCompactBlockDigest(t, cb, mockPrivVal)
	tampered := withTamperedParityPartHash(cb)

	// the signature still verifies, and only the root check fails
	require.NoError(t, receiver.validateCompactBlock(tampered))
	_, err := tampered.Proofs()
	require.ErrorContains(t, err, "parity root")

	receiver.handleCompactBlock(tampered, tamperer.self, false)
	_, _, has := receiver.GetProposal(2, 0)
	require.False(t, has, "compact block with a tampered part hash must not be applied")
}

// TestCachedTamperedPartsHashesGrantNoPushAllowance ensures a peer whose
// cached compact block has a valid digest signature but a changed part hash
// is not allowed to push parts.
func TestCachedTamperedPartsHashesGrantNoPushAllowance(t *testing.T) {
	reactors, _ := createTestReactors(3, defaultTestP2PConf(), false, "")
	receiver, sender, tamperer := reactors[0], reactors[1], reactors[2]
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	receiver.SetAppVersion(12)
	receiver.SetConsensusState(1, 0, mockPubKey)

	cb, _, _, _ := testCompactBlock(t, sm, pv, 2, 0)
	cb.Signature = signCompactBlockDigest(t, cb, mockPrivVal)
	tampered := withTamperedParityPartHash(cb)

	// both cached while behind, then replayed on reaching height 2
	receiver.handleCompactBlock(cb, sender.self, false)
	receiver.handleCompactBlock(tampered, tamperer.self, false)
	receiver.Prune(1)
	receiver.SetConsensusState(2, 0, mockPubKey)

	_, _, has := receiver.GetProposal(2, 0)
	require.True(t, has)
	require.True(t, receiver.getPeer(sender.self).ConsumePushAllowance(2, 0, 0))
	require.False(t, receiver.getPeer(tamperer.self).ConsumePushAllowance(2, 0, 0))
}
