package types

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

// muxBatchEnabled gates server-side recognition of DeliverTx batch payloads in
// GRPCApplication.DeliverTx (sync-speedup prototype). It is read once at
// process start from CELESTIA_MUX_BATCH=1; the embedded child process inherits
// the multiplexer's environment.
//
// WARNING: set this only on sync-only nodes driven by the batching
// multiplexer, never on validators or other consensus-participating nodes.
// With the flag on, a raw tx that happens to start with DeliverTxBatchMagic
// would be executed as its inner txs instead of as a single tx, which would
// diverge from nodes that run without the flag. When the flag is off (the
// default), every payload is passed to Application.DeliverTx unchanged.
var muxBatchEnabled = os.Getenv("CELESTIA_MUX_BATCH") == "1"

// DeliverTxBatchMagic prefixes a RequestDeliverTx.Tx that carries a batch of
// length-prefixed txs (sync-speedup prototype). Real txs are protobuf-encoded
// and cannot start with 0xff, so the prefix is unambiguous.
const DeliverTxBatchMagic = "\xffCELESTIA-DELIVERTX-BATCH-V1"

// EncodeDeliverTxBatch packs txs into a single DeliverTx payload.
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
		txs = append(txs, b[k:k+int(l)])
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
