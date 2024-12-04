package consensus

import (
	"fmt"
	"testing"

	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
)

func TestDataRoutine_basic(t *testing.T) {
	N := 2
	css, cleanup := randConsensusNet(N, "consensus_reactor_test", newMockTickerFunc(true), newCounter)
	defer cleanup()
	reactors, blocksSubs, eventBuses := startConsensusNet(t, css, N)
	defer stopConsensusNet(log.TestingLogger(), reactors, eventBuses)

	// wait till everyone makes the first new block
	timeoutWaitGroup(t, N, func(j int) {
		<-blocksSubs[j].Out()
	}, css)

	fmt.Println("yeah")

	for _, reactor := range reactors {
		fmt.Println("reactor", reactor.dr.self, "-------------------------------")
		fmt.Println("peer state", reactor.dr.peerstate)
		fmt.Println("proposals", reactor.dr.proposals)
	}
}

func TestDebug(t *testing.T) {
	c := bits.NewBitArray(1)
	fmt.Println(c.IsEmpty(), c)
}
