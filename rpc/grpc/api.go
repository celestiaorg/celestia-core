package coregrpc

import (
	"context"
	"errors"
	"sync"
	"time"

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/rpc/core"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	types2 "github.com/tendermint/tendermint/types"
)

type broadcastAPI struct {
}

func (bapi *broadcastAPI) Ping(ctx context.Context, req *RequestPing) (*ResponsePing, error) {
	// kvstore so we can check if the server is up
	return &ResponsePing{}, nil
}

func (bapi *broadcastAPI) BroadcastTx(ctx context.Context, req *RequestBroadcastTx) (*ResponseBroadcastTx, error) {
	// NOTE: there's no way to get client's remote address
	// see https://stackoverflow.com/questions/33684570/session-and-remote-ip-address-in-grpc-go
	res, err := core.BroadcastTxCommit(&rpctypes.Context{}, req.Tx)
	if err != nil {
		return nil, err
	}

	return &ResponseBroadcastTx{
		CheckTx: &abci.ResponseCheckTx{
			Code: res.CheckTx.Code,
			Data: res.CheckTx.Data,
			Log:  res.CheckTx.Log,
		},
		DeliverTx: &abci.ResponseDeliverTx{
			Code: res.DeliverTx.Code,
			Data: res.DeliverTx.Data,
			Log:  res.DeliverTx.Log,
		},
	}, nil
}

type BlockAPI struct {
	sync.Mutex
	heightListeners      map[chan NewHeightEvent]struct{}
	newBlockSubscription types2.Subscription
}

func NewBlockAPI() *BlockAPI {
	return &BlockAPI{
		// TODO(rach-id) make 1000 configurable if there is a need for it
		heightListeners: make(map[chan NewHeightEvent]struct{}, 1000),
	}
}

func (blockAPI *BlockAPI) StartNewBlockEventListener(ctx context.Context) {
	env := core.GetEnvironment()
	if blockAPI.newBlockSubscription == nil {
		var err error
		blockAPI.newBlockSubscription, err = env.EventBus.Subscribe(ctx, "new-block-grpc-subscription", types2.EventQueryNewBlock, 500)
		if err != nil {
			env.Logger.Error("Failed to subscribe to new blocks", "err", err)
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-blockAPI.newBlockSubscription.Cancelled():
			env.Logger.Error("cancelled grpc subscription. retrying")
			ok, err := blockAPI.retryNewBlocksSubscription(ctx)
			if err != nil {
				blockAPI.closeAllListeners()
				return
			}
			if !ok {
				// this will happen when the context is done. we can stop here
				return
			}
		case event, ok := <-blockAPI.newBlockSubscription.Out():
			if !ok {
				env.Logger.Error("new blocks subscription closed. re-subscribing")
				ok, err := blockAPI.retryNewBlocksSubscription(ctx)
				if err != nil {
					blockAPI.closeAllListeners()
					return
				}
				if !ok {
					// this will happen when the context is done. we can stop here
					return
				}
				continue
			}
			newBlockEvent, ok := event.Events()[types2.EventTypeKey]
			if !ok || len(newBlockEvent) == 0 || newBlockEvent[0] != types2.EventNewBlock {
				continue
			}
			data, ok := event.Data().(types2.EventDataNewBlock)
			if !ok {
				env.Logger.Debug("couldn't cast event data to new block")
				continue
			}
			blockAPI.broadcastToListeners(ctx, data.Block.Height, data.Block.DataHash)
		}
	}

}

func (blockAPI *BlockAPI) retryNewBlocksSubscription(ctx context.Context) (bool, error) {
	env := core.GetEnvironment()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	blockAPI.Lock()
	defer blockAPI.Unlock()
	for i := 1; i < 6; i++ {
		select {
		case <-ctx.Done():
			return false, nil
		case <-ticker.C:
			var err error
			blockAPI.newBlockSubscription, err = env.EventBus.Subscribe(ctx, "new-block-grpc-subscription", types2.EventQueryNewBlock, 500)
			if err != nil {
				env.Logger.Error("Failed to subscribe to new blocks. retrying", "err", err, "retry_number", i)
			} else {
				return true, nil
			}
		}
	}
	return false, errors.New("couldn't recover from failed blocks subscription. stopping listeners")
}

func (blockAPI *BlockAPI) broadcastToListeners(ctx context.Context, height int64, hash []byte) {
	for ch := range blockAPI.heightListeners {
		select {
		case <-ctx.Done():
			return
		case ch <- NewHeightEvent{Height: height, Hash: hash}:
		}
	}
}

func (blockAPI *BlockAPI) addHeightListener() chan NewHeightEvent {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	ch := make(chan NewHeightEvent, 50)
	blockAPI.heightListeners[ch] = struct{}{}
	return ch
}

func (blockAPI *BlockAPI) removeHeightListener(ch chan NewHeightEvent) {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	delete(blockAPI.heightListeners, ch)
	close(ch)
}

func (blockAPI *BlockAPI) closeAllListeners() {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	for chanel, _ := range blockAPI.heightListeners {
		delete(blockAPI.heightListeners, chanel)
		close(chanel)
	}
}

func (blockAPI *BlockAPI) BlockByHash(req *BlockByHashRequest, stream BlockAPI_BlockByHashServer) error {
	blockStore := core.GetEnvironment().BlockStore
	blockMeta := blockStore.LoadBlockMetaByHash(req.Hash)
	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part, err := blockStore.LoadBlockPart(blockMeta.Header.Height, i).ToProto()
		if err != nil {
			return err
		}
		isLastPart := i == int(blockMeta.BlockID.PartSetHeader.Total)-1
		err = stream.Send(&BlockByHashResponse{
			BlockPart: part,
			IsLast:    isLastPart,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (blockAPI *BlockAPI) BlockByHeight(req *BlockByHeightRequest, stream BlockAPI_BlockByHeightServer) error {
	blockStore := core.GetEnvironment().BlockStore
	blockMeta := blockStore.LoadBlockMeta(req.Height)
	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part, err := blockStore.LoadBlockPart(req.Height, i).ToProto()
		if err != nil {
			return err
		}
		isLastPart := i == int(blockMeta.BlockID.PartSetHeader.Total)-1
		err = stream.Send(&BlockByHeightResponse{
			BlockPart: part,
			IsLast:    isLastPart,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (blockAPI *BlockAPI) BlockMetaByHash(ctx context.Context, req *BlockMetaByHashRequest) (*BlockMetaByHashResponse, error) {
	blockMeta := core.GetEnvironment().BlockStore.LoadBlockMetaByHash(req.Hash).ToProto()
	return &BlockMetaByHashResponse{
		BlockMeta: blockMeta,
	}, nil
}

func (blockAPI *BlockAPI) BlockMetaByHeight(ctx context.Context, req *BlockMetaByHeightRequest) (*BlockMetaByHeightResponse, error) {
	blockMeta := core.GetEnvironment().BlockStore.LoadBlockMeta(req.Height).ToProto()
	return &BlockMetaByHeightResponse{
		BlockMeta: blockMeta,
	}, nil
}

func (blockAPI *BlockAPI) Commit(_ context.Context, req *CommitRequest) (*CommitResponse, error) {
	commit := core.GetEnvironment().BlockStore.LoadBlockCommit(req.Height).ToProto()

	return &CommitResponse{
		Height:     commit.Height,
		Round:      commit.Round,
		BlockID:    commit.BlockID,
		Signatures: commit.Signatures,
	}, nil
}

func (blockAPI *BlockAPI) ValidatorSet(_ context.Context, req *ValidatorSetRequest) (*ValidatorSetResponse, error) {
	validatorSet, err := core.GetEnvironment().StateStore.LoadValidators(req.Height)
	if err != nil {
		return nil, err
	}
	proposer, err := validatorSet.Proposer.ToProto()
	if err != nil {
		return nil, err
	}
	validators := make([]*types.Validator, 0, len(validatorSet.Validators))
	for _, validator := range validatorSet.Validators {
		protoValidator, err := validator.ToProto()
		if err != nil {
			return nil, err
		}
		validators = append(validators, protoValidator)
	}
	return &ValidatorSetResponse{
		Validators:       validators,
		Proposer:         proposer,
		TotalVotingPower: validatorSet.TotalVotingPower(),
	}, nil
}

func (blockAPI *BlockAPI) SubscribeNewHeights(_ *SubscribeNewHeightsRequest, stream BlockAPI_SubscribeNewHeightsServer) error {
	heightListener := blockAPI.addHeightListener()
	defer blockAPI.removeHeightListener(heightListener)

	for {
		select {
		case event, ok := <-heightListener:
			if !ok {
				return errors.New("blocks subscription closed from the service side")
			}
			if err := stream.Send(&event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}
