package evidence_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/evidence"
	"github.com/cometbft/cometbft/p2p"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// A peer that sends an undecodable evidence message is disconnected.
func TestReactorStopsPeerSendingUndecodableMessage(t *testing.T) {
	val := types.NewMockPV()
	reactors, _ := makeAndConnectReactorsAndPools(cfg.TestConfig(), []sm.Store{
		initializeValidatorState(val, 10),
		initializeValidatorState(val, 10),
	})
	require.Equal(t, 1, reactors[1].Switch.Peers().Size())

	// A Vote's first field is a varint, so it does not decode as an EvidenceList.
	sender := reactors[0].Switch.Peers().List()[0]
	require.True(t, sender.Send(p2p.Envelope{
		ChannelID: evidence.EvidenceChannel,
		Message:   &cmtproto.Vote{Type: cmtproto.PrevoteType},
	}))
	require.Eventually(t, func() bool {
		return reactors[1].Switch.Peers().Size() == 0
	}, 5*time.Second, 10*time.Millisecond)
}
