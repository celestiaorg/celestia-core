package load

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/config"
)

func TestMultipleConnections(t *testing.T) {
	cfg := config.DefaultP2PConfig()
	cfg.AllowDuplicateIP = true
	cfg.DialTimeout = 10 * time.Second

	peerCount := 2
	reactors := make([]*MockReactor, peerCount)
	nodes := make([]*node, peerCount)

	chainID := "base-30"

	for i := 0; i < peerCount; i++ {
		reactor := NewMockReactor(defaultTestChannels, defaultMsgSizes)
		node, err := newnode(*cfg, chainID, reactor)
		require.NoError(t, err)

		err = node.start()
		require.NoError(t, err)
		defer node.stop()

		reactors[i] = reactor
		nodes[i] = node
		fmt.Println("added node", i, node.addr)
	}

	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 1; i < peerCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fmt.Println(i, nodes[i].addr)
			err := nodes[0].sw.DialPeerWithAddress(nodes[i].addr)
			require.NoError(t, err)
		}(i)
	}

	fmt.Println("-----------------------------")

	wg.Wait()

	go func() {
		for i := 0; i < 90; i++ {
			time.Sleep(10 * time.Second)
			for _, reactor := range reactors {
				reactor.PrintReceiveSpeed()
			}
			fmt.Println("-----------------------------")
		}
	}()

	for _, reactor := range reactors {
		reactor.FloodAllPeers(&wg,
			time.Minute*60,
			FirstChannel,
			SecondChannel,
			ThirdChannel,
			FourthChannel,
			FifthChannel,
			SixthChannel,
			SeventhChannel,
			EighthChannel,
			NinthChannel,
			TenthChannel,
		)
	}

	//for _, size := range []int64{
	//	100,
	//	1_000,
	//	2_000,
	//	5_000,
	//	10_000,
	//	50_000,
	//	100_000,
	//	500_000,
	//	1_000_000,
	//	10_000_000,
	//	100_000_000,
	//	1_000_000_000,
	//} {
	//	for _, reactor := range reactors {
	//		reactor.IncreaseSize(size)
	//	}
	//	fmt.Printf("increased size to %d bytes\n", size)
	//	time.Sleep(10 * time.Second)
	//}

	wg.Wait()
	time.Sleep(10 * time.Minute)
}
