package coregrpc

import (
	"context"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	core "github.com/cometbft/cometbft/rpc/core"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

type blockAPI struct {
}

func (b *blockAPI) BlockByHash(ctx context.Context, req *RequestBlockByHash) (*cmtproto.Block, error) {
	res, err := core.BlockByHash(&rpctypes.Context{}, req.Hash)
	if err != nil {
		return nil, err
	}

	return res.Block.ToProto()
}
