package blocksync

import (
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// The blocksync channel should wire up its precheck, and that precheck should
// reject block responses carrying too many signatures.
func TestBlocksyncChannelPrecheck(t *testing.T) {
	blockSyncCh := (&Reactor{}).GetChannels()
	require.NotNil(t, blockSyncCh[0], "blocksync channel descriptor not found")
	require.NotNil(t, blockSyncCh[0].RecvMessagePrecheck, "blocksync channel must set RecvMessagePrecheck")

	// The wired precheck must enforce the bound: oversized rejected, normal allowed.
	err := blockSyncCh[0].RecvMessagePrecheck(blockResponseBytes(t, types.MaxVotesCount+1, 0))
	require.ErrorIs(t, err, errTooManySignatures)
	require.NoError(t, blockSyncCh[0].RecvMessagePrecheck(blockResponseBytes(t, 10, 0)))
}

func TestValidateBlockSyncBytes(t *testing.T) {
	tests := []struct {
		name     string
		nSigs    int
		nExtSigs int
		wantErr  bool
	}{
		{"empty", 0, 0, false},
		{"under limit", 100, 100, false},
		{"at limit", types.MaxVotesCount, types.MaxVotesCount, false},
		{"commit over limit", types.MaxVotesCount + 1, 0, true},
		{"ext commit over limit", 0, types.MaxVotesCount + 1, true},
		{"both over limit", types.MaxVotesCount + 1, types.MaxVotesCount + 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bz := blockResponseBytes(t, tc.nSigs, tc.nExtSigs)
			err := validateBlockSyncBytes(bz)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Count signatures straight off the wire with an explicit limit: count == limit
// is fine, count > limit is rejected.
func TestCheckSignatureCount(t *testing.T) {
	tests := []struct {
		name        string
		nSigs       int
		limit       int
		wantTooMany bool
	}{
		{"none", 0, 5, false},
		{"under limit", 4, 5, false},
		{"at limit", 5, 5, false},
		{"one over limit", 6, 5, true},
		{"far over limit", 100, 5, true},
		{"at MaxVotesCount", types.MaxVotesCount, types.MaxVotesCount, false},
		{"over MaxVotesCount", types.MaxVotesCount + 1, types.MaxVotesCount, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// last_commit.signatures path
			bz := blockResponseBytes(t, tc.nSigs, 0)
			err := checkSignatureCount(bz, lastCommitSigPath, commitSignaturesField, tc.limit)
			if tc.wantTooMany {
				require.ErrorIs(t, err, errTooManySignatures)
			} else {
				require.NoError(t, err)
			}

			// ext_commit.extended_signatures path
			bz = blockResponseBytes(t, 0, tc.nSigs)
			err = checkSignatureCount(bz, extendedCommitSigPath, extCommitSignaturesField, tc.limit)
			if tc.wantTooMany {
				require.ErrorIs(t, err, errTooManySignatures)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Signatures only count on their own path: hunting for commit sigs down the
// ext-commit path (or vice versa) finds none.
func TestCheckSignatureCount_WrongPathNotCounted(t *testing.T) {
	// 50 commit sigs, 0 extended sigs. The external path should count 0.
	bz := blockResponseBytes(t, 50, 0)
	require.NoError(t, checkSignatureCount(bz, extendedCommitSigPath, extCommitSignaturesField, 1))
	// And the commit path should reject them against a limit of 1.
	require.ErrorIs(t, checkSignatureCount(bz, lastCommitSigPath, commitSignaturesField, 1), errTooManySignatures)
}

// Garbage bytes come back as a parse error, not a count error — and never panic.
func TestCheckSignatureCount_Malformed(t *testing.T) {
	require.NotPanics(t, func() {
		err := checkSignatureCount([]byte{0xff, 0xff, 0xff}, lastCommitSigPath, commitSignaturesField, 5)
		require.Error(t, err)
		require.NotErrorIs(t, err, errTooManySignatures)
	})
}

// Other message types have no signatures to bound, so they always pass.
func TestValidateBlockSyncBytes_OtherMessages(t *testing.T) {
	msgs := []*bcproto.Message{
		{Sum: &bcproto.Message_BlockRequest{BlockRequest: &bcproto.BlockRequest{Height: 5}}},
		{Sum: &bcproto.Message_NoBlockResponse{NoBlockResponse: &bcproto.NoBlockResponse{Height: 5}}},
		{Sum: &bcproto.Message_StatusRequest{StatusRequest: &bcproto.StatusRequest{}}},
		{Sum: &bcproto.Message_StatusResponse{StatusResponse: &bcproto.StatusResponse{Height: 5, Base: 1}}},
	}
	for _, m := range msgs {
		bz, err := proto.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, validateBlockSyncBytes(bz))
	}
}

// Garbage bytes are rejected; empty input is a valid (empty) Message.
func TestValidateBlockSyncBytes_Malformed(t *testing.T) {
	require.NoError(t, validateBlockSyncBytes(nil))

	require.Error(t, validateBlockSyncBytes([]byte{0xff, 0xff, 0xff})) // truncated varint tag
	require.Error(t, validateBlockSyncBytes([]byte{0x1a, 0x05}))       // field 3, len 5, but no payload
}

// Splitting signatures across two last_commits still sums — you can't dodge the
// limit by spreading the field out.
func TestValidateBlockSyncBytes_MergedCommits(t *testing.T) {
	half := types.MaxVotesCount/2 + 1 // two halves exceed the limit
	one := blockResponseBytes(t, half, 0)
	// Concatenating two valid encodings merges them at decode time.
	merged := append(append([]byte{}, one...), one...)
	require.Error(t, validateBlockSyncBytes(merged))
}

// blockResponseBytes builds a blocksync Message holding a BlockResponse with
// numCommitSigs commit signatures and numExtCommitSigs extended-commit
// signatures (all empty).
func blockResponseBytes(t *testing.T, numCommitSigs, numExtCommitSigs int) []byte {
	t.Helper()

	signatures := make([]cmtproto.CommitSig, numCommitSigs)
	for i := range signatures {
		signatures[i] = cmtproto.CommitSig{Timestamp: time.Unix(0, 0)}
	}
	extendedSignatures := make([]cmtproto.ExtendedCommitSig, numExtCommitSigs)
	for i := range extendedSignatures {
		extendedSignatures[i] = cmtproto.ExtendedCommitSig{Timestamp: time.Unix(0, 0)}
	}

	message := &bcproto.Message{
		Sum: &bcproto.Message_BlockResponse{
			BlockResponse: &bcproto.BlockResponse{
				Block: &cmtproto.Block{
					LastCommit: &cmtproto.Commit{Signatures: signatures},
				},
				ExtCommit: &cmtproto.ExtendedCommit{ExtendedSignatures: extendedSignatures},
			},
		},
	}
	encoded, err := proto.Marshal(message)
	require.NoError(t, err)
	return encoded
}
