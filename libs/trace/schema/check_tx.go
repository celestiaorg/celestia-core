package schema

import "github.com/cometbft/cometbft/libs/trace"

const (
	CheckTxTable = "check_tx"
)

type CheckTxUpdate string

const (
	CheckTxStart CheckTxUpdate = "check_tx_start"
	CheckTxEnd   CheckTxUpdate = "check_tx_end"
)

type CheckTx struct {
	TraceType string `json:"trace"`
}

func (CheckTx) Table() string {
	return CheckTxTable
}

func WriteCheckTx(client trace.Tracer, traceType CheckTxUpdate) {
	client.Write(CheckTx{
		TraceType: string(traceType),
	})
}
