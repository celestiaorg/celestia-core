package schema

import (
	"github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/types"
)

// Table names for mempool tracing. These should be used as the name of the
// table (aka an influxdb measurement) to enforce the mempool tracing schema.
// The measurement name should be the same for both receiving and sending a tx.
const (
	// MempoolTxTable is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to gossiping transactions.
	//
	// The schema for this table is:
	// | time | peerID | tx size | tx hash | transfer type | mempool version |
	MempoolTxTable = "mempool_tx"

	// MempoolPeerState is the tracing "measurement" (aka table) for the mempool
	// that stores tracing data related to mempool state, specifically
	// the gossipping of "SeenTx" and "WantTx".
	//
	// The schema for this table is:
	// | time | peerID | update type | mempool version |
	MempoolPeerStateTable = "mempool_peer_state"

	// LocalTable is the tracing "measurement" (aka table) for the local mempool
	// updates, such as when a tx is added or removed.
	// TODO: actually implement the local mempool tracing
	// LocalTable = "mempool_local"
)

// Field keys for mempool tracing. These should be used as the keys for the
// fields to enforce the mempool tracing schema.
const (
	// TxFieldKey is the tracing field key for receiving for sending a
	// tx. This should take the form of a tx hash as the value.
	TxFieldKey = "tx"

	// SizeFieldKey is the tracing field key for the size of a tx. This
	// should take the form of the size of the tx as the value.
	SizeFieldKey = "size"

	// PeerFieldKey is the tracing field key for the peer that sent or
	// received a tx. This should take the form of the peer's address as the
	// value.
	PeerFieldKey = "peer"

	// TransferTypeFieldKey is the tracing field key for the class of a tx.
	TransferTypeFieldKey = "transfer_type"

	// VersionFieldKey is the tracing field key for the version of the mempool.
	// This is used to distinguish between versions of the mempool.
	VersionFieldKey = "version"

	// UpdateFieldKey is the tracing field key for state updates of the mempool.
	StateUpdateFieldKey = "update"
)

// Field values for mempool tracing. When applicable, these should be used as the values for the
// fields to enforce the mempool tracing schema.
const (
	// TransferTypeDownload is a tracing field value for receiving some
	// data from a peer. This value is used by the "TransferType" field key.
	TransferTypeDownload = "download"

	// TransferTypeUpload is a tracing field value for sending some data
	// to a peer. This value is used by the "TransferType" field key.
	TransferTypeUpload = "upload"

	// V1VersionFieldValue is a tracing field value for the version of
	// the mempool. This value is used by the "version" field key.
	V1VersionFieldValue = "v1"

	// CatVersionFieldValue is a tracing field value for the version of
	// the mempool. This value is used by the "version" field key.
	CatVersionFieldValue = "cat"

	// SeenTxStateUpdateFieldValue is a tracing field value for the state
	// update of the mempool. This value is used by the "update" field key.
	SeenTxStateUpdateFieldValue = "seen_tx"

	// WantTxStateUpdateFieldValue is a tracing field value for the state
	// update of the mempool. This value is used by the "update" field key.
	WantTxStateUpdateFieldValue = "want_tx"

	// RemovedTxStateUpdateFieldValue is a tracing field value for the state
	// update of the mempool. This value is used by the "update" field key.
	RemovedTxStateUpdateFieldValue = "removed_tx"

	// AddedTxStateUpdateFieldValue is a tracing field value for the state
	// update of the mempool. This value is used by the "update" field key.
	AddedTxStateUpdateFieldValue = "added_tx"
)

// MempoolTables returns the list of tables for mempool tracing.
func MempoolTables() []string {
	return []string{
		MempoolTxTable,
		MempoolPeerStateTable,
	}
}

// WriteMempoolTx writes a tracing point for a tx using the predetermined
// schema for mempool tracing. This is used to create a table in the following
// schema:
//
// | time | peerID | tx size | tx hash | transfer type | mempool version |
func WriteMempoolTx(client *trace.Client, peer p2p.ID, tx []byte, transferType, version string) {
	// this check is redundant to what is checked during WritePoint, although it
	// is an optimization to avoid allocations from the txTracingPoint function
	if !client.IsCollecting(MempoolTxTable) {
		return
	}
	client.WritePoint(MempoolTxTable, map[string]interface{}{
		TxFieldKey:           bytes.HexBytes(types.Tx(tx).Hash()).String(),
		PeerFieldKey:         peer,
		SizeFieldKey:         len(tx),
		TransferTypeFieldKey: transferType,
		VersionFieldKey:      version,
	})
}

// WriteMempoolPeerState writes a tracing point for the mempool state using
// the predetermined schema for mempool tracing. This is used to create a table
// in the following schema:
//
// | time | peerID | transfer type | mempool version | state update |
func WriteMempoolPeerState(client *trace.Client, peer p2p.ID, stateUpdate, transferType, version string) {
	// this check is redundant to what is checked during WritePoint, although it
	// is an optimization to avoid allocations from creating the map of fields.
	if !client.IsCollecting(RoundStateTable) {
		return
	}
	client.WritePoint(RoundStateTable, map[string]interface{}{
		PeerFieldKey:         peer,
		TransferTypeFieldKey: transferType,
		VersionFieldKey:      version,
		"state_update":       stateUpdate,
	})
}
