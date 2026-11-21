package types

import "testing"

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
