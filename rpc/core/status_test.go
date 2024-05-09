package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/rpc/core"
	"github.com/tendermint/tendermint/state/mocks"
)

func TestGetNodeInfo(t *testing.T) {
	p2pTransport := mockTransport{}
	stateStore := &mocks.Store{}
	stateStore.On("LoadConsensusParams", int64(1)).Return(types.ConsensusParams{Version: types.VersionParams{AppVersion: 1}}, nil)
	stateStore.On("LoadConsensusParams", int64(2)).Return(types.ConsensusParams{Version: types.VersionParams{AppVersion: 2}}, nil)

	type testCase struct {
		name         string
		env          *core.Environment
		latestHeight int64
		want         uint64
	}
	testCases := []testCase{
		{
			name:         "want 1 when consensus params app version is 1",
			env:          &core.Environment{P2PTransport: p2pTransport, StateStore: stateStore},
			latestHeight: 1,
			want:         1,
		},
		{
			name:         "want 2 if consensus params app version is 2",
			env:          &core.Environment{P2PTransport: p2pTransport, StateStore: stateStore},
			latestHeight: 2,
			want:         2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeInfo, err := core.GetNodeInfo(tc.env, tc.latestHeight)
			require.NoError(t, err)
			assert.Equal(t, tc.want, nodeInfo.ProtocolVersion.App)
		})
	}
}

// transport is copy + pasted from the core package because it isn't exported.
// https://github.com/celestiaorg/celestia-core/blob/640d115aec834609022c842b2497fc568df53692/rpc/core/env.go#L69-L73
type transport interface {
	Listeners() []string
	IsListening() bool
	NodeInfo() p2p.NodeInfo
}

// mockTransport implements the transport interface.
var _ transport = (*mockTransport)(nil)

type mockTransport struct{}

func (m mockTransport) Listeners() []string {
	return []string{}
}
func (m mockTransport) IsListening() bool {
	return false
}

func (m mockTransport) NodeInfo() p2p.NodeInfo {
	return p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.ProtocolVersion{
			P2P:   0,
			Block: 0,
			App:   0,
		},
	}
}
