package coregrpc

import (
	"context"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/rpc/core"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	types2 "github.com/tendermint/tendermint/types"
	"log"
	"sync"
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

type blockAPI struct {
	sync.Mutex
	heightListeners map[chan int64]struct{}
}

func newBlockAPI() *blockAPI {
	return &blockAPI{
		// TODO(rach-id) make 1000 configurable if there is a need for it
		heightListeners: make(map[chan int64]struct{}, 1000),
	}
}

func (blockAPI *blockAPI) listenForHeights() {
	env := core.GetEnvironment()
	newBlocksChannel, err := env.EventBus.Subscribe(context.Background(), "grpc-listener", types2.EventQueryNewBlock, 500)
	if err != nil {
		log.Fatalf("Failed to subscribe to new blocks: %v", err)
	}
	for {
		select {
		case <-newBlocksChannel.Cancelled():
			env.Logger.Error("cancelled grpc subscription")
			// TODO(rach-id): maybe retry to connect if users want this functionality
			return
		case event := <-newBlocksChannel.Out():
			newBlockEvent, ok := event.Events()[types2.EventTypeKey]
			if !ok || len(newBlockEvent) == 0 || newBlockEvent[0] != types2.EventNewBlock {
				continue
			}
			data, ok := event.Data().(types2.EventDataNewBlock)
			if !ok {
				env.Logger.Debug("couldn't cast event data to new block")
				continue
			}
			blockAPI.broadcastToListeners(data.Block.Height)
		}
	}

}

func (blockAPI *blockAPI) broadcastToListeners(height int64) {
	for ch := range blockAPI.heightListeners {
		select {
		case ch <- height:
		default:
		}
	}
}

func (blockAPI *blockAPI) addHeightListener() chan int64 {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	ch := make(chan int64, 50)
	blockAPI.heightListeners[ch] = struct{}{}
	return ch
}

func (blockAPI *blockAPI) removeHeightListener(ch chan int64) {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	delete(blockAPI.heightListeners, ch)
	close(ch)
}

func (blockAPI *blockAPI) BlockByHash(req *BlockByHashRequest, stream BlockAPI_BlockByHashServer) error {
	// Question: do you want just the block, or you want the response to be similar to what
	// the RPC endpoint returns: a ResultBlock?
	block, err := core.GetEnvironment().BlockStore.LoadBlockByHash(req.Hash).ToProto()
	if err != nil {
		return err
	}
	err = stream.Send(&BlockByHashResponse{Block: block})
	if err != nil {
		return err
	}
	return nil
}

func (blockAPI *blockAPI) BlockByHeight(req *BlockByHeightRequest, stream BlockAPI_BlockByHeightServer) error {
	block, err := core.GetEnvironment().BlockStore.LoadBlock(req.Height).ToProto()
	if err != nil {
		return err
	}
	err = stream.Send(&BlockByHeightResponse{
		Block: block,
	})
	if err != nil {
		return err
	}
	return nil
}

func (blockAPI *blockAPI) Commit(_ context.Context, req *CommitRequest) (*CommitResponse, error) {
	commit := core.GetEnvironment().BlockStore.LoadBlockCommit(req.Height).ToProto()

	return &CommitResponse{
		Height:     commit.Height,
		Round:      commit.Round,
		BlockID:    commit.BlockID,
		Signatures: commit.Signatures,
	}, nil
}

func (blockAPI *blockAPI) ValidatorSet(_ context.Context, req *ValidatorSetRequest) (*ValidatorSetResponse, error) {
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

func (blockAPI *blockAPI) SubscribeNewHeights(_ *SubscribeNewHeightsRequest, stream BlockAPI_SubscribeNewHeightsServer) error {
	heightChan := blockAPI.addHeightListener()
	defer blockAPI.removeHeightListener(heightChan)

	for {
		select {
		case height := <-heightChan:
			event := &NewHeightEvent{
				Height: height,
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}
