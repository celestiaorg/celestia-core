package statesync

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/light"
	"github.com/cometbft/cometbft/light/provider"
	mockp "github.com/cometbft/cometbft/light/provider/mock"
	lightdb "github.com/cometbft/cometbft/light/store/db"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

// genLightChain generates a chain of signed headers for heights 1..numBlocks
// signed by a single, unchanging validator set.
func genLightChain(
	t *testing.T,
	chainID string,
	numBlocks int64,
	start time.Time,
) (map[int64]*types.SignedHeader, map[int64]*types.ValidatorSet) {
	t.Helper()
	vals, privVals := types.RandValidatorSet(4, 10)

	headers := make(map[int64]*types.SignedHeader, numBlocks)
	valSets := make(map[int64]*types.ValidatorSet, numBlocks+1)
	var lastBlockID types.BlockID
	for height := int64(1); height <= numBlocks; height++ {
		header := &types.Header{
			Version:            cmtversion.Consensus{Block: version.BlockProtocol, App: 0},
			ChainID:            chainID,
			Height:             height,
			Time:               start.Add(time.Duration(height) * time.Minute),
			LastBlockID:        lastBlockID,
			ValidatorsHash:     vals.Hash(),
			NextValidatorsHash: vals.Hash(),
			DataHash:           types.Txs{}.Hash(),
			AppHash:            tmhash.Sum([]byte("app_hash")),
			ConsensusHash:      tmhash.Sum([]byte("cons_hash")),
			LastResultsHash:    tmhash.Sum([]byte("results_hash")),
			ProposerAddress:    vals.Validators[0].Address,
		}
		blockID := types.BlockID{
			Hash:          header.Hash(),
			PartSetHeader: types.PartSetHeader{Total: 1, Hash: tmhash.Sum([]byte("parts"))},
		}
		commit, err := test.MakeCommit(blockID, height, 0, vals, privVals, chainID, header.Time.Add(time.Second))
		require.NoError(t, err)

		headers[height] = &types.SignedHeader{Header: header, Commit: commit}
		valSets[height] = vals
		lastBlockID = blockID
	}
	valSets[numBlocks+1] = vals
	return headers, valSets
}

func newTestStateProvider(
	t *testing.T,
	chainID string,
	trustedHeader *types.SignedHeader,
	primary provider.Provider,
	witnesses ...provider.Provider,
) *lightClientStateProvider {
	t.Helper()
	lc, err := light.NewClient(
		context.Background(),
		chainID,
		light.TrustOptions{
			Period: 24 * time.Hour,
			Height: trustedHeader.Height,
			Hash:   trustedHeader.Hash(),
		},
		primary,
		witnesses,
		lightdb.New(dbm.NewMemDB(), ""),
		light.Logger(log.TestingLogger()),
	)
	require.NoError(t, err)
	return &lightClientStateProvider{lc: lc}
}

func TestLightClientStateProviderCommitVerifiesBackwardsCommit(t *testing.T) {
	const (
		chainID      = "test-chain"
		numBlocks    = 4
		targetHeight = 1
	)
	headers, valSets := genLightChain(t, chainID, numBlocks, time.Now().Add(-time.Hour))

	t.Run("valid commit is returned", func(t *testing.T) {
		primary := mockp.New(chainID, headers, valSets)
		witness := mockp.New(chainID, headers, valSets)
		sp := newTestStateProvider(t, chainID, headers[numBlocks], primary, witness)

		commit, err := sp.Commit(context.Background(), targetHeight)
		require.NoError(t, err)
		require.Equal(t, headers[targetHeight].Commit, commit)
	})

	// tamperCommit returns a copy of the target height's commit with the first
	// commit signature modified by tamper.
	tamperCommit := func(tamper func(sig *types.CommitSig)) map[int64]*types.SignedHeader {
		target := headers[targetHeight]
		badCommit := *target.Commit
		badCommit.Signatures = make([]types.CommitSig, len(target.Commit.Signatures))
		copy(badCommit.Signatures, target.Commit.Signatures)
		tamper(&badCommit.Signatures[0])

		badHeaders := make(map[int64]*types.SignedHeader, len(headers))
		for h, sh := range headers {
			badHeaders[h] = sh
		}
		badHeaders[targetHeight] = &types.SignedHeader{Header: target.Header, Commit: &badCommit}
		// The tampered commit must still pass structural validation.
		require.NoError(t, badHeaders[targetHeight].ValidateBasic(chainID))
		return badHeaders
	}

	// The primary serves the authentic header at the target height but with a
	// commit that does not verify against the validator set.
	testCases := []struct {
		name   string
		tamper func(sig *types.CommitSig)
	}{
		{
			name: "wrong validator address",
			tamper: func(sig *types.CommitSig) {
				addr := make([]byte, len(sig.ValidatorAddress))
				copy(addr, sig.ValidatorAddress)
				addr[0] ^= 0x01
				sig.ValidatorAddress = addr
			},
		},
		{
			name: "wrong signature",
			tamper: func(sig *types.CommitSig) {
				signature := make([]byte, len(sig.Signature))
				copy(signature, sig.Signature)
				signature[0] ^= 0x01
				sig.Signature = signature
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name+": honest witness replaces primary", func(t *testing.T) {
			badHeaders := tamperCommit(tc.tamper)
			primary := mockp.New(chainID, badHeaders, valSets)
			// The light client needs a witness left over after replacing the
			// primary, so give it two honest ones.
			witness1 := mockp.New(chainID, headers, valSets)
			witness2 := mockp.New(chainID, headers, valSets)
			sp := newTestStateProvider(t, chainID, headers[numBlocks], primary, witness1, witness2)

			commit, err := sp.Commit(context.Background(), targetHeight)
			require.NoError(t, err)
			require.Equal(t, headers[targetHeight].Commit, commit)
		})

		t.Run(tc.name+": rejected without honest witness", func(t *testing.T) {
			badHeaders := tamperCommit(tc.tamper)
			primary := mockp.New(chainID, badHeaders, valSets)
			witness := mockp.New(chainID, badHeaders, valSets)
			sp := newTestStateProvider(t, chainID, headers[numBlocks], primary, witness)

			_, err := sp.Commit(context.Background(), targetHeight)
			require.Error(t, err)
		})
	}
}
