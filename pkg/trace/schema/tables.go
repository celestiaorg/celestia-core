package schema

import "github.com/tendermint/tendermint/config"

func init() {
	config.DefaultInfluxTables = AllTables()
}

func AllTables() []string {
	tables := []string{}
	tables = append(tables, MempoolTables()...)
	tables = append(tables, ConsensusTables()...)
	return tables
}
