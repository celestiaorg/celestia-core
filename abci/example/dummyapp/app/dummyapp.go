// Package dummy extends the kvstore.Application to include random data.
// The random data is generated via the PreprocessTxs abci method.
//
// The name is inspired by:
// https://github.com/lazyledger/lazyledger-prototype/blob/2aeca6f55ad389b9d68034a0a7038f80a8d2982e/app_dummy.go#L7
package dummy

import (
	"bytes"
	"math/rand"
	"sort"
	"time"

	"github.com/lazyledger/lazyledger-core/abci/example/kvstore"
	"github.com/lazyledger/lazyledger-core/abci/types"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	"github.com/lazyledger/lazyledger-core/types/consts"
)

type Application struct {
	*kvstore.Application

	// PreprocessTxs will create random messages according to these numbers:
	randTxs  uint32
	txSize   uint32
	msgSize  uint32
	randMsgs uint32
	sleep    time.Duration
}

type Option func(app *Application)

// RandMessagesOnPreprocess will indicate to generate a given number of
// random messages and transactions on PreprocessTxs.
func RandMessagesOnPreprocess(numTx, txSize, numMsgs, msgSize uint32) Option {
	return func(app *Application) {
		app.randTxs = numTx
		app.txSize = txSize
		app.randMsgs = numMsgs
		app.msgSize = msgSize
	}
}

// SleepDuration simulates latency (e.g. incurred by execution or other computation)
// during PreprocessTxs.
func SleepDuration(sleep time.Duration) Option {
	return func(app *Application) {
		app.sleep = sleep
	}
}

func NewApplication(opts ...Option) *Application {
	app := &Application{Application: kvstore.NewApplication()}
	for _, opt := range opts {
		opt(app)
	}

	return app
}

func (app *Application) PreprocessTxs(req types.RequestPreprocessTxs) types.ResponsePreprocessTxs {
	time.Sleep(app.sleep)
	if app.randTxs > 0 || app.randMsgs > 0 {
		randMsgs := generateRandNamespacedRawData(app.randMsgs, consts.NamespaceSize, app.msgSize)
		randMessages := toMessageSlice(randMsgs)
		// yeah, we misuse same function as above to generate Txs
		// as they will still be app.txSize large ¯\_(ツ)_/¯
		randTxs := generateRandNamespacedRawData(app.randTxs, app.txSize, 0)

		return types.ResponsePreprocessTxs{
			Txs: append(append(
				make([][]byte, len(req.Txs)+len(randTxs)),
				req.Txs...), randTxs...),
			Messages: &tmproto.Messages{MessagesList: randMessages},
		}
	}
	return types.ResponsePreprocessTxs{Txs: req.Txs}
}

func toMessageSlice(msgs [][]byte) []*tmproto.Message {
	res := make([]*tmproto.Message, len(msgs))
	for i := 0; i < len(msgs); i++ {
		res[i] = &tmproto.Message{NamespaceId: msgs[i][:consts.NamespaceSize], Data: msgs[i][consts.NamespaceSize:]}
	}
	return res
}

// nolint:gosec // G404: Use of weak random number generator
func generateRandNamespacedRawData(total, nidSize, leafSize uint32) [][]byte {
	data := make([][]byte, total)
	for i := uint32(0); i < total; i++ {
		nid := make([]byte, nidSize)
		rand.Read(nid)
		data[i] = nid
	}
	sortByteArrays(data)
	for i := uint32(0); i < total; i++ {
		d := make([]byte, leafSize)
		rand.Read(d)
		data[i] = append(data[i], d...)
	}

	return data
}

func sortByteArrays(src [][]byte) {
	sort.Slice(src, func(i, j int) bool { return bytes.Compare(src[i], src[j]) < 0 })
}
