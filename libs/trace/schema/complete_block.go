package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

func CompleteBlockTables() []string {
	return []string{
		CompleteBlockTable,
	}
}

const (
	CompleteBlockTable = "recovery_complete_block"
)

type CompleteBlock struct {
	Height     int64 `json:"height"`
	Round      int32 `json:"round"`
	IsComplete bool  `json:"is_complete"`
}

func (CompleteBlock) Table() string {
	return CompleteBlockTable
}

func WriteCompleteBlock(client trace.Tracer, height int64, round int32, complete bool) {
	client.Write(CompleteBlock{Height: height, Round: round, IsComplete: complete})
}
