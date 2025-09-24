package schema

import (
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/trace"
)

// MempoolTables returns the list of tables for mempool tracing.
func MempoolTables() []string {
	return []string{
		MempoolTxTable,
		MempoolStateTable,
		MempoolRecoveredPartsTable,
	}
}

// Schema constants for the mempool_tx table.
const (
	// MempoolTxTable is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to gossiping transactions.
	MempoolTxTable = "mempool_tx"
)

// MemPoolTx describes the schema for the "mempool_tx" table.
type MempoolTx struct {
	TxHash       string       `json:"tx_hash"`
	Peer         string       `json:"peer"`
	Size         int          `json:"size"`
	TransferType TransferType `json:"transfer_type"`
}

// Table returns the table name for the MempoolTx struct.
func (MempoolTx) Table() string {
	return MempoolTxTable
}

// WriteMempoolTx writes a tracing point for a tx using the predetermined
// schema for mempool tracing.
func WriteMempoolTx(client trace.Tracer, peer string, txHash []byte, size int, transferType TransferType) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(MempoolTxTable) {
		return
	}
	client.Write(MempoolTx{
		TxHash:       bytes.HexBytes(txHash).String(),
		Peer:         peer,
		Size:         size,
		TransferType: transferType,
	})
}

const (
	// MempoolState is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to mempool state, specifically
	// the gossipping of "SeenTx" and "WantTx".
	MempoolStateTable = "mempool_state"
)

type MempoolStateUpdateType string

const (
	SeenTx  MempoolStateUpdateType = "SeenTx"
	WantTx  MempoolStateUpdateType = "WantTx"
	Unknown MempoolStateUpdateType = "Unknown"
)

// MempoolState describes the schema for the "mempool_peer_state" table.
type MempoolState struct {
	Peer         string                 `json:"peer"`
	StateUpdate  MempoolStateUpdateType `json:"state_update"`
	TxHash       string                 `json:"tx_hash"`
	TransferType TransferType           `json:"transfer_type"`
}

// Table returns the table name for the MempoolState struct.
func (MempoolState) Table() string {
	return MempoolStateTable
}

// WriteMempoolState writes a tracing point for the mempool state using
// the predetermined schema for mempool tracing.
func WriteMempoolState(
	client trace.Tracer,
	peer string,
	stateUpdate MempoolStateUpdateType,
	txHash []byte,
	transferType TransferType,
) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from creating the map of fields.
	if !client.IsCollecting(memR.mempool.PeerHasTx(peerID, txKey)
	schema.WriteMempoolTx(
		memR.traceClient,
		string(e.Src.ID()),
		txKey[:],
		len(tx.Tx),
		schema.Upload,
	)) {
		return
	}
	client.Write(MempoolState{
		Peer:         peer,
		StateUpdate:  stateUpdate,
		TransferType: transferType,
		TxHash:       bytes.HexBytes(txHash).String(),
	})
}

const (
	// MempoolRecoveredPartsTable is the tracing "measurement" (aka table) for the
	// mempool that stores tracing data related to the recovery of parts.
	MempoolRecoveredPartsTable = "recovered"
)

// MempoolRecoveredParts describes the schema for the "recovered" table.
type MempoolRecoveredParts struct {
	Height         int64 `json:"height"`
	Round          int32 `json:"round"`
	RecoveredParts int   `json:"recovered_parts"`
	Ontime         bool  `json:"ontime"`
}

func (m MempoolRecoveredParts) Table() string {
	return MempoolRecoveredPartsTable
}

// WriteMempoolRecoveredParts writes a tracing point for the recovery of parts
// using the predetermined schema for mempool tracing.
func WriteMempoolRecoveredParts(client trace.Tracer, height int64, round int32, parts int) {
	if !client.IsCollecting(MempoolRecoveredPartsTable) {
		return
	}
	client.Write(MempoolRecoveredParts{
		Height:         height,
		Round:          round,
		RecoveredParts: parts,
	})
}
