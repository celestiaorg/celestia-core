package schema

import (
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/trace"
)

// MempoolTables returns the list of tables for mempool tracing.
func MempoolTables() []string {
	return []string{
		MempoolTxTable,
		MempoolPeerStateTable,
		MempoolRecoveredPartsTable,
		MempoolAddResultTable,
		MempoolTxStatusTable,
		MempoolRecheckTable,
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
	// MempoolPeerState is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to mempool state, specifically
	// the gossipping of "SeenTx" and "WantTx".
	MempoolPeerStateTable = "mempool_peer_state"
)

type MempoolStateUpdateType string

const (
	SeenTx          MempoolStateUpdateType = "SeenTx"
	WantTx          MempoolStateUpdateType = "WantTx"
	MissingSequence MempoolStateUpdateType = "MissingSequence"
	Unknown         MempoolStateUpdateType = "Unknown"
)

// MempoolPeerState describes the schema for the "mempool_peer_state" table.
type MempoolPeerState struct {
	Peer         string                 `json:"peer"`
	StateUpdate  MempoolStateUpdateType `json:"state_update"`
	TxHash       string                 `json:"tx_hash"`
	TransferType TransferType           `json:"transfer_type"`
	Signer       string                 `json:"signer,omitempty"`
	Sequence     uint64                 `json:"sequence,omitempty"`
}

// Table returns the table name for the MempoolPeerState struct.
func (MempoolPeerState) Table() string {
	return MempoolPeerStateTable
}

// WriteMempoolPeerState writes a tracing point for the mempool state using
// the predetermined schema for mempool tracing.
func WriteMempoolPeerState(
	client trace.Tracer,
	peer string,
	stateUpdate MempoolStateUpdateType,
	txHash []byte,
	transferType TransferType,
) {
	WriteMempoolPeerStateWithSeq(client, peer, stateUpdate, txHash, transferType, nil, 0)
}

// WriteMempoolPeerStateWithSeq writes a tracing point for the mempool state
// including signer and sequence information.
func WriteMempoolPeerStateWithSeq(
	client trace.Tracer,
	peer string,
	stateUpdate MempoolStateUpdateType,
	txHash []byte,
	transferType TransferType,
	signer []byte,
	sequence uint64,
) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from creating the map of fields.
	if !client.IsCollecting(MempoolPeerStateTable) {
		return
	}

	signerStr := ""
	if len(signer) > 0 {
		signerStr = string(signer)
	}

	client.Write(MempoolPeerState{
		Peer:         peer,
		StateUpdate:  stateUpdate,
		TransferType: transferType,
		TxHash:       bytes.HexBytes(txHash).String(),
		Signer:       signerStr,
		Sequence:     sequence,
	})
}

const (
	// MempoolAddResultTable is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to adding transactions to the mempool.
	MempoolAddResultTable = "mempool_add_result"
)

type MempoolAddResultType string

const (
	Added            MempoolAddResultType = "added"
	AlreadyInMempool MempoolAddResultType = "already_in_mempool"
	Rejected         MempoolAddResultType = "rejected"
)

// MempoolAddResult describes the schema for the "mempool_add_result" table.
type MempoolAddResult struct {
	Peer     string               `json:"peer"`
	TxHash   string               `json:"tx_hash"`
	Result   MempoolAddResultType `json:"result"`
	Error    string               `json:"error,omitempty"`
	Signer   string               `json:"signer,omitempty"`
	Sequence uint64               `json:"sequence,omitempty"`
}

// Table returns the table name for the MempoolAddResult struct.
func (MempoolAddResult) Table() string {
	return MempoolAddResultTable
}

// WriteMempoolAddResult writes a tracing point for mempool add results using
// the predetermined schema for mempool tracing.
func WriteMempoolAddResult(
	client trace.Tracer,
	peer string,
	txHash []byte,
	result MempoolAddResultType,
	err error,
	signer string,
	sequence uint64,
) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from creating the map of fields.
	if !client.IsCollecting(MempoolAddResultTable) {
		return
	}

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	client.Write(MempoolAddResult{
		Peer:     peer,
		TxHash:   bytes.HexBytes(txHash).String(),
		Result:   result,
		Error:    errStr,
		Signer:   signer,
		Sequence: sequence,
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

const (
	// MempoolTxStatusTable is the tracing "measurement" (aka table) for the
	// mempool that stores tracing data related to transaction status changes
	// (evictions, rejections, confirmations).
	MempoolTxStatusTable = "mempool_tx_status"
)

type MempoolTxStatusType string

const (
	TxEvicted   MempoolTxStatusType = "evicted"
	TxRejected  MempoolTxStatusType = "rejected"
	TxConfirmed MempoolTxStatusType = "confirmed"
	TxExpired   MempoolTxStatusType = "expired"
)

// MempoolTxStatus describes the schema for the "mempool_tx_status" table.
type MempoolTxStatus struct {
	TxHash   string              `json:"tx_hash"`
	Status   MempoolTxStatusType `json:"status"`
	Error    string              `json:"error,omitempty"`
	Signer   string              `json:"signer,omitempty"`
	Sequence uint64              `json:"sequence,omitempty"`
}

// Table returns the table name for the MempoolTxStatus struct.
func (MempoolTxStatus) Table() string {
	return MempoolTxStatusTable
}

// WriteMempoolTxStatus writes a tracing point for mempool transaction status
// changes using the predetermined schema for mempool tracing.
func WriteMempoolTxStatus(
	client trace.Tracer,
	txHash []byte,
	status MempoolTxStatusType,
	err error,
	signer []byte,
	sequence uint64,
) {
	if !client.IsCollecting(MempoolTxStatusTable) {
		return
	}

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	signerStr := ""
	if len(signer) > 0 {
		signerStr = string(signer)
	}

	client.Write(MempoolTxStatus{
		TxHash:   bytes.HexBytes(txHash).String(),
		Status:   status,
		Error:    errStr,
		Signer:   signerStr,
		Sequence: sequence,
	})
}

const (
	// MempoolRecheckTable is the tracing "measurement" (aka table) for the
	// mempool that stores tracing data related to transaction rechecks.
	MempoolRecheckTable = "mempool_recheck"
)

// MempoolRecheck describes the schema for the "mempool_recheck" table.
type MempoolRecheck struct {
	TxHash   string `json:"tx_hash"`
	Signer   string `json:"signer,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
	Kept     bool   `json:"kept"`
	Error    string `json:"error,omitempty"`
}

// Table returns the table name for the MempoolRecheck struct.
func (MempoolRecheck) Table() string {
	return MempoolRecheckTable
}

// WriteMempoolRecheck writes a tracing point for mempool recheck events using
// the predetermined schema for mempool tracing.
func WriteMempoolRecheck(
	client trace.Tracer,
	txHash []byte,
	signer []byte,
	sequence uint64,
	kept bool,
	err error,
) {
	if !client.IsCollecting(MempoolRecheckTable) {
		return
	}

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	signerStr := ""
	if len(signer) > 0 {
		signerStr = string(signer)
	}

	client.Write(MempoolRecheck{
		TxHash:   bytes.HexBytes(txHash).String(),
		Signer:   signerStr,
		Sequence: sequence,
		Kept:     kept,
		Error:    errStr,
	})
}
