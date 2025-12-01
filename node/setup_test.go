package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/mempool/cat"
	"github.com/cometbft/cometbft/p2p"
)

func TestInitDBs(t *testing.T) {
	testCases := []struct {
		name           string
		blockstorePath string
		expectSamePath bool
	}{
		{
			name:           "custom blockstore path",
			blockstorePath: "blockstore",
			expectSamePath: false,
		},
		{
			name:           "default blockstore path",
			blockstorePath: "", // Should default to DBPath
			expectSamePath: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := cfg.DefaultConfig()
			config.SetRoot(t.TempDir())
			config.DBPath = "data"
			config.BlockstorePath = tc.blockstorePath

			// Create a custom DBProvider that tracks which paths were used
			paths := make(map[string]string)
			dbProvider := func(ctx *cfg.DBContext) (dbm.DB, error) {
				paths[ctx.ID] = ctx.Path
				return cfg.DefaultDBProvider(ctx)
			}

			blockStore, stateDB, err := initDBs(config, dbProvider)
			require.NoError(t, err)
			defer func() {
				if blockStore != nil {
					blockStore.Close()
				}
				if stateDB != nil {
					stateDB.Close()
				}
			}()

			if tc.expectSamePath {
				assert.Equal(t, paths["state"], paths["blockstore"], "expected state and blockstore to use same path")
				assert.Equal(t, config.DBDir(), paths["blockstore"])
			} else {
				assert.NotEqual(t, paths["state"], paths["blockstore"], "expected state and blockstore to use different paths")
				assert.Equal(t, config.BlockstoreDir(), paths["blockstore"])
				assert.Equal(t, config.DBDir(), paths["state"])
			}
		})
	}
}

type mockPeer struct {
	p2p.Peer
	nodeInfo p2p.NodeInfo
}

func (mp *mockPeer) NodeInfo() p2p.NodeInfo {
	return mp.nodeInfo
}

func TestPeerFilter(t *testing.T) {
	config := cfg.DefaultConfig()
	nodeKey := &p2p.NodeKey{PrivKey: ed25519.GenPrivKey()}
	nodeInfo := p2p.DefaultNodeInfo{}
	// proxyApp can be nil because config.FilterPeers is false by default
	traceClient := trace.NoOpTracer()

	_, peerFilters := createTransport(config, nodeInfo, nodeKey, nil, traceClient)

	require.NotEmpty(t, peerFilters, "should return at least one peer filter")
	filter := peerFilters[len(peerFilters)-1]

	testCases := []struct {
		name     string
		channels []byte
		errMsg   string
	}{
		{
			name: "Valid Peer",
			channels: []byte{
				propagation.DataChannel, propagation.WantChannel,
				cat.MempoolDataChannel, cat.MempoolWantsChannel,
			},
			errMsg: "",
		},
		{
			name: "Legacy Peer (No Prop Channels)",
			channels: []byte{
				cat.MempoolDataChannel, cat.MempoolWantsChannel,
			},
			errMsg: "peer is only using legacy propagation",
		},
		{
			name: "Non-CAT Peer (Missing MempoolDataChannel)",
			channels: []byte{
				propagation.DataChannel, propagation.WantChannel,
				cat.MempoolWantsChannel,
			},
			errMsg: "peer is not using CAT mempool",
		},
		{
			name: "Non-CAT Peer (Missing MempoolWantsChannel)",
			channels: []byte{
				propagation.DataChannel, propagation.WantChannel,
				cat.MempoolDataChannel,
			},
			errMsg: "peer is not using CAT mempool",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mp := &mockPeer{
				nodeInfo: p2p.DefaultNodeInfo{
					Channels: tc.channels,
				},
			}

			err := filter(nil, mp)
			if tc.errMsg == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errMsg)
			}
		})
	}
}
