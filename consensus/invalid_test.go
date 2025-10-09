package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

//----------------------------------------------
// byzantine failures

// one byz val sends a precommit for a random block at each height
// Ensure a testnet makes blocks
func TestReactorInvalidPrecommit(t *testing.T) {
	N := 4
	css, cleanup := randConsensusNet(t, N, "consensus_reactor_test", newMockTickerFunc(true), newKVStore)
	defer cleanup()

	// Set timeouts in the state since consensus now uses state timeouts instead of config timeouts
	for i := 0; i < N; i++ {
		css[i].mtx.Lock()
		css[i].state.Timeouts.TimeoutPropose = 3000 * time.Millisecond
		css[i].state.Timeouts.TimeoutPrevote = 1000 * time.Millisecond
		css[i].state.Timeouts.TimeoutPrecommit = 1000 * time.Millisecond
		css[i].mtx.Unlock()
	}

	for i := 0; i < N; i++ {
		ticker := NewTimeoutTicker()
		ticker.SetLogger(css[i].Logger)
		css[i].SetTimeoutTicker(ticker)

	}

	reactors, blocksSubs, eventBuses := startConsensusNet(t, css, N)
	defer stopConsensusNet(log.TestingLogger(), reactors, eventBuses)

	// this val sends a random precommit at each height
	byzValIdx := N - 1
	byzVal := css[byzValIdx]
	byzR := reactors[byzValIdx]

	// update the doPrevote function to just send a valid precommit for a random block
	// and otherwise disable the priv validator
	byzVal.mtx.Lock()
	pv := byzVal.privValidator
	byzVal.doPrevote = func(int64, int32) {
		invalidDoPrevoteFunc(t, byzVal, byzR.Switch, pv)
	}
	byzVal.mtx.Unlock()

	// wait for a bunch of blocks
	// TODO: make this tighter by ensuring the halt happens by block 2
	for i := 0; i < 10; i++ {
		timeoutWaitGroup(N, func(j int) {
			<-blocksSubs[j].Out()
		})
	}
}

// reactorTestCase represents a test case for invalid reactor messages
type reactorTestCase struct {
	name      string
	channelID byte
	message   proto.Message
}

// genReactorTestCases generates test cases for invalid messages across all channels
// Uses current height and round to generate messages with various invalid values
func genReactorTestCases(height int64, round int32) []reactorTestCase {
	testCases := []reactorTestCase{
		// Vote messages (VoteChannel) - nil and structural errors
		{
			name:      "vote with nil inner vote",
			channelID: VoteChannel,
			message:   &cmtcons.Vote{Vote: nil},
		},
		{
			name:      "vote with negative height",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: -1,
					Round:  round,
					Type:   cmtproto.PrevoteType,
				},
			},
		},
		{
			name:      "vote with zero height",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: 0,
					Round:  round,
					Type:   cmtproto.PrevoteType,
				},
			},
		},
		{
			name:      "vote with past height",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: height - 1,
					Round:  round,
					Type:   cmtproto.PrevoteType,
				},
			},
		},
		{
			name:      "vote with future height",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: height + 100,
					Round:  round,
					Type:   cmtproto.PrevoteType,
				},
			},
		},
		{
			name:      "vote with negative round",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: height,
					Round:  -1,
					Type:   cmtproto.PrevoteType,
				},
			},
		},

		// Proposal messages (DataChannel)
		{
			name:      "proposal with nil proposal",
			channelID: DataChannel,
			message:   &cmtcons.Proposal{Proposal: cmtproto.Proposal{}},
		},
		{
			name:      "proposal with negative height",
			channelID: DataChannel,
			message: &cmtcons.Proposal{
				Proposal: cmtproto.Proposal{
					Height: -1,
					Round:  round,
				},
			},
		},
		{
			name:      "proposal with past height",
			channelID: DataChannel,
			message: &cmtcons.Proposal{
				Proposal: cmtproto.Proposal{
					Height: height - 1,
					Round:  round,
				},
			},
		},
		{
			name:      "proposal with future height",
			channelID: DataChannel,
			message: &cmtcons.Proposal{
				Proposal: cmtproto.Proposal{
					Height: height + 100,
					Round:  round,
				},
			},
		},

		// BlockPart messages (DataChannel)
		{
			name:      "blockpart with nil part",
			channelID: DataChannel,
			message: &cmtcons.BlockPart{
				Height: height,
				Round:  round,
				Part:   cmtproto.Part{},
			},
		},
		{
			name:      "blockpart with negative height",
			channelID: DataChannel,
			message: &cmtcons.BlockPart{
				Height: -1,
				Round:  round,
				Part:   cmtproto.Part{Index: 0},
			},
		},
		{
			name:      "blockpart with past height",
			channelID: DataChannel,
			message: &cmtcons.BlockPart{
				Height: height - 1,
				Round:  round,
				Part:   cmtproto.Part{Index: 0},
			},
		},
		{
			name:      "blockpart with future height",
			channelID: DataChannel,
			message: &cmtcons.BlockPart{
				Height: height + 100,
				Round:  round,
				Part:   cmtproto.Part{Index: 0},
			},
		},

		// HasVote messages (StateChannel)
		{
			name:      "hasvote with negative height",
			channelID: StateChannel,
			message: &cmtcons.HasVote{
				Height: -1,
				Round:  round,
				Type:   cmtproto.PrevoteType,
				Index:  0,
			},
		},
		{
			name:      "hasvote with past height",
			channelID: StateChannel,
			message: &cmtcons.HasVote{
				Height: height - 1,
				Round:  round,
				Type:   cmtproto.PrevoteType,
				Index:  0,
			},
		},
		{
			name:      "hasvote with future height",
			channelID: StateChannel,
			message: &cmtcons.HasVote{
				Height: height + 100,
				Round:  round,
				Type:   cmtproto.PrevoteType,
				Index:  0,
			},
		},
		{
			name:      "hasvote with negative round",
			channelID: StateChannel,
			message: &cmtcons.HasVote{
				Height: height,
				Round:  -1,
				Type:   cmtproto.PrevoteType,
				Index:  0,
			},
		},

		// NewRoundStep messages (StateChannel)
		{
			name:      "newroundstep with negative height",
			channelID: StateChannel,
			message: &cmtcons.NewRoundStep{
				Height:          -1,
				Round:           round,
				Step:            1,
				LastCommitRound: -1,
			},
		},
		{
			name:      "newroundstep with past height",
			channelID: StateChannel,
			message: &cmtcons.NewRoundStep{
				Height:          height - 1,
				Round:           round,
				Step:            1,
				LastCommitRound: -1,
			},
		},
		{
			name:      "newroundstep with future height",
			channelID: StateChannel,
			message: &cmtcons.NewRoundStep{
				Height:          height + 100,
				Round:           round,
				Step:            1,
				LastCommitRound: -1,
			},
		},
		{
			name:      "newroundstep with invalid step",
			channelID: StateChannel,
			message: &cmtcons.NewRoundStep{
				Height:          height,
				Round:           round,
				Step:            255, // Invalid step value
				LastCommitRound: -1,
			},
		},

		// VoteSetBits messages (VoteSetBitsChannel)
		{
			name:      "votesetbits with negative height",
			channelID: VoteSetBitsChannel,
			message: &cmtcons.VoteSetBits{
				Height: -1,
				Round:  round,
				Type:   cmtproto.PrevoteType,
			},
		},
		{
			name:      "votesetbits with past height",
			channelID: VoteSetBitsChannel,
			message: &cmtcons.VoteSetBits{
				Height: height - 1,
				Round:  round,
				Type:   cmtproto.PrevoteType,
			},
		},
		{
			name:      "votesetbits with future height",
			channelID: VoteSetBitsChannel,
			message: &cmtcons.VoteSetBits{
				Height: height + 100,
				Round:  round,
				Type:   cmtproto.PrevoteType,
			},
		},
		{
			name:      "votesetbits with negative round",
			channelID: VoteSetBitsChannel,
			message: &cmtcons.VoteSetBits{
				Height: height,
				Round:  -1,
				Type:   cmtproto.PrevoteType,
			},
		},
		{
			name:      "vote with past round",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: height,
					Round:  round - 1,
					Type:   cmtproto.PrevoteType,
				},
			},
		},
		{
			name:      "vote with future round",
			channelID: VoteChannel,
			message: &cmtcons.Vote{
				Vote: &cmtproto.Vote{
					Height: height,
					Round:  round + 100,
					Type:   cmtproto.PrevoteType,
				},
			},
		},

		{
			name:      "hasvote with past round",
			channelID: StateChannel,
			message: &cmtcons.HasVote{
				Height: height,
				Round:  round - 1,
				Type:   cmtproto.PrevoteType,
				Index:  0,
			},
		},
		{
			name:      "hasvote with future round",
			channelID: StateChannel,
			message: &cmtcons.HasVote{
				Height: height,
				Round:  round + 100,
				Type:   cmtproto.PrevoteType,
				Index:  0,
			},
		},

		{
			name:      "votesetbits with past round",
			channelID: VoteSetBitsChannel,
			message: &cmtcons.VoteSetBits{
				Height: height,
				Round:  round - 1,
				Type:   cmtproto.PrevoteType,
			},
		},
		{
			name:      "votesetbits with future round",
			channelID: VoteSetBitsChannel,
			message: &cmtcons.VoteSetBits{
				Height: height,
				Round:  round + 100,
				Type:   cmtproto.PrevoteType,
			},
		},
	}

	return testCases
}

// testReactorInvalidMessagesInState is a helper that waits for a specific state
// and tests invalid messages starting from startIdx until a peer disconnects or all messages are tested.
// Returns the index of the last message that was tested.
func testReactorInvalidMessagesInState(t *testing.T, targetState cstypes.RoundStepType, allMessages []reactorTestCase, startIdx int) int {
	N := 4
	css, cleanup := randConsensusNet(t, N, "consensus_reactor_state_test", newMockTickerFunc(true), newKVStore)
	defer cleanup()

	for i := 0; i < N; i++ {
		css[i].mtx.Lock()
		css[i].state.Timeouts.TimeoutPropose = 200 * time.Millisecond
		css[i].state.Timeouts.TimeoutPrevote = 200 * time.Millisecond
		css[i].state.Timeouts.TimeoutPrecommit = 200 * time.Millisecond
		css[i].state.Timeouts.TimeoutCommit = 200 * time.Millisecond
		css[i].mtx.Unlock()
	}

	for i := 0; i < N; i++ {
		ticker := NewTimeoutTicker()
		ticker.SetLogger(css[i].Logger)
		css[i].SetTimeoutTicker(ticker)
	}

	reactors, _, eventBuses := startConsensusNet(t, css, N)
	defer stopConsensusNet(log.TestingLogger(), reactors, eventBuses)

	reactor := reactors[0]

	// Subscribe to NewRoundStep events
	sub, err := eventBuses[0].Subscribe(context.Background(), testSubscriber, types.EventQueryNewRoundStep)
	require.NoError(t, err)
	defer eventBuses[0].Unsubscribe(context.Background(), testSubscriber, types.EventQueryNewRoundStep) //nolint:errcheck
	stepCh := sub.Out()

	// Wait for target state and test messages until disconnect
	timeout := time.After(60 * time.Second)
	for {
		select {
		case msg := <-stepCh:
			rsEvent, ok := msg.Data().(types.EventDataRoundState)
			if !ok {
				continue
			}

			if rsEvent.Step != targetState.String() {
				continue
			}

			// Hold consensus lock to prevent state changes while testing
			cs := reactors[0].conS
			cs.mtx.Lock()

			// Verify still in target state
			currentState := cs.GetRoundState()
			if currentState.Step != targetState {
				cs.mtx.Unlock()
				continue
			}

			// Generate messages with current height/round
			invalidMessages := genReactorTestCases(currentState.Height, currentState.Round)

			// Send messages starting from startIdx until we disconnect a peer
			lastTestedIdx := startIdx - 1
			for i := startIdx; i < len(invalidMessages); i++ {
				invalidMsg := invalidMessages[i]

				peers := reactor.Switch.Peers().List()
				if len(peers) == 0 {
					t.Fatalf("No more peers available after testing %d messages", i-startIdx)
					break
				}
				peer := peers[0]
				reactor.Receive(p2p.Envelope{
					ChannelID: invalidMsg.channelID,
					Src:       peer,
					Message:   invalidMsg.message,
				})

				lastTestedIdx = i

				// Check if peer was disconnected
				if !peer.IsRunning() {
					break // Stop on disconnect, will restart with fresh peers
				}
			}

			cs.mtx.Unlock()
			return lastTestedIdx

		case <-timeout:
			t.Fatalf("Timed out waiting for state %s", targetState)
			return startIdx - 1
		}
	}
}

func TestReactorInvalidMessages(t *testing.T) {
	states := []struct {
		name  string
		state cstypes.RoundStepType
	}{
		// Note: NewHeight is skipped because it's extremely short-lived (~1ms)
		// and transitions to NewRound before the test can acquire the lock
		//
		// Note: PrevoteWait and PrecommitWait are skipped because they require
		// specific vote patterns (2/3 votes but no majority) which are difficult
		// to trigger reliably in a test environment with limited peers
		{"Propose", cstypes.RoundStepPropose},
		{"Prevote", cstypes.RoundStepPrevote},
		{"Precommit", cstypes.RoundStepPrecommit},
		{"Commit", cstypes.RoundStepCommit},
	}

	// Get all message types (using dummy height/round)
	allMessages := genReactorTestCases(1, 0)
	totalMessages := len(allMessages)

	for _, state := range states {
		state := state // capture range variable

		t.Run(state.name, func(t *testing.T) {
			t.Logf("Testing %d invalid messages for state %s", totalMessages, state.name)
			// keep restarting the network until all messages are tested, because incorrect messages cause peer disconnect
			nextIdx := 0
			for nextIdx < totalMessages {
				lastTestedIdx := testReactorInvalidMessagesInState(t, state.state, allMessages, nextIdx)
				if lastTestedIdx < nextIdx {
					t.Fatalf("No progress made in iteration (lastTested=%d, next=%d)", lastTestedIdx, nextIdx)
				}
				nextIdx = lastTestedIdx + 1
			}
		})
	}
}

func invalidDoPrevoteFunc(t *testing.T, cs *State, sw *p2p.Switch, pv types.PrivValidator) {
	// routine to:
	// - precommit for a random block
	// - send precommit to all peers
	// - disable privValidator (so we don't do normal precommits)
	go func() {
		cs.mtx.Lock()
		defer cs.mtx.Unlock()
		cs.privValidator = pv
		pubKey, err := cs.privValidator.GetPubKey()
		if err != nil {
			panic(err)
		}
		addr := pubKey.Address()
		valIndex, _ := cs.rs.Validators.GetByAddress(addr)

		// precommit a random block
		blockHash := bytes.HexBytes(cmtrand.Bytes(32))
		precommit := &types.Vote{
			ValidatorAddress: addr,
			ValidatorIndex:   valIndex,
			Height:           cs.rs.Height,
			Round:            cs.rs.Round,
			Timestamp:        cs.voteTime(),
			Type:             cmtproto.PrecommitType,
			BlockID: types.BlockID{
				Hash:          blockHash,
				PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)},
			},
		}
		p := precommit.ToProto()
		err = cs.privValidator.SignVote(cs.state.ChainID, p)
		if err != nil {
			t.Error(err)
		}
		precommit.Signature = p.Signature
		precommit.ExtensionSignature = p.ExtensionSignature
		cs.privValidator = nil // disable priv val so we don't do normal votes

		peers := sw.Peers().List()
		for _, peer := range peers {
			cs.Logger.Info("Sending bad vote", "block", blockHash, "peer", peer)
			peer.Send(p2p.Envelope{
				Message:   &cmtcons.Vote{Vote: precommit.ToProto()},
				ChannelID: VoteChannel,
			})
		}
	}()
}
