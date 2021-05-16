package ipld

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-ipfs/core/coreapi"
	coremock "github.com/ipfs/go-ipfs/core/mock"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/types"
)

func TestPutBlock(t *testing.T) {
	ipfsNode, err := coremock.NewMockNode()
	if err != nil {
		t.Error(err)
	}

	ipfsAPI, err := coreapi.NewCoreAPI(ipfsNode)
	if err != nil {
		t.Error(err)
	}

	maxOriginalSquareSize := types.MaxSquareSize / 2
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
			err = PutBlock(ctx, ipfsAPI.Dag(), block)
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
				_, err = ipfsAPI.Dag().Get(timeoutCtx, cid)
				if err != nil {
					t.Errorf("Root not found: %s", cid.String())
				}
			}
		})
	}
}

func generateRandomMsgOnlyData(msgCount int) types.Data {
	out := make([]types.Message, msgCount)
	for i, msg := range generateRandNamespacedRawData(msgCount, types.NamespaceSize, types.MsgShareSize-2) {
		out[i] = types.Message{NamespaceID: msg[:types.NamespaceSize], Data: msg[types.NamespaceSize:]}
	}
	return types.Data{
		Messages: types.Messages{MessagesList: out},
	}
}
