package checktx

import (
	"github.com/lazyledger/lazyledger-core/abci/example/kvstore"
	"github.com/lazyledger/lazyledger-core/config"
	mempl "github.com/lazyledger/lazyledger-core/mempool"
	"github.com/lazyledger/lazyledger-core/proxy"
)

var mempool mempl.Mempool

func init() {
	app := kvstore.NewApplication()
	cc := proxy.NewLocalClientCreator(app)
	appConnMem, _ := cc.NewABCIClient()
	err := appConnMem.Start()
	if err != nil {
		panic(err)
	}

	cfg := config.DefaultMempoolConfig()
	cfg.Broadcast = false

	mempool = mempl.NewCListMempool(cfg, appConnMem, 0)
}

func Fuzz(data []byte) int {
	err := mempool.CheckTx(data, nil, mempl.TxInfo{})
	if err != nil {
		return 0
	}

	return 1
}
