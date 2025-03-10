package propagation

import (
	"fmt"
	"testing"
	"time"

	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/state"
)

func TestHugeBlock(t *testing.T) {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 5000000
	p2pCfg.RecvRate = 5000000

	nodes := 20

	reactors, _ := createTestReactors(nodes, p2pCfg, false, "/home/evan/data/experiments/celestia/fast-recovery/debug")

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// create a 32MB block
	prop, ps, _, metaData := createTestProposal(sm, 1, 128, 1000000)

	start := time.Now()
	reactors[1].ProposeBlock(prop, ps, metaData)

	end := time.Now()

	time.Sleep(1 * time.Second)

	fmt.Println("Time to propagate 128MB block to 20 nodes:", end.Sub(start))
}
