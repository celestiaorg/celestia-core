package mempool

import (
	"github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/types"
)

const (
	// MeasurementTracingTag is the tracing tag for the mempool
	MeasurementTracingTag = "mempool"

	// TxTracingFieldKey is the tracing field key for receiving for sending a
	// tx. This should take the form of a tx hash as the value.
	TxTracingFieldKey = "tx"

	// SizeTracingFieldKey is the tracing field key for the size of a tx. This
	// should take the form of the size of the tx as the value.
	SizeTracingFieldKey = "size"

	// PeerTracingFieldKey is the tracing field key for the peer that sent or
	// received a tx. This should take the form of the peer's address as the
	// value.
	PeerTracingFieldKey = "peer"

	// ClassTracingFieldKey is the tracing field key for the class of a tx. This should use either the send or receive value.
	ClassTracingFieldKey = "class"

	// ReceiveTracingFieldValue is the tracing field value for receiving some
	// data from a peer. This value is used by the "class" field key.
	ReceiveTracingFieldValue = "receive"

	// SendTracingFieldValue is the tracing field value for sending some data
	// to a peer. This value is used by the "class" field key.
	SendTracingFieldValue = "send"
)

// TxTracingPoint returns a tracing point for a tx using the predetermined
// schema for mempool tracing. This can be used for either receiving or sending
// a tx (change the class).
//
// This is used to create a table in the following schema:
// | time | peerID | class (receiving or sending) | tx size | tx hash |
func TxTracingPoint(class string, peer p2p.ID, tx []byte) map[string]interface{} {
	return map[string]interface{}{
		TxTracingFieldKey:    bytes.HexBytes(types.Tx(tx).Hash()).String(),
		PeerTracingFieldKey:  peer,
		SizeTracingFieldKey:  len(tx),
		ClassTracingFieldKey: class,
	}
}
