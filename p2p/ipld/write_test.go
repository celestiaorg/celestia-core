package ipld

import (
	"context"
	mrand "math/rand"
	"testing"
	"time"

	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/lazyledger/nmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	abci "github.com/lazyledger/lazyledger-core/abci/types"
	"github.com/lazyledger/lazyledger-core/crypto/tmhash"
	"github.com/lazyledger/lazyledger-core/ipfs"
	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/libs/log"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/lazyledger-core/types/consts"
)

func TestPutBlock(t *testing.T) {
	logger := log.TestingLogger()
	dag := mdutils.Mock()
	croute := ipfs.MockRouting()

	maxOriginalSquareSize := consts.MaxSquareSize / 2
	maxShareCount := maxOriginalSquareSize * maxOriginalSquareSize

	testCases := []struct {
		name      string
		blockData types.Data
		expectErr bool
		errString string
	}{
		{"no leaves", generateRandomMsgOnlyData(0), false, ""},
		{"single leaf", generateRandomMsgOnlyData(1), false, ""},
		{"16 leaves", generateRandomMsgOnlyData(16), false, ""},
		{"max square size", generateRandomMsgOnlyData(maxShareCount), false, ""},
	}
	ctx := context.Background()
	for _, tc := range testCases {
		tc := tc

		block := &types.Block{Data: tc.blockData}

		t.Run(tc.name, func(t *testing.T) {
			err := PutBlock(ctx, dag, block, croute, logger)
			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errString)
				return
			}

			require.NoError(t, err)

			timeoutCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			block.Hash()
			for _, rowRoot := range block.DataAvailabilityHeader.RowsRoots.Bytes() {
				// recreate the cids using only the computed roots
				cid, err := plugin.CidFromNamespacedSha256(rowRoot)
				if err != nil {
					t.Error(err)
				}

				// retrieve the data from IPFS
				_, err = dag.Get(timeoutCtx, cid)
				if err != nil {
					t.Errorf("Root not found: %s", cid.String())
				}
			}
		})
	}
}

type preprocessingApp struct {
	abci.BaseApplication
}

func (app *preprocessingApp) PreprocessTxs(
	req abci.RequestPreprocessTxs) abci.ResponsePreprocessTxs {
	time.Sleep(time.Second * 2)
	randTxs := generateRandTxs(64, 256)
	randMsgs := generateRandNamespacedRawData(128, nmt.DefaultNamespaceIDLen, 256)
	randMessages := toMessageSlice(randMsgs)
	return abci.ResponsePreprocessTxs{
		Txs:      append(req.Txs, randTxs...),
		Messages: &tmproto.Messages{MessagesList: randMessages},
	}
}
func generateRandTxs(num int, size int) [][]byte {
	randMsgs := generateRandNamespacedRawData(num, nmt.DefaultNamespaceIDLen, size)
	for _, msg := range randMsgs {
		copy(msg[:nmt.DefaultNamespaceIDLen], consts.TxNamespaceID)
	}
	return randMsgs
}

func toMessageSlice(msgs [][]byte) []*tmproto.Message {
	res := make([]*tmproto.Message, len(msgs))
	for i := 0; i < len(msgs); i++ {
		res[i] = &tmproto.Message{NamespaceId: msgs[i][:nmt.DefaultNamespaceIDLen], Data: msgs[i][nmt.DefaultNamespaceIDLen:]}
	}
	return res
}

func TestDataAvailabilityHeaderRewriteBug(t *testing.T) {
	logger := log.TestingLogger()
	dag := mdutils.Mock()
	croute := ipfs.MockRouting()

	txs := types.Txs{}
	l := len(txs)
	bzs := make([][]byte, l)
	for i := 0; i < l; i++ {
		bzs[i] = txs[i]
	}
	app := &preprocessingApp{}

	// See state.CreateProposalBlock to understand why we do this here:
	processedBlockTxs := app.PreprocessTxs(abci.RequestPreprocessTxs{Txs: bzs})
	ppt := processedBlockTxs.GetTxs()

	pbmessages := processedBlockTxs.GetMessages()

	lp := len(ppt)
	processedTxs := make(types.Txs, lp)
	if lp > 0 {
		for i := 0; i < l; i++ {
			processedTxs[i] = ppt[i]
		}
	}

	messages := types.MessagesFromProto(pbmessages)
	lastID := makeBlockIDRandom()
	h := int64(3)

	voteSet, _, vals := randVoteSet(h-1, 1, tmproto.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals, time.Now())
	assert.NoError(t, err)
	block := types.MakeBlock(1, processedTxs, nil, nil, messages, commit)
	block.Hash()

	hash1 := block.DataAvailabilityHeader.Hash()

	ctx := context.TODO()
	err = PutBlock(ctx, dag, block, croute, logger)
	if err != nil {
		t.Fatal(err)
	}

	block.Hash()
	hash2 := block.DataAvailabilityHeader.Hash()
	assert.Equal(t, hash1, hash2)

}

func generateRandomMsgOnlyData(msgCount int) types.Data {
	out := make([]types.Message, msgCount)
	for i, msg := range generateRandNamespacedRawData(msgCount, consts.NamespaceSize, consts.MsgShareSize-2) {
		out[i] = types.Message{NamespaceID: msg[:consts.NamespaceSize], Data: msg[consts.NamespaceSize:]}
	}
	return types.Data{
		Messages: types.Messages{MessagesList: out},
	}
}

func makeBlockIDRandom() types.BlockID {
	var (
		blockHash   = make([]byte, tmhash.Size)
		partSetHash = make([]byte, tmhash.Size)
	)
	mrand.Read(blockHash)
	mrand.Read(partSetHash)
	return types.BlockID{
		Hash: blockHash,
		PartSetHeader: types.PartSetHeader{
			Total: 123,
			Hash:  partSetHash,
		},
	}
}

func randVoteSet(
	height int64,
	round int32,
	signedMsgType tmproto.SignedMsgType,
	numValidators int,
	votingPower int64,
) (*types.VoteSet, *types.ValidatorSet, []types.PrivValidator) {
	valSet, privValidators := types.RandValidatorSet(numValidators, votingPower)
	return types.NewVoteSet("test_chain_id", height, round, signedMsgType, valSet), valSet, privValidators
}
