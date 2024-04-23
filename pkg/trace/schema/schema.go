package schema

import (
	"strings"

	"github.com/tendermint/tendermint/config"
)

func init() {
	config.DefaultTracingTables = strings.Join(AllTables(), ",")
}

func AllTables() []string {
	tables := []string{}
	tables = append(tables, MempoolTables()...)
	tables = append(tables, ConsensusTables()...)
	return tables
}

type TransferType int

const (
	Download TransferType = iota
	Upload
)

func (t TransferType) String() string {
	switch t {
	case Download:
		return "download"
	case Upload:
		return "upload"
	default:
		return "unknown"
	}
}
