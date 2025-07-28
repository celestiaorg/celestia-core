package schema

import (
	"strings"

	"github.com/cometbft/cometbft/config"
)

func init() {
	config.DefaultTracingTables = strings.Join(AllTables(), ",")
}

func AllTables() []string {
	tables := []string{}
	tables = append(tables, MempoolTables()...)
	tables = append(tables, ConsensusTables()...)
	tables = append(tables, P2PTables()...)
	tables = append(tables, ABCITable)
	tables = append(tables, RecoveryTables()...)
	tables = append(tables, MessageStatsTables()...)
	tables = append(tables, CompleteBlockTables()...)
	return tables
}

const (
	Broadcast = "broadcast"
)

type TransferType int

const (
	Download TransferType = iota
	Upload
	Haves
)

func (t TransferType) String() string {
	switch t {
	case Download:
		return "download"
	case Upload:
		return "upload"
	case Haves:
		return "haves"
	default:
		return "unknown"
	}
}
