package cat

import (
	"math"

	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cometbft/cometbft/types"
)

// Message classes the app caps separately when it fills a block.
const (
	classSDK = iota
	classPFB
	classPFF
	// classUnknown is charged to no class and never capped, and the reap waits for every
	// such tx, so a tx core cannot classify can only make a reap wait longer.
	classUnknown
	numClasses
)

const (
	payForFibreURL = "/celestia.fibre.v1.MsgPayForFibre" //nolint:misspell // proto package name
	msgExecURL     = "/cosmos.authz.v1beta1.MsgExec"
)

// maxBlockMsgs is types.MaxBlockMsgsForAppVersion; tests shrink the caps.
var maxBlockMsgs = types.MaxBlockMsgsForAppVersion

// classQuotas returns the reap's per-class message caps for appVersion: the app's cap plus
// the reap's 25% over-reap. Without caps every class is unlimited.
func classQuotas(appVersion uint64) (q [numClasses]int64) {
	for c := range q {
		q[c] = math.MaxInt64
	}
	if sdk, pfb, pff, ok := maxBlockMsgs(appVersion); ok {
		for c, n := range [...]int{sdk, pfb, pff} {
			q[c] = int64(n + n/4)
		}
	}
	return q
}

// classifyTx returns the class the app's PrepareProposal sorts tx into and a weight that
// never exceeds the app's message count for it. Anything ambiguous is classUnknown.
func classifyTx(tx []byte) (class uint8, weight int64) {
	if _, isBlob, err := squaretx.UnmarshalBlobTxNoCopy(tx); isBlob {
		if err != nil {
			return classUnknown, 1
		}
		return classPFB, 1 // CheckTx admits one MsgPayForBlobs per BlobTx
	}
	var body []byte
	if !protoFields(tx, func(num protowire.Number, typ protowire.Type, v []byte) bool {
		if num == 1 && typ == protowire.BytesType {
			body = v // TxRaw.body_bytes; the last one wins, as in protobuf
		}
		return true
	}) {
		return classUnknown, 1
	}
	msgs, pffs := 0, 0
	if !protoFields(body, func(num protowire.Number, typ protowire.Type, v []byte) bool {
		if num != 1 { // TxBody.messages
			return true
		}
		if typ != protowire.BytesType {
			return false
		}
		url, val, ok := anyParts(v)
		if !ok {
			return false
		}
		msgs++
		weight++
		switch url {
		case payForFibreURL:
			pffs++
		case msgExecURL: // the app also counts each message a MsgExec dispatches (field 2)
			return protoFields(val, func(num protowire.Number, typ protowire.Type, _ []byte) bool {
				if num == 2 && typ == protowire.BytesType {
					weight++
				}
				return true
			})
		}
		return true
	}) || msgs == 0 {
		return classUnknown, 1
	}
	if pffs > 0 {
		if msgs != 1 { // CheckTx rejects a PayForFibre beside other messages
			return classUnknown, 1
		}
		return classPFF, 1
	}
	return classSDK, weight
}

// anyParts returns a google.protobuf.Any's type_url and value.
func anyParts(b []byte) (url string, val []byte, ok bool) {
	ok = protoFields(b, func(num protowire.Number, typ protowire.Type, v []byte) bool {
		switch {
		case num == 1 && typ == protowire.BytesType:
			url = string(v)
		case num == 2 && typ == protowire.BytesType:
			val = v
		}
		return true
	})
	return url, val, ok
}

// protoFields calls fn for each field of a protobuf message (v is set for bytes fields);
// false on malformed input or when fn returns false.
func protoFields(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte) bool) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		var v []byte
		if typ == protowire.BytesType {
			v, n = protowire.ConsumeBytes(b)
		} else {
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return false
		}
		b = b[n:]
		if !fn(num, typ, v) {
			return false
		}
	}
	return true
}
