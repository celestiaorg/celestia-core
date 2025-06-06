package core

import (
	"time"

	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/p2p"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cometbft/cometbft/types"
)

// Status returns CometBFT status including node info, pubkey, latest block
// hash, app hash, block height and time.
// More: https://docs.cometbft.com/v0.38.x/rpc/#/Info/status
func (env *Environment) Status(*rpctypes.Context) (*ctypes.ResultStatus, error) {
	var (
		earliestBlockHeight   int64
		earliestBlockHash     cmtbytes.HexBytes
		earliestAppHash       cmtbytes.HexBytes
		earliestBlockTimeNano int64
	)

	if earliestBlockMeta := env.BlockStore.LoadBaseMeta(); earliestBlockMeta != nil {
		earliestBlockHeight = earliestBlockMeta.Header.Height
		earliestAppHash = earliestBlockMeta.Header.AppHash
		earliestBlockHash = earliestBlockMeta.BlockID.Hash
		earliestBlockTimeNano = earliestBlockMeta.Header.Time.UnixNano()
	}

	var (
		latestBlockHash     cmtbytes.HexBytes
		latestAppHash       cmtbytes.HexBytes
		latestBlockTimeNano int64

		latestHeight = env.BlockStore.Height()
	)

	if latestHeight != 0 {
		if latestBlockMeta := env.BlockStore.LoadBlockMeta(latestHeight); latestBlockMeta != nil {
			latestBlockHash = latestBlockMeta.BlockID.Hash
			latestAppHash = latestBlockMeta.Header.AppHash
			latestBlockTimeNano = latestBlockMeta.Header.Time.UnixNano()
		}
	}

	// Return the very last voting power, not the voting power of this validator
	// during the last block.
	var votingPower int64
	if val := env.validatorAtHeight(env.latestUncommittedHeight()); val != nil {
		votingPower = val.VotingPower
	}

	result := &ctypes.ResultStatus{
		NodeInfo: GetNodeInfo(env, latestHeight),
		SyncInfo: ctypes.SyncInfo{
			LatestBlockHash:     latestBlockHash,
			LatestAppHash:       latestAppHash,
			LatestBlockHeight:   latestHeight,
			LatestBlockTime:     time.Unix(0, latestBlockTimeNano),
			EarliestBlockHash:   earliestBlockHash,
			EarliestAppHash:     earliestAppHash,
			EarliestBlockHeight: earliestBlockHeight,
			EarliestBlockTime:   time.Unix(0, earliestBlockTimeNano),
			CatchingUp:          env.ConsensusReactor.WaitSync(),
		},
		ValidatorInfo: ctypes.ValidatorInfo{
			Address:     env.PubKey.Address(),
			PubKey:      env.PubKey,
			VotingPower: votingPower,
		},
	}

	return result, nil
}

func (env *Environment) validatorAtHeight(h int64) *types.Validator {
	valsWithH, err := env.StateStore.LoadValidators(h)
	if err != nil {
		return nil
	}
	privValAddress := env.PubKey.Address()
	_, val := valsWithH.GetByAddress(privValAddress)
	return val
}

// GetNodeInfo returns the node info with the app version set to the latest app
// version from the state store.
//
// This function is necessary because upstream CometBFT does not support
// upgrading app versions for a running binary. Therefore the
// env.P2PTransport.NodeInfo.ProtocolVersion.App is expected to be set on node
// start-up and never updated. Celestia supports upgrading the app version for a
// running binary so the env.P2PTransport.NodeInfo.ProtocolVersion.App will be
// incorrect if a node upgraded app versions without restarting. This function
// corrects that issue by fetching the latest app version from the state store.
func GetNodeInfo(env *Environment, latestHeight int64) p2p.DefaultNodeInfo {
	nodeInfo := env.P2PTransport.NodeInfo().(p2p.DefaultNodeInfo)

	consensusParams, err := env.StateStore.LoadConsensusParams(latestHeight)
	if err != nil {
		// use the default app version if we can't load the consensus params (i.e. height 0)
		return nodeInfo
	}

	// override the default app version with the latest app version
	nodeInfo.ProtocolVersion.App = consensusParams.Version.App
	return nodeInfo
}
