package types

import (
	"bytes"
	"testing"

	context "golang.org/x/net/context"
	"google.golang.org/grpc/metadata"
)

func batchCtx() context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(DeliverTxBatchMetadataKey, DeliverTxBatchMetadataValue))
}

func TestDeliverTxBatchRoundTrip(t *testing.T) {
	txs := [][]byte{{0x0a, 1, 2}, {}, make([]byte, 300)}
	got, ok := DecodeDeliverTxBatch(EncodeDeliverTxBatch(txs))
	if !ok || len(got) != 3 || len(got[2]) != 300 || len(got[1]) != 0 {
		t.Fatalf("bad decode %v %v", ok, got)
	}
	if _, ok := DecodeDeliverTxBatch([]byte{0x0a, 1}); ok {
		t.Fatal("plain tx detected as batch")
	}
	resps := []ResponseDeliverTx{{Code: 1, Log: "x", GasUsed: 7}, {Data: []byte{1}}}
	bz, err := EncodeDeliverTxBatchResponse(resps)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeDeliverTxBatchResponse(bz)
	if err != nil || len(out) != 2 || out[0].GasUsed != 7 || out[0].Log != "x" || out[1].Data[0] != 1 {
		t.Fatalf("bad resp decode %v %v", err, out)
	}
}

type recordingApp struct {
	BaseApplication
	delivered [][]byte
}

func (a *recordingApp) DeliverTx(req RequestDeliverTx) ResponseDeliverTx {
	a.delivered = append(a.delivered, req.Tx)
	//nolint:gosec
	return ResponseDeliverTx{Code: uint32(len(a.delivered)), Data: req.Tx, GasUsed: int64(len(req.Tx))}
}

type preparingApp struct {
	recordingApp
	prepared [][][]byte
}

func (a *preparingApp) PrepareDeliverTxBatch(txs [][]byte) {
	a.prepared = append(a.prepared, txs)
}

func TestGRPCDeliverTxBatch(t *testing.T) {
	txs := [][]byte{{0x0a, 1}, {}, {0x0a, 3, 3}}
	payload := EncodeDeliverTxBatch(txs)

	check := func(t *testing.T, app Application, rec *recordingApp) {
		t.Helper()
		res, err := NewGRPCApplication(app).DeliverTx(batchCtx(), &RequestDeliverTx{Tx: payload})
		if err != nil {
			t.Fatal(err)
		}
		if res.Info != DeliverTxBatchMagic {
			t.Fatalf("expected batch response, got %+v", res)
		}
		if len(rec.delivered) != len(txs) {
			t.Fatalf("expected %d DeliverTx calls, got %d", len(txs), len(rec.delivered))
		}
		for i := range txs {
			if !bytes.Equal(rec.delivered[i], txs[i]) {
				t.Fatalf("tx %d out of order or modified: %v", i, rec.delivered[i])
			}
		}
		resps, err := DecodeDeliverTxBatchResponse(res.Data)
		if err != nil || len(resps) != len(txs) {
			t.Fatalf("bad batch response %v %v", err, resps)
		}
		for i := range txs {
			//nolint:gosec
			if resps[i].Code != uint32(i+1) || !bytes.Equal(resps[i].Data, txs[i]) || resps[i].GasUsed != int64(len(txs[i])) {
				t.Fatalf("response %d mismatch: %+v", i, resps[i])
			}
		}

		// A plain tx still goes through unchanged.
		plain := []byte{0x0a, 9}
		res, err = NewGRPCApplication(app).DeliverTx(context.Background(), &RequestDeliverTx{Tx: plain})
		if err != nil || res.Info == DeliverTxBatchMagic || !bytes.Equal(rec.delivered[len(rec.delivered)-1], plain) {
			t.Fatalf("plain tx not passed through: %v %+v", err, res)
		}
	}

	t.Run("with PrepareDeliverTxBatch", func(t *testing.T) {
		app := &preparingApp{}
		check(t, app, &app.recordingApp)
		if len(app.prepared) != 1 {
			t.Fatalf("expected PrepareDeliverTxBatch to be called once, got %d", len(app.prepared))
		}
		if len(app.prepared[0]) != len(txs) {
			t.Fatalf("PrepareDeliverTxBatch got %d txs", len(app.prepared[0]))
		}
		for i := range txs {
			if !bytes.Equal(app.prepared[0][i], txs[i]) {
				t.Fatalf("PrepareDeliverTxBatch tx %d mismatch", i)
			}
		}
	})
	t.Run("without PrepareDeliverTxBatch", func(t *testing.T) {
		app := &recordingApp{}
		check(t, app, app)
	})
}

// TestGRPCDeliverTxBatchRequiresMetadata checks that a magic-prefixed tx
// without the batch metadata (e.g. a raw tx from a block) is executed as a
// single tx.
func TestGRPCDeliverTxBatchRequiresMetadata(t *testing.T) {
	payload := EncodeDeliverTxBatch([][]byte{{0x0a, 1}, {0x0a, 2}})
	for name, ctx := range map[string]context.Context{
		"no metadata":   context.Background(),
		"outgoing only": metadata.AppendToOutgoingContext(context.Background(), DeliverTxBatchMetadataKey, DeliverTxBatchMetadataValue),
		"wrong value":   metadata.NewIncomingContext(context.Background(), metadata.Pairs(DeliverTxBatchMetadataKey, "v2")),
		"repeated":      metadata.NewIncomingContext(context.Background(), metadata.Pairs(DeliverTxBatchMetadataKey, DeliverTxBatchMetadataValue, DeliverTxBatchMetadataKey, DeliverTxBatchMetadataValue)),
	} {
		app := &preparingApp{}
		res, err := NewGRPCApplication(app).DeliverTx(ctx, &RequestDeliverTx{Tx: payload})
		if err != nil {
			t.Fatal(err)
		}
		if len(app.delivered) != 1 || !bytes.Equal(app.delivered[0], payload) || len(app.prepared) != 0 || res.Info == DeliverTxBatchMagic {
			t.Fatalf("%s: magic payload without batch metadata was not passed through as one tx", name)
		}
	}
}

// TestDecodeDeliverTxBatchMalformed checks that truncated or oversized length
// prefixes are rejected without panicking.
func TestDecodeDeliverTxBatchMalformed(t *testing.T) {
	good := EncodeDeliverTxBatch([][]byte{{1, 2, 3}, make([]byte, 200)})
	firstEnd := len(DeliverTxBatchMagic) + 1 + 3 // a cut after the first tx is a valid 1-tx batch
	for i := len(DeliverTxBatchMagic) + 1; i < len(good); i++ {
		if _, ok := DecodeDeliverTxBatch(good[:i]); ok != (i == firstEnd) {
			t.Fatalf("truncated batch (%d bytes) decoded", i)
		}
	}
	huge := append([]byte(DeliverTxBatchMagic), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01)
	if _, ok := DecodeDeliverTxBatch(huge); ok {
		t.Fatal("oversized length decoded")
	}
	if _, err := DecodeDeliverTxBatchResponse([]byte{0x05, 1}); err == nil {
		t.Fatal("truncated response decoded")
	}
}

func TestGRPCDeliverTxBatchMalformedWithMetadata(t *testing.T) {
	app := &preparingApp{}
	payload := append([]byte(DeliverTxBatchMagic), 0x05, 1)
	if _, err := NewGRPCApplication(app).DeliverTx(batchCtx(), &RequestDeliverTx{Tx: payload}); err == nil {
		t.Fatal("expected an error for a malformed batch")
	}
	if len(app.delivered) != 0 || len(app.prepared) != 0 {
		t.Fatal("malformed batch must not execute anything")
	}
}

func TestGRPCEchoBatchProbe(t *testing.T) {
	app := NewGRPCApplication(&recordingApp{})
	res, err := app.Echo(context.Background(), &RequestEcho{Message: DeliverTxBatchProbe})
	if err != nil || res.Message != DeliverTxBatchProbeAck {
		t.Fatalf("probe not acknowledged: %v %+v", err, res)
	}
	res, err = app.Echo(context.Background(), &RequestEcho{Message: "hello"})
	if err != nil || res.Message != "hello" {
		t.Fatalf("plain echo changed: %v %+v", err, res)
	}
}
