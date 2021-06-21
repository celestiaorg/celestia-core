package ipld

import (
	"context"
	"testing"
	"time"

	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
)

func TestPutBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dag := mdutils.Mock()

	testCases := []struct {
		name       string
		squareSize int
	}{
		{"1x1(min)", 1},
		{"32x32(med)", 32},
		{"128x128(max)", MaxSquareSize},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			eds, err := PutData(ctx, RandNamespacedShares(t, tc.squareSize*tc.squareSize), dag)
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			for _, rowRoot := range MakeDataHeader(eds).RowsRoots.Bytes() {
				// recreate the cids using only the computed roots
				cid, err := plugin.CidFromNamespacedSha256(rowRoot)
				require.NoError(t, err)

				// retrieve the data from IPFS
				_, err = dag.Get(ctx, cid)
				require.NoError(t, err)
			}
		})
	}
}

// TODO(Wondertan): Find a proper place for this test
// type preprocessingApp struct {
// 	abci.BaseApplication
// }
//
// func (app *preprocessingApp) PreprocessTxs(
// 	req abci.RequestPreprocessTxs) abci.ResponsePreprocessTxs {
// 	time.Sleep(time.Second * 2)
// 	randTxs := generateRandTxs(64, 256)
// 	randMsgs := generateRandNamespacedRawData(128, nmt.DefaultNamespaceIDLen, 256)
// 	randMessages := toMessageSlice(randMsgs)
// 	return abci.ResponsePreprocessTxs{
// 		Txs:      append(req.Txs, randTxs...),
// 		Messages: &tmproto.Messages{MessagesList: randMessages},
// 	}
// }
// func generateRandTxs(num int, size int) [][]byte {
// 	randMsgs := generateRandNamespacedRawData(num, nmt.DefaultNamespaceIDLen, size)
// 	for _, msg := range randMsgs {
// 		copy(msg[:nmt.DefaultNamespaceIDLen], consts.TxNamespaceID)
// 	}
// 	return randMsgs
// }
//
// func toMessageSlice(msgs [][]byte) []*tmproto.Message {
// 	res := make([]*tmproto.Message, len(msgs))
// 	for i := 0; i < len(msgs); i++ {
// 		res[i] = &tmproto.Message{
// 			NamespaceId: msgs[i][:nmt.DefaultNamespaceIDLen],
// 			Data: msgs[i][nmt.DefaultNamespaceIDLen:],
// 		}
// 	}
// 	return res
// }
//
// func TestDataAvailabilityHeaderRewriteBug(t *testing.T) {
// 	ctx := context.Background()
// 	dag := mdutils.Mock()
//
// 	txs := types.Txs{}
// 	l := len(txs)
// 	bzs := make([][]byte, l)
// 	for i := 0; i < l; i++ {
// 		bzs[i] = txs[i]
// 	}
// 	app := &preprocessingApp{}
//
// 	// See state.CreateProposalBlock to understand why we do this here:
// 	processedBlockTxs := app.PreprocessTxs(abci.RequestPreprocessTxs{Txs: bzs})
// 	ppt := processedBlockTxs.GetTxs()
//
// 	pbmessages := processedBlockTxs.GetMessages()
//
// 	lp := len(ppt)
// 	processedTxs := make(types.Txs, lp)
// 	if lp > 0 {
// 		for i := 0; i < l; i++ {
// 			processedTxs[i] = ppt[i]
// 		}
// 	}
//
// 	messages := types.MessagesFromProto(pbmessages)
// 	lastID := makeBlockIDRandom()
// 	h := int64(3)
//
// 	voteSet, _, vals := randVoteSet(h-1, 1, tmproto.PrecommitType, 10, 1)
// 	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals, time.Now())
// 	assert.NoError(t, err)
// 	block := types.MakeBlock(1, processedTxs, nil, nil, messages, commit)
// 	block.Hash()
//
// 	hash1 := block.DataAvailabilityHeader.Hash()
//
// 	_, dah, _, err := PutData(ctx, block, dag)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
//
// 	block.Hash()
// 	hash2 := block.DataAvailabilityHeader.Hash()
// 	assert.Equal(t, hash1, hash2)
// }
//
// func makeBlockIDRandom() types.BlockID {
// 	var (
// 		blockHash   = make([]byte, tmhash.Size)
// 		partSetHash = make([]byte, tmhash.Size)
// 	)
// 	mrand.Read(blockHash)
// 	mrand.Read(partSetHash)
// 	return types.BlockID{
// 		Hash: blockHash,
// 		PartSetHeader: types.PartSetHeader{
// 			Total: 123,
// 			Hash:  partSetHash,
// 		},
// 	}
// }
//
// func randVoteSet(
// 	height int64,
// 	round int32,
// 	signedMsgType tmproto.SignedMsgType,
// 	numValidators int,
// 	votingPower int64,
// ) (*types.VoteSet, *types.ValidatorSet, []types.PrivValidator) {
// 	valSet, privValidators := types.RandValidatorSet(numValidators, votingPower)
// 	return types.NewVoteSet("test_chain_id", height, round, signedMsgType, valSet), valSet, privValidators
// }
