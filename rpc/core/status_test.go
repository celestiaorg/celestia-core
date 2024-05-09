package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tendermint/tendermint/p2p"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/rpc/core"
)

func TestGetNodeInfo(t *testing.T) {
	type testCase struct {
		name            string
		env             *core.Environment
		consensusParams cmtproto.ConsensusParams
		want            uint64
	}
	testCases := []testCase{
		{
			name: "want 1 when consensus params app version is 1",
			env:  &core.Environment{P2PTransport: mockTransport{}},
			consensusParams: cmtproto.ConsensusParams{
				Version: cmtproto.VersionParams{
					AppVersion: 1,
				},
			},
			want: 1,
		},
		{
			name: "want 2 if consensus params app version is 2",
			env:  &core.Environment{P2PTransport: mockTransport{}},
			consensusParams: cmtproto.ConsensusParams{
				Version: cmtproto.VersionParams{
					AppVersion: 2,
				},
			},
			want: 2,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeInfo := core.GetNodeInfo(tc.env, tc.consensusParams)
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
