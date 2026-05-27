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
		MempoolRequestSchedulingTable,
		MempoolReapTable,
		MempoolRecheckBatchTable,
		MempoolChunkedMessageTable,
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
	TxEvicted                  MempoolTxStatusType = "evicted"
	TxRejected                 MempoolTxStatusType = "rejected"
	TxConfirmed                MempoolTxStatusType = "confirmed"
	TxExpired                  MempoolTxStatusType = "expired"
	TxDroppedOutOfSequence     MempoolTxStatusType = "dropped_out_of_sequence"
	TxDroppedPendingOverflow   MempoolTxStatusType = "dropped_pending_overflow"
	TxDroppedLowerSequence     MempoolTxStatusType = "dropped_lower_sequence"
	TxReaped                   MempoolTxStatusType = "reaped"
	TxDroppedByPrepareProposal MempoolTxStatusType = "dropped_by_prepare_proposal"
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
	// MempoolRequestSchedulingTable is the tracing "measurement" (aka table) for
	// the mempool that stores tracing data related to WantTx request scheduling
	// latency (time between observing a SeenTx and actually sending the WantTx).
	MempoolRequestSchedulingTable = "mempool_request_scheduling"
)

// MempoolWantTxKind describes the code path that scheduled the WantTx.
type MempoolWantTxKind string

const (
	// WantTxDirect is a WantTx sent immediately in response to a SeenTx.
	WantTxDirect MempoolWantTxKind = "direct"
	// WantTxFromPending is a WantTx sent for an entry that was first held in
	// the pendingSeen tracker waiting for sequence ordering.
	WantTxFromPending MempoolWantTxKind = "from_pending"
	// WantTxRerequest is a WantTx sent after a prior peer failed to deliver.
	WantTxRerequest MempoolWantTxKind = "rerequest"
)

// MempoolWantTxScheduled describes the schema for the
// "mempool_request_scheduling" table.
type MempoolWantTxScheduled struct {
	TxHash      string            `json:"tx_hash"`
	Peer        string            `json:"peer"`
	Signer      string            `json:"signer,omitempty"`
	Sequence    uint64            `json:"sequence,omitempty"`
	QueuedForNs int64             `json:"queued_for_ns"`
	Kind        MempoolWantTxKind `json:"kind"`
}

// Table returns the table name for the MempoolWantTxScheduled struct.
func (MempoolWantTxScheduled) Table() string {
	return MempoolRequestSchedulingTable
}

// WriteMempoolWantTxScheduled writes a tracing point capturing the latency
// between observing a SeenTx for a transaction and sending the corresponding
// WantTx to a peer.
func WriteMempoolWantTxScheduled(
	client trace.Tracer,
	txHash []byte,
	peer string,
	signer []byte,
	sequence uint64,
	queuedForNs int64,
	kind MempoolWantTxKind,
) {
	if !client.IsCollecting(MempoolRequestSchedulingTable) {
		return
	}

	signerStr := ""
	if len(signer) > 0 {
		signerStr = string(signer)
	}

	client.Write(MempoolWantTxScheduled{
		TxHash:      bytes.HexBytes(txHash).String(),
		Peer:        peer,
		Signer:      signerStr,
		Sequence:    sequence,
		QueuedForNs: queuedForNs,
		Kind:        kind,
	})
}

const (
	// MempoolReapTable is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to ReapMaxBytesMaxGas calls and the
	// PrepareProposal filtering that follows.
	MempoolReapTable = "mempool_reap"
)

// MempoolReapPhase distinguishes the reap call from the post-PrepareProposal
// filtering pass.
type MempoolReapPhase string

const (
	// ReapPhaseReap is the bare mempool reap, before the application has had
	// a chance to filter or re-order via PrepareProposal.
	ReapPhaseReap MempoolReapPhase = "reap"
	// ReapPhasePostPrepareProposal is recorded after PrepareProposal returns,
	// capturing how many of the reaped txs survived the application filter.
	ReapPhasePostPrepareProposal MempoolReapPhase = "post_prepare_proposal"
)

// MempoolReap describes the schema for the "mempool_reap" table.
type MempoolReap struct {
	Height                      int64            `json:"height"`
	Phase                       MempoolReapPhase `json:"phase"`
	TxCountReaped               int32            `json:"tx_count_reaped"`
	TxCountAfterPrepareProposal int32            `json:"tx_count_after_prepare_proposal,omitempty"`
	BytesReaped                 int64            `json:"bytes_reaped"`
	MaxReapBytes                int64            `json:"max_reap_bytes,omitempty"`
	MaxGas                      int64            `json:"max_gas,omitempty"`
	DurationNs                  int64            `json:"duration_ns"`
}

// Table returns the table name for the MempoolReap struct.
func (MempoolReap) Table() string {
	return MempoolReapTable
}

// WriteMempoolReap writes a tracing point summarising a reap call (or the
// PrepareProposal pass that follows it).
func WriteMempoolReap(
	client trace.Tracer,
	height int64,
	phase MempoolReapPhase,
	txCountReaped int32,
	txCountAfterPrepareProposal int32,
	bytesReaped int64,
	maxReapBytes int64,
	maxGas int64,
	durationNs int64,
) {
	if !client.IsCollecting(MempoolReapTable) {
		return
	}
	client.Write(MempoolReap{
		Height:                      height,
		Phase:                       phase,
		TxCountReaped:               txCountReaped,
		TxCountAfterPrepareProposal: txCountAfterPrepareProposal,
		BytesReaped:                 bytesReaped,
		MaxReapBytes:                maxReapBytes,
		MaxGas:                      maxGas,
		DurationNs:                  durationNs,
	})
}

const (
	// MempoolRecheckBatchTable is the tracing "measurement" (aka table) for
	// the mempool that brackets each full recheck pass with start/finish rows
	// so an operator can see how long recheck is taking and whether it is
	// gating consensus.
	MempoolRecheckBatchTable = "mempool_recheck_batch"
)

// MempoolRecheckBatchPhase distinguishes the start of a recheck pass from
// the corresponding finish row.
type MempoolRecheckBatchPhase string

const (
	RecheckBatchStarted  MempoolRecheckBatchPhase = "started"
	RecheckBatchFinished MempoolRecheckBatchPhase = "finished"
)

// MempoolRecheckBatch describes the schema for the "mempool_recheck_batch"
// table.
type MempoolRecheckBatch struct {
	Height     int64                    `json:"height"`
	Phase      MempoolRecheckBatchPhase `json:"phase"`
	TxCount    int32                    `json:"tx_count"`
	DurationNs int64                    `json:"duration_ns,omitempty"`
}

// Table returns the table name for the MempoolRecheckBatch struct.
func (MempoolRecheckBatch) Table() string {
	return MempoolRecheckBatchTable
}

// WriteMempoolRecheckBatch writes a tracing point bracketing a recheck pass.
// Emit with phase="started" at the top of recheckTransactions and with
// phase="finished" once the pass completes. duration_ns is meaningful only
// on the finished row.
func WriteMempoolRecheckBatch(
	client trace.Tracer,
	height int64,
	phase MempoolRecheckBatchPhase,
	txCount int32,
	durationNs int64,
) {
	if !client.IsCollecting(MempoolRecheckBatchTable) {
		return
	}
	client.Write(MempoolRecheckBatch{
		Height:     height,
		Phase:      phase,
		TxCount:    txCount,
		DurationNs: durationNs,
	})
}

const (
	// MempoolChunkedMessageTable is the tracing "measurement" (aka table) for
	// every chunked-mempool wire message (ADR-013). Rows are emitted on both
	// send and receive sides so an operator can reconstruct per-tx timelines
	// and pinpoint which propagation phase is slow (announce, want, serve,
	// reconstruct).
	MempoolChunkedMessageTable = "mempool_chunked_message"
)

// MempoolChunkedMessageType identifies which ADR-013 wire message this row
// describes.
type MempoolChunkedMessageType string

const (
	ChunkedSeenLargeTx  MempoolChunkedMessageType = "seen_large_tx"
	ChunkedHaveTxChunks MempoolChunkedMessageType = "have_tx_chunks"
	ChunkedWantTxChunks MempoolChunkedMessageType = "want_tx_chunks"
	ChunkedTxChunks     MempoolChunkedMessageType = "tx_chunks"
	// ChunkedReconstructed is recorded when this node finishes reconstructing
	// the body from K-of-2K chunks and admits it. Not a wire message; sits in
	// the same table so the per-tx timeline is consolidated.
	ChunkedReconstructed MempoolChunkedMessageType = "reconstructed"
	// ChunkedOriginate is recorded when this node admits a tx via local RPC
	// and starts disseminating (Default or Push RPC path). Marks t=0 for the
	// per-tx timeline.
	ChunkedOriginate MempoolChunkedMessageType = "originate"
)

// MempoolChunkedMessage describes the schema for the
// "mempool_chunked_message" table. Each row is one wire-level event keyed
// by tx_key so a downstream analysis can group by tx and order by timestamp.
//
// Field meanings by message type:
//
//	seen_large_tx:    num_chunks = NumParts; payload_bytes = serialized SeenLargeTx size
//	have_tx_chunks:   num_chunks = bits set in the bitmap;     payload_bytes ≈ bitmap bytes
//	want_tx_chunks:   num_chunks = bits set in the bitmap;     payload_bytes ≈ bitmap bytes
//	tx_chunks:        num_chunks = number of chunks in batch;  payload_bytes = sum(chunk.Data)
//	reconstructed:    num_chunks = NumParts;                   payload_bytes = body size
//	originate:        num_chunks = NumParts;                   payload_bytes = body size
type MempoolChunkedMessage struct {
	TxHash       string                    `json:"tx_hash"`
	Peer         string                    `json:"peer"`
	MsgType      MempoolChunkedMessageType `json:"msg_type"`
	TransferType TransferType              `json:"transfer_type"`
	NumChunks    int                       `json:"num_chunks"`
	PayloadBytes int                       `json:"payload_bytes,omitempty"`
	Role         string                    `json:"role,omitempty"`
}

// Table returns the table name for MempoolChunkedMessage.
func (MempoolChunkedMessage) Table() string {
	return MempoolChunkedMessageTable
}

// WriteMempoolChunkedMessage writes a single chunked-mempool wire-event
// trace row. Cheap when tracing is off (returns early via IsCollecting).
func WriteMempoolChunkedMessage(
	client trace.Tracer,
	peer string,
	msgType MempoolChunkedMessageType,
	transferType TransferType,
	txHash []byte,
	numChunks int,
	payloadBytes int,
	role string,
) {
	if !client.IsCollecting(MempoolChunkedMessageTable) {
		return
	}
	client.Write(MempoolChunkedMessage{
		TxHash:       bytes.HexBytes(txHash).String(),
		Peer:         peer,
		MsgType:      msgType,
		TransferType: transferType,
		NumChunks:    numChunks,
		PayloadBytes: payloadBytes,
		Role:         role,
	})
}
