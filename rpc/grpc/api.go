package coregrpc

import (
	"context"
	"errors"
	fmt "fmt"
	"sync"
	time "time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/pubsub"
	"github.com/cometbft/cometbft/libs/rand"
	crypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	"github.com/cometbft/cometbft/proto/tendermint/types"
	core "github.com/cometbft/cometbft/rpc/core"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	eventstypes "github.com/cometbft/cometbft/types"
)

type broadcastAPI struct {
	env *core.Environment
}

func (bapi *broadcastAPI) Ping(context.Context, *RequestPing) (*ResponsePing, error) {
	// kvstore so we can check if the server is up
	return &ResponsePing{}, nil
}

func (bapi *broadcastAPI) BroadcastTx(_ context.Context, req *RequestBroadcastTx) (*ResponseBroadcastTx, error) {
	// NOTE: there's no way to get client's remote address
	// see https://stackoverflow.com/questions/33684570/session-and-remote-ip-address-in-grpc-go
	res, err := bapi.env.BroadcastTxCommit(&rpctypes.Context{}, req.Tx)
	if err != nil {
		return nil, err
	}

	return &ResponseBroadcastTx{
		CheckTx: &abci.ResponseCheckTx{
			Code: res.CheckTx.Code,
			Data: res.CheckTx.Data,
			Log:  res.CheckTx.Log,
		},
		TxResult: &abci.ExecTxResult{
			Code: res.TxResult.Code,
			Data: res.TxResult.Data,
			Log:  res.TxResult.Log,
		},
	}, nil
}

type BlockAPI struct {
	env *core.Environment
	sync.Mutex
	heightListeners      map[chan SubscribeNewHeightsResponse]struct{}
	newBlockSubscription eventstypes.Subscription
	subscriptionID       string
	subscriptionQuery    pubsub.Query
}

func NewBlockAPI(env *core.Environment) *BlockAPI {
	return &BlockAPI{
		env:               env,
		heightListeners:   make(map[chan SubscribeNewHeightsResponse]struct{}, 1000),
		subscriptionID:    fmt.Sprintf("block-api-subscription-%s", rand.Str(6)),
		subscriptionQuery: eventstypes.EventQueryNewBlock,
	}
}

func (blockAPI *BlockAPI) StartNewBlockEventListener(ctx context.Context) error {
	if blockAPI.newBlockSubscription == nil {
		var err error
		blockAPI.newBlockSubscription, err = blockAPI.env.EventBus.Subscribe(
			ctx,
			blockAPI.subscriptionID,
			blockAPI.subscriptionQuery,
			500,
		)
		if err != nil {
			blockAPI.env.Logger.Error("Failed to subscribe to new blocks", "err", err)
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-blockAPI.newBlockSubscription.Canceled():
			blockAPI.env.Logger.Error("canceled grpc subscription. retrying")
			ok, err := blockAPI.retryNewBlocksSubscription(ctx)
			if err != nil {
				return err
			}
			if !ok {
				// this will happen when the context is done. we can stop here
				return nil
			}
		case event, ok := <-blockAPI.newBlockSubscription.Out():
			if !ok {
				blockAPI.env.Logger.Error("new blocks subscription closed. re-subscribing")
				ok, err := blockAPI.retryNewBlocksSubscription(ctx)
				if err != nil {
					return err
				}
				if !ok {
					// this will happen when the context is done. we can stop here
					return nil
				}
				continue
			}
			newBlockEvent, ok := event.Events()[eventstypes.EventTypeKey]
			if !ok || len(newBlockEvent) == 0 || newBlockEvent[0] != eventstypes.EventNewBlock {
				continue
			}
			data, ok := event.Data().(eventstypes.EventDataNewBlock)
			if !ok {
				blockAPI.env.Logger.Error("couldn't cast event data to new block")
				return fmt.Errorf("couldn't cast event data to new block. Events: %s", event.Events())
			}
			blockAPI.broadcastToListeners(ctx, data.Block.Height, data.Block.Hash())
		}
	}
}

// RetryAttempts the number of retry times when the subscription is closed.
const RetryAttempts = 6

// SubscriptionCapacity the maximum number of pending blocks in the subscription.
const SubscriptionCapacity = 500

func (blockAPI *BlockAPI) retryNewBlocksSubscription(ctx context.Context) (bool, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	blockAPI.Lock()
	defer blockAPI.Unlock()
	for i := 1; i < RetryAttempts; i++ {
		select {
		case <-ctx.Done():
			return false, nil
		case <-ticker.C:
			var err error
			blockAPI.newBlockSubscription, err = blockAPI.env.EventBus.Subscribe(
				ctx,
				fmt.Sprintf("block-api-subscription-%s", rand.Str(6)),
				blockAPI.subscriptionQuery,
				SubscriptionCapacity,
			)
			if err != nil {
				blockAPI.env.Logger.Error("Failed to subscribe to new blocks. retrying", "err", err, "retry_number", i)
			} else {
				return true, nil
			}
		}
	}
	return false, errors.New("couldn't recover from failed blocks subscription. stopping listeners")
}

func (blockAPI *BlockAPI) broadcastToListeners(ctx context.Context, height int64, hash []byte) {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	for ch := range blockAPI.heightListeners {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// logging the error then removing the heights listener
					blockAPI.env.Logger.Debug("failed to write to heights listener", "err", r)
					blockAPI.removeHeightListener(ch)
				}
			}()
			select {
			case <-ctx.Done():
				return
			case ch <- SubscribeNewHeightsResponse{Height: height, Hash: hash}:
			}
		}()
	}
}

func (blockAPI *BlockAPI) addHeightListener() chan SubscribeNewHeightsResponse {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	ch := make(chan SubscribeNewHeightsResponse, 50)
	blockAPI.heightListeners[ch] = struct{}{}
	return ch
}

func (blockAPI *BlockAPI) removeHeightListener(ch chan SubscribeNewHeightsResponse) {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	delete(blockAPI.heightListeners, ch)
}

func (blockAPI *BlockAPI) closeAllListeners() {
	blockAPI.Lock()
	defer blockAPI.Unlock()
	if blockAPI.heightListeners == nil {
		// if this is nil, then there is no need to close anything
		return
	}
	for channel := range blockAPI.heightListeners {
		delete(blockAPI.heightListeners, channel)
	}
}

// Stop cleans up the BlockAPI instance by closing all listeners
// and ensuring no further events are processed.
func (blockAPI *BlockAPI) Stop(ctx context.Context) error {
	blockAPI.Lock()
	defer blockAPI.Unlock()

	// close all height listeners
	blockAPI.closeAllListeners()

	var err error
	// stop the events subscription
	if blockAPI.newBlockSubscription != nil {
		err = blockAPI.env.EventBus.Unsubscribe(ctx, blockAPI.subscriptionID, blockAPI.subscriptionQuery)
		blockAPI.newBlockSubscription = nil
	}

	blockAPI.env.Logger.Info("gRPC streaming API has been stopped")
	return err
}

func (blockAPI *BlockAPI) BlockByHash(req *BlockByHashRequest, stream BlockAPIService_BlockByHashServer) error {
	blockStore := blockAPI.env.BlockStore
	blockMeta := blockStore.LoadBlockMetaByHash(req.Hash)
	if blockMeta == nil {
		return fmt.Errorf("nil block meta for block hash %d", req.Hash)
	}
	commit := blockStore.LoadBlockCommit(blockMeta.Header.Height)
	if commit == nil {
		return fmt.Errorf("nil commit for block hash %d", req.Hash)
	}
	protoCommit := commit.ToProto()

	validatorSet, err := blockAPI.env.StateStore.LoadValidators(blockMeta.Header.Height)
	if err != nil {
		return err
	}
	protoValidatorSet, err := validatorSet.ToProto()
	if err != nil {
		return err
	}

	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part, err := blockStore.LoadBlockPart(blockMeta.Header.Height, i).ToProto()
		if err != nil {
			return err
		}
		if part == nil {
			return fmt.Errorf("nil block part %d for block hash %d", i, req.Hash)
		}
		if !req.Prove {
			part.Proof = crypto.Proof{}
		}
		isLastPart := i == int(blockMeta.BlockID.PartSetHeader.Total)-1
		resp := BlockByHashResponse{
			BlockPart: part,
			IsLast:    isLastPart,
		}
		if i == 0 {
			resp.ValidatorSet = protoValidatorSet
			resp.Commit = protoCommit
		}
		err = stream.Send(&resp)
		if err != nil {
			return err
		}
	}
	return nil
}

func (blockAPI *BlockAPI) BlockByHeight(req *BlockByHeightRequest, stream BlockAPIService_BlockByHeightServer) error {
	blockStore := blockAPI.env.BlockStore
	height := req.Height
	if height == 0 {
		height = blockStore.Height()
	}

	blockMeta := blockStore.LoadBlockMeta(height)
	if blockMeta == nil {
		return fmt.Errorf("nil block meta for height %d", height)
	}

	commit := blockStore.LoadSeenCommit(height)
	if commit == nil {
		return fmt.Errorf("nil block commit for height %d", height)
	}
	protoCommit := commit.ToProto()

	validatorSet, err := blockAPI.env.StateStore.LoadValidators(height)
	if err != nil {
		return err
	}
	protoValidatorSet, err := validatorSet.ToProto()
	if err != nil {
		return err
	}

	for i := 0; i < int(blockMeta.BlockID.PartSetHeader.Total); i++ {
		part, err := blockStore.LoadBlockPart(height, i).ToProto()
		if err != nil {
			return err
		}
		if part == nil {
			return fmt.Errorf("nil block part %d for height %d", i, height)
		}
		if !req.Prove {
			part.Proof = crypto.Proof{}
		}
		isLastPart := i == int(blockMeta.BlockID.PartSetHeader.Total)-1
		resp := BlockByHeightResponse{
			BlockPart: part,
			IsLast:    isLastPart,
		}
		if i == 0 {
			resp.ValidatorSet = protoValidatorSet
			resp.Commit = protoCommit
		}
		err = stream.Send(&resp)
		if err != nil {
			return err
		}
	}
	return nil
}

func (blockAPI *BlockAPI) Status(_ context.Context, _ *StatusRequest) (*StatusResponse, error) {
	status, err := blockAPI.env.Status(nil)
	if err != nil {
		return nil, err
	}

	protoPubKey, err := encoding.PubKeyToProto(status.ValidatorInfo.PubKey)
	if err != nil {
		return nil, err
	}
	return &StatusResponse{
		NodeInfo: status.NodeInfo.ToProto(),
		SyncInfo: &SyncInfo{
			LatestBlockHash:     status.SyncInfo.LatestBlockHash,
			LatestAppHash:       status.SyncInfo.LatestAppHash,
			LatestBlockHeight:   status.SyncInfo.LatestBlockHeight,
			LatestBlockTime:     status.SyncInfo.LatestBlockTime,
			EarliestBlockHash:   status.SyncInfo.EarliestBlockHash,
			EarliestAppHash:     status.SyncInfo.EarliestAppHash,
			EarliestBlockHeight: status.SyncInfo.EarliestBlockHeight,
			EarliestBlockTime:   status.SyncInfo.EarliestBlockTime,
			CatchingUp:          status.SyncInfo.CatchingUp,
		},
		ValidatorInfo: &ValidatorInfo{
			Address:     status.ValidatorInfo.Address,
			PubKey:      &protoPubKey,
			VotingPower: status.ValidatorInfo.VotingPower,
		},
	}, nil
}

func (blockAPI *BlockAPI) Commit(_ context.Context, req *CommitRequest) (*CommitResponse, error) {
	blockStore := blockAPI.env.BlockStore
	height := req.Height
	if height == 0 {
		height = blockStore.Height()
	}
	commit := blockStore.LoadSeenCommit(height)
	if commit == nil {
		return nil, fmt.Errorf("nil block commit for height %d", height)
	}
	protoCommit := commit.ToProto()

	return &CommitResponse{
		Commit: &types.Commit{
			Height:     protoCommit.Height,
			Round:      protoCommit.Round,
			BlockID:    protoCommit.BlockID,
			Signatures: protoCommit.Signatures,
		},
	}, nil
}

func (blockAPI *BlockAPI) ValidatorSet(_ context.Context, req *ValidatorSetRequest) (*ValidatorSetResponse, error) {
	blockStore := blockAPI.env.BlockStore
	height := req.Height
	if height == 0 {
		height = blockStore.Height()
	}
	validatorSet, err := blockAPI.env.StateStore.LoadValidators(height)
	if err != nil {
		return nil, err
	}
	protoValidatorSet, err := validatorSet.ToProto()
	if err != nil {
		return nil, err
	}
	return &ValidatorSetResponse{
		ValidatorSet: protoValidatorSet,
		Height:       height,
	}, nil
}

func (blockAPI *BlockAPI) SubscribeNewHeights(_ *SubscribeNewHeightsRequest, stream BlockAPIService_SubscribeNewHeightsServer) error {
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

type BlobstreamAPI struct {
	env *core.Environment
}

func NewBlobstreamAPI(env *core.Environment) *BlobstreamAPI {
	return &BlobstreamAPI{env: env}
}

func (blobAPI *BlobstreamAPI) DataRootInclusionProof(_ context.Context, req *DataRootInclusionProofRequest) (*DataRootInclusionProofResponse, error) {
	proof, err := blobAPI.env.GenerateDataRootInclusionProof(req.Height, req.Start, req.End)
	if err != nil {
		return nil, err
	}

	return &DataRootInclusionProofResponse{
		Proof: *proof.ToProto(),
	}, nil
}
