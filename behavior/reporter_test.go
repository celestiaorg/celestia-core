package behavior_test

import (
	"sync"
	"testing"

	bh "github.com/tendermint/tendermint/behavior"
	"github.com/tendermint/tendermint/p2p"
)

// TestMockReporter tests the MockReporter's ability to store reported
// peer behavior in memory indexed by the peerID.
func TestMockReporter(t *testing.T) {
	var peerID p2p.ID = "MockPeer"
	pr := bh.NewMockReporter()

	behaviors := pr.Getbehaviors(peerID)
	if len(behaviors) != 0 {
		t.Error("Expected to have no behaviors reported")
	}

	badMessage := bh.BadMessage(peerID, "bad message")
	if err := pr.Report(badMessage); err != nil {
		t.Error(err)
	}
	behaviors = pr.Getbehaviors(peerID)
	if len(behaviors) != 1 {
		t.Error("Expected the peer have one reported behavior")
	}

	if behaviors[0] != badMessage {
		t.Error("Expected Bad Message to have been reported")
	}
}

type scriptItem struct {
	peerID   p2p.ID
	behavior bh.Peerbehavior
}

// equalbehaviors returns true if a and b contain the same Peerbehaviors with
// the same freequencies and otherwise false.
func equalbehaviors(a []bh.Peerbehavior, b []bh.Peerbehavior) bool {
	aHistogram := map[bh.Peerbehavior]int{}
	bHistogram := map[bh.Peerbehavior]int{}

	for _, behavior := range a {
		aHistogram[behavior]++
	}

	for _, behavior := range b {
		bHistogram[behavior]++
	}

	if len(aHistogram) != len(bHistogram) {
		return false
	}

	for _, behavior := range a {
		if aHistogram[behavior] != bHistogram[behavior] {
			return false
		}
	}

	for _, behavior := range b {
		if bHistogram[behavior] != aHistogram[behavior] {
			return false
		}
	}

	return true
}

// TestEqualPeerbehaviors tests that equalbehaviors can tell that two slices
// of peer behaviors can be compared for the behaviors they contain and the
// freequencies that those behaviors occur.
func TestEqualPeerbehaviors(t *testing.T) {
	var (
		peerID        p2p.ID = "MockPeer"
		consensusVote        = bh.ConsensusVote(peerID, "voted")
		blockPart            = bh.BlockPart(peerID, "blocked")
		equals               = []struct {
			left  []bh.Peerbehavior
			right []bh.Peerbehavior
		}{
			// Empty sets
			{[]bh.Peerbehavior{}, []bh.Peerbehavior{}},
			// Single behaviors
			{[]bh.Peerbehavior{consensusVote}, []bh.Peerbehavior{consensusVote}},
			// Equal Frequencies
			{
				[]bh.Peerbehavior{consensusVote, consensusVote},
				[]bh.Peerbehavior{consensusVote, consensusVote},
			},
			// Equal frequencies different orders
			{
				[]bh.Peerbehavior{consensusVote, blockPart},
				[]bh.Peerbehavior{blockPart, consensusVote},
			},
		}
		unequals = []struct {
			left  []bh.Peerbehavior
			right []bh.Peerbehavior
		}{
			// Comparing empty sets to non empty sets
			{[]bh.Peerbehavior{}, []bh.Peerbehavior{consensusVote}},
			// Different behaviors
			{[]bh.Peerbehavior{consensusVote}, []bh.Peerbehavior{blockPart}},
			// Same behavior with different frequencies
			{
				[]bh.Peerbehavior{consensusVote},
				[]bh.Peerbehavior{consensusVote, consensusVote},
			},
		}
	)

	for _, test := range equals {
		if !equalbehaviors(test.left, test.right) {
			t.Errorf("expected %#v and %#v to be equal", test.left, test.right)
		}
	}

	for _, test := range unequals {
		if equalbehaviors(test.left, test.right) {
			t.Errorf("expected %#v and %#v to be unequal", test.left, test.right)
		}
	}
}

// TestPeerbehaviorConcurrency constructs a scenario in which
// multiple goroutines are using the same MockReporter instance.
// This test reproduces the conditions in which MockReporter will
// be used within a Reactor `Receive` method tests to ensure thread safety.
func TestMockPeerbehaviorReporterConcurrency(t *testing.T) {
	behaviorScript := []struct {
		peerID    p2p.ID
		behaviors []bh.Peerbehavior
	}{
		{"1", []bh.Peerbehavior{bh.ConsensusVote("1", "")}},
		{"2", []bh.Peerbehavior{bh.ConsensusVote("2", ""), bh.ConsensusVote("2", ""), bh.ConsensusVote("2", "")}},
		{
			"3",
			[]bh.Peerbehavior{
				bh.BlockPart("3", ""),
				bh.ConsensusVote("3", ""),
				bh.BlockPart("3", ""),
				bh.ConsensusVote("3", ""),
			},
		},
		{
			"4",
			[]bh.Peerbehavior{
				bh.ConsensusVote("4", ""),
				bh.ConsensusVote("4", ""),
				bh.ConsensusVote("4", ""),
				bh.ConsensusVote("4", ""),
			},
		},
		{
			"5",
			[]bh.Peerbehavior{
				bh.BlockPart("5", ""),
				bh.ConsensusVote("5", ""),
				bh.BlockPart("5", ""),
				bh.ConsensusVote("5", ""),
			},
		},
	}

	var receiveWg sync.WaitGroup
	pr := bh.NewMockReporter()
	scriptItems := make(chan scriptItem)
	done := make(chan int)
	numConsumers := 3
	for i := 0; i < numConsumers; i++ {
		receiveWg.Add(1)
		go func() {
			defer receiveWg.Done()
			for {
				select {
				case pb := <-scriptItems:
					if err := pr.Report(pb.behavior); err != nil {
						t.Error(err)
					}
				case <-done:
					return
				}
			}
		}()
	}

	var sendingWg sync.WaitGroup
	sendingWg.Add(1)
	go func() {
		defer sendingWg.Done()
		for _, item := range behaviorScript {
			for _, reason := range item.behaviors {
				scriptItems <- scriptItem{item.peerID, reason}
			}
		}
	}()

	sendingWg.Wait()

	for i := 0; i < numConsumers; i++ {
		done <- 1
	}

	receiveWg.Wait()

	for _, items := range behaviorScript {
		reported := pr.Getbehaviors(items.peerID)
		if !equalbehaviors(reported, items.behaviors) {
			t.Errorf("expected peer %s to have behaved \nExpected: %#v \nGot %#v \n",
				items.peerID, items.behaviors, reported)
		}
	}
}
