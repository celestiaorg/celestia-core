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

// General purpose schema constants used across multiple tables
const (
	// PeerFieldKey is the tracing field key for the peer that sent or
	// received a tx. This should take the form of the peer's address as the
	// value.
	PeerFieldKey = "peer"

	// TransferTypeFieldKey is the tracing field key for the class of a tx.
	TransferTypeFieldKey = "transfer_type"

	// TransferTypeDownload is a tracing field value for receiving some
	// data from a peer. This value is used by the "TransferType" field key.
	TransferTypeDownload = "download"

	// TransferTypeUpload is a tracing field value for sending some data
	// to a peer. This value is used by the "TransferType" field key.
	TransferTypeUpload = "upload"

	// RoundFieldKey is the name of the field that stores the consensus round.
	// The value is an int32.
	RoundFieldKey = "round"

	// HeightFieldKey is the name of the field that stores the consensus height.
	// The value is an int64.
	HeightFieldKey = "height"
)
