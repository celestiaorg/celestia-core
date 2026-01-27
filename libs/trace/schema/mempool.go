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
		MempoolBufferTable,
		MempoolRequestTable,
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
	MinSequence  uint64                 `json:"min_sequence,omitempty"`
	MaxSequence  uint64                 `json:"max_sequence,omitempty"`
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
	WriteMempoolPeerStateWithSeq(client, peer, stateUpdate, txHash, transferType, nil, 0, 0, 0)
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
	minSequence uint64,
	maxSequence uint64,
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
		MinSequence:  minSequence,
		MaxSequence:  maxSequence,
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
	Duration       int64 `json:"duration"`
}

func (m MempoolRecoveredParts) Table() string {
	return MempoolRecoveredPartsTable
}

// WriteMempoolRecoveredParts writes a tracing point for the recovery of parts
// using the predetermined schema for mempool tracing.
func WriteMempoolRecoveredParts(client trace.Tracer, height int64, round int32, parts int, duration int64) {
	if !client.IsCollecting(MempoolRecoveredPartsTable) {
		return
	}
	client.Write(MempoolRecoveredParts{
		Height:         height,
		Round:          round,
		RecoveredParts: parts,
		Duration:       duration,
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

const (
	// MempoolBufferTable is the tracing "measurement" (aka table) for the
	// mempool that stores tracing data related to the received transaction buffer.
	MempoolBufferTable = "mempool_buffer"
)

// MempoolBufferEventType represents the type of buffer event
type MempoolBufferEventType string

const (
	BufferEventBuffered        MempoolBufferEventType = "buffered"
	BufferEventProcessed       MempoolBufferEventType = "processed"
	BufferEventRejectedTooFar  MempoolBufferEventType = "rejected_too_far"
	BufferEventRejectedAddFail MempoolBufferEventType = "rejected_add_failed"
)

// MempoolBufferSource indicates where in the code the buffer event occurred
type MempoolBufferSource string

const (
	// BufferSourceReceiveTx - event from Receive() when processing incoming Txs message
	BufferSourceReceiveTx MempoolBufferSource = "receive_tx"
	// BufferSourceProcessBuffer - event from processReceivedBuffer() when draining buffered txs
	BufferSourceProcessBuffer MempoolBufferSource = "process_buffer"
)

// MempoolBuffer describes the schema for the "mempool_buffer" table.
type MempoolBuffer struct {
	// Signer identification
	Signer string `json:"signer"`

	// Sequence state for this signer
	Sequence         uint64 `json:"sequence"`          // tx being buffered/processed
	ExpectedSequence uint64 `json:"expected_sequence"` // what app expects next
	GapSize          uint64 `json:"gap_size"`          // sequence - expected

	// This signer's buffer state
	SignerBufferSize int    `json:"signer_buffer_size"` // txs buffered for this signer
	MinBufferedSeq   uint64 `json:"min_buffered_seq"`   // lowest seq in this signer's buffer
	MaxBufferedSeq   uint64 `json:"max_buffered_seq"`   // highest seq in this signer's buffer

	// Event info
	Event  MempoolBufferEventType `json:"event"`  // buffered, processed, rejected_too_far, rejected_add_failed
	Source MempoolBufferSource    `json:"source"` // where in code this happened
	TxHash string                 `json:"tx_hash,omitempty"`

	// Global context
	TotalSignersWithBufferedTxs int `json:"total_signers,omitempty"`
}

// Table returns the table name for the MempoolBuffer struct.
func (MempoolBuffer) Table() string {
	return MempoolBufferTable
}

// WriteMempoolBuffer writes a tracing point for buffer events using
// the predetermined schema for mempool tracing.
func WriteMempoolBuffer(
	client trace.Tracer,
	signer []byte,
	sequence uint64,
	expectedSequence uint64,
	event MempoolBufferEventType,
	source MempoolBufferSource,
	txHash []byte,
	signerBufferSize int,
	minBufferedSeq uint64,
	maxBufferedSeq uint64,
	totalSigners int,
) {
	if !client.IsCollecting(MempoolBufferTable) {
		return
	}

	signerStr := ""
	if len(signer) > 0 {
		signerStr = string(signer)
	}

	var gapSize uint64
	if sequence > expectedSequence {
		gapSize = sequence - expectedSequence
	}

	client.Write(MempoolBuffer{
		Signer:                      signerStr,
		Sequence:                    sequence,
		ExpectedSequence:            expectedSequence,
		GapSize:                     gapSize,
		SignerBufferSize:            signerBufferSize,
		MinBufferedSeq:              minBufferedSeq,
		MaxBufferedSeq:              maxBufferedSeq,
		Event:                       event,
		Source:                      source,
		TxHash:                      bytes.HexBytes(txHash).String(),
		TotalSignersWithBufferedTxs: totalSigners,
	})
}

const (
	// MempoolRequestTable is the tracing "measurement" (aka table) for the
	// mempool that stores tracing data related to outbound transaction requests.
	MempoolRequestTable = "mempool_request"
)

// MempoolRequestEventType represents the type of request event
type MempoolRequestEventType string

const (
	RequestEventAdded    MempoolRequestEventType = "added"
	RequestEventReceived MempoolRequestEventType = "received"
	RequestEventTimeout  MempoolRequestEventType = "timeout"
	RequestEventCleared  MempoolRequestEventType = "cleared"
)

// MempoolRequest describes the schema for the "mempool_request" table.
type MempoolRequest struct {
	// Request identification
	TxHash   string `json:"tx_hash,omitempty"` // empty for sequence-only requests
	Signer   string `json:"signer,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
	Peer     uint16 `json:"peer"`

	// Event
	Event MempoolRequestEventType `json:"event"` // added, received, timeout, cleared

	// Counts (state after event)
	TotalByTx       int `json:"total_by_tx"`       // requests tracked by txKey
	TotalBySequence int `json:"total_by_sequence"` // requests tracked by (signer, sequence)
	ForPeer         int `json:"for_peer"`          // requests to this specific peer
	ForSigner       int `json:"for_signer"`        // requests for this signer
}

// Table returns the table name for the MempoolRequest struct.
func (MempoolRequest) Table() string {
	return MempoolRequestTable
}

// WriteMempoolRequest writes a tracing point for request events using
// the predetermined schema for mempool tracing.
func WriteMempoolRequest(
	client trace.Tracer,
	txHash []byte,
	signer []byte,
	sequence uint64,
	peer uint16,
	event MempoolRequestEventType,
	totalByTx int,
	totalBySequence int,
	forPeer int,
	forSigner int,
) {
	if !client.IsCollecting(MempoolRequestTable) {
		return
	}

	txHashStr := ""
	if len(txHash) > 0 {
		txHashStr = bytes.HexBytes(txHash).String()
	}

	signerStr := ""
	if len(signer) > 0 {
		signerStr = string(signer)
	}

	client.Write(MempoolRequest{
		TxHash:          txHashStr,
		Signer:          signerStr,
		Sequence:        sequence,
		Peer:            peer,
		Event:           event,
		TotalByTx:       totalByTx,
		TotalBySequence: totalBySequence,
		ForPeer:         forPeer,
		ForSigner:       forSigner,
	})
}
