package core

import (
	"time"

	cmtbytes "github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/p2p"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	"github.com/tendermint/tendermint/types"
)

// Status returns CometBFT status including node info, pubkey, latest block
// hash, app hash, block height and time.
// More: https://docs.cometbft.com/v0.34/rpc/#/Info/status
func Status(ctx *rpctypes.Context) (*ctypes.ResultStatus, error) {
	var (
		earliestBlockHeight   int64
		earliestBlockHash     cmtbytes.HexBytes
		earliestAppHash       cmtbytes.HexBytes
		earliestBlockTimeNano int64
	)

	env := GetEnvironment()
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
	if val := validatorAtHeight(latestUncommittedHeight()); val != nil {
		votingPower = val.VotingPower
	}
	nodeInfo, err := GetNodeInfo(env, latestHeight)
	if err != nil {
		return nil, err
	}

	result := &ctypes.ResultStatus{
		NodeInfo: nodeInfo,
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

func validatorAtHeight(h int64) *types.Validator {
	env := GetEnvironment()
	vals, err := env.StateStore.LoadValidators(h)
	if err != nil {
		return nil
	}
	privValAddress := env.PubKey.Address()
	_, val := vals.GetByAddress(privValAddress)
	return val
}

// GetNodeInfo returns the node info with the app version set to the latest app
// version from the state store. Upstream CometBFT does not support coordinated
// network upgrades so the env.P2PTransport.NodeInfo.ProtocolVersion.App is
// expected to be set on node start-up and never updated. Celestia supports
// upgrading the app version while running the same binary so the
// env.P2PTransport.NodeInfo.ProtocolVersion.App will be incorect if a node
// upgraded app versions without restarting.
func GetNodeInfo(env *Environment, latestHeight int64) (p2p.DefaultNodeInfo, error) {
	consensusParams, err := env.StateStore.LoadConsensusParams(latestHeight)
	if err != nil {
		return p2p.DefaultNodeInfo{}, err
	}

	nodeInfo := env.P2PTransport.NodeInfo().(p2p.DefaultNodeInfo)
	nodeInfo.ProtocolVersion.App = consensusParams.Version.AppVersion
	return nodeInfo, nil
}
