package types

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"google.golang.org/grpc/metadata"
)

// DeliverTxBatchMetadataKey is the gRPC metadata key, with value
// DeliverTxBatchMetadataValue, that marks a DeliverTx call as a batch. A tx in
// a block cannot carry call metadata, so a raw tx that happens to start with
// DeliverTxBatchMagic is always executed as a single tx.
const (
	DeliverTxBatchMetadataKey   = "x-celestia-delivertx-batch"
	DeliverTxBatchMetadataValue = "v1"
)

// DeliverTxBatchProbe is an Echo message that a batch-capable server answers
// with DeliverTxBatchProbeAck instead of echoing it, so a client can detect
// support without sending a batch.
const (
	DeliverTxBatchProbe    = "celestia-delivertx-batch-v1?"
	DeliverTxBatchProbeAck = "celestia-delivertx-batch-v1"
)

func isDeliverTxBatchCall(ctx context.Context) bool {
	v := metadata.ValueFromIncomingContext(ctx, DeliverTxBatchMetadataKey)
	return len(v) == 1 && v[0] == DeliverTxBatchMetadataValue
}

// DeliverTxBatchMagic prefixes a RequestDeliverTx.Tx that carries a batch of
// length-prefixed txs. Block txs are arbitrary bytes (app v1 accepted
// undecodable txs), so the prefix alone is not a safe marker; see
// DeliverTxBatchMetadataKey.
const DeliverTxBatchMagic = "\xffCELESTIA-DELIVERTX-BATCH-V1"

// EncodeDeliverTxBatch packs txs into a single DeliverTx payload. It is the
// reference encoder for clients (the multiplexer has its own copy) and is
// used by tests.
func EncodeDeliverTxBatch(txs [][]byte) []byte {
	n := len(DeliverTxBatchMagic)
	for _, tx := range txs {
		n += binary.MaxVarintLen64 + len(tx)
	}
	buf := make([]byte, 0, n)
	buf = append(buf, DeliverTxBatchMagic...)
	var tmp [binary.MaxVarintLen64]byte
	for _, tx := range txs {
		k := binary.PutUvarint(tmp[:], uint64(len(tx)))
		buf = append(buf, tmp[:k]...)
		buf = append(buf, tx...)
	}
	return buf
}

// DecodeDeliverTxBatch returns the txs if b is a batch payload.
func DecodeDeliverTxBatch(b []byte) ([][]byte, bool) {
	if !bytes.HasPrefix(b, []byte(DeliverTxBatchMagic)) {
		return nil, false
	}
	b = b[len(DeliverTxBatchMagic):]
	var txs [][]byte
	for len(b) > 0 {
		l, k := binary.Uvarint(b)
		if k <= 0 || uint64(len(b)-k) < l {
			return nil, false
		}
		txs = append(txs, b[k:k+int(l):k+int(l)])
		b = b[k+int(l):]
	}
	return txs, true
}

// EncodeDeliverTxBatchResponse packs responses as length-prefixed protobufs.
func EncodeDeliverTxBatchResponse(resps []ResponseDeliverTx) ([]byte, error) {
	var buf []byte
	var tmp [binary.MaxVarintLen64]byte
	for i := range resps {
		bz, err := resps[i].Marshal()
		if err != nil {
			return nil, err
		}
		k := binary.PutUvarint(tmp[:], uint64(len(bz)))
		buf = append(buf, tmp[:k]...)
		buf = append(buf, bz...)
	}
	return buf, nil
}

// DecodeDeliverTxBatchResponse is the inverse of EncodeDeliverTxBatchResponse.
// It is the reference decoder for clients and is used by tests.
func DecodeDeliverTxBatchResponse(b []byte) ([]ResponseDeliverTx, error) {
	var out []ResponseDeliverTx
	for len(b) > 0 {
		l, k := binary.Uvarint(b)
		if k <= 0 || uint64(len(b)-k) < l {
			return nil, fmt.Errorf("malformed batch response")
		}
		var r ResponseDeliverTx
		if err := r.Unmarshal(b[k : k+int(l)]); err != nil {
			return nil, err
		}
		out = append(out, r)
		b = b[k+int(l):]
	}
	return out, nil
}
