package cat

import (
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cometbft/cometbft/types"
)

// Message classes the app fills separately, in this order, when it builds a block.
const (
	classSDK = iota
	classPFB
	classPFF
	// classUnknown is a tx core cannot place. While one may still be rechecked, a proposal
	// does not stop waiting early for a class that has run out of txs.
	classUnknown
	numClasses
)

const payForFibreURL = "/celestia.fibre.v1.MsgPayForFibre" //nolint:misspell // proto package name

// maxBlockMsgs is types.MaxBlockMsgsForAppVersion; tests shrink the caps.
var maxBlockMsgs = types.MaxBlockMsgsForAppVersion

// classNeeds returns, per class, how many simple txs a rechecked prefix needs before a
// proposal can stop waiting: the app's cap plus a margin for txs PrepareProposal drops for
// reasons core cannot see. ok is false for an app version whose caps core does not know.
func classNeeds(appVersion uint64) (need [classUnknown]int64, ok bool) {
	sdk, pfb, pff, ok := maxBlockMsgs(appVersion)
	// SDK txs are filled first, so they see no more spent state than at recheck; blob txs
	// come next; a PayForFibre can pass CheckTx and still fail to settle, so its margin is 2x cap.
	return [classUnknown]int64{int64(sdk + sdk/4), int64(pfb + pfb/2), int64(pff + 2*pff)}, ok
}

// classifyTx returns the class the app's PrepareProposal sorts tx into, and whether it is
// simple: one message the app counts once and never executes while building a block, and
// no timeout. It trusts the encoding CheckTx enforces; only accepted txs keep their class.
func classifyTx(tx []byte) (class uint8, simple bool) {
	var body, typeID []byte
	if !protoFields(tx, func(num protowire.Number, typ protowire.Type, v []byte) bool {
		switch {
		case num == 1 && typ == protowire.BytesType:
			body = v // TxRaw.body_bytes or BlobTx.tx; the last one wins, as in protobuf
		case num == 3 && typ == protowire.BytesType:
			typeID = v // BlobTx.type_id; CheckTx rejects a TxRaw whose last signature is "BLOB"
		}
		return true
	}) {
		return classUnknown, false
	}
	if string(typeID) == squaretx.ProtoBlobTxTypeID {
		// CheckTx admits one MsgPayForBlobs per BlobTx; body is its TxRaw.
		var inner []byte
		ok := protoFields(body, func(num protowire.Number, typ protowire.Type, v []byte) bool {
			if num == 1 && typ == protowire.BytesType {
				inner = v
			}
			return true
		})
		_, timeout, ok2 := scanBody(inner)
		return classPFB, ok && ok2 && !timeout
	}
	msgs, timeout, ok := scanBody(body)
	if !ok || msgs.n == 0 || msgs.unknown {
		return classUnknown, false
	}
	if msgs.pff {
		if msgs.n != 1 { // CheckTx rejects a PayForFibre beside other messages
			return classUnknown, false
		}
		return classPFF, !timeout
	}
	return classSDK, msgs.n == 1 && msgs.simple && !timeout
}

type bodyMsgs struct {
	n       int
	pff     bool // a MsgPayForFibre
	unknown bool // a message the app executes or caps in a proposal in ways core does not model
	simple  bool // the last message is one the app counts once and does not execute in a proposal
}

// scanBody reads a TxBody's messages, and reports whether it sets a timeout or unordered.
func scanBody(body []byte) (msgs bodyMsgs, timeout, ok bool) {
	ok = protoFields(body, func(num protowire.Number, typ protowire.Type, v []byte) bool {
		switch num {
		case 1: // messages
			if typ != protowire.BytesType {
				return false
			}
			url, ok := anyURL(v)
			if !ok {
				return false
			}
			msgs.n++
			msgs.simple = false
			switch string(url) {
			case payForFibreURL:
				msgs.pff = true
			case "/celestia.fibre.v1.MsgDepositToEscrow", //nolint:misspell // proto package name
				"/celestia.fibre.v1.MsgPaymentPromiseTimeout", //nolint:misspell // proto package name
				"/celestia.zkism.v1.MsgCreateInterchainSecurityModule",
				"/celestia.zkism.v1.MsgUpdateInterchainSecurityModule",
				"/celestia.zkism.v1.MsgSubmitMessages",
				"/celestia.zkism.v1.MsgUpdateVerifyingKeys",
				"/hyperlane.core.v1.MsgProcessMessage":
				msgs.unknown = true
			case "/cosmos.bank.v1beta1.MsgSend",
				"/ibc.applications.transfer.v1.MsgTransfer",
				"/cosmos.staking.v1beta1.MsgDelegate",
				"/cosmos.staking.v1beta1.MsgUndelegate",
				"/cosmos.staking.v1beta1.MsgBeginRedelegate",
				"/cosmos.distribution.v1beta1.MsgWithdrawDelegatorReward":
				msgs.simple = true
			}
		case 3, 4, 5: // timeout_height, unordered, timeout_timestamp
			timeout = true
		}
		return true
	})
	return msgs, timeout, ok
}

// anyURL returns a google.protobuf.Any's type_url; the last one wins, as in protobuf.
func anyURL(b []byte) (url []byte, ok bool) {
	ok = protoFields(b, func(num protowire.Number, typ protowire.Type, v []byte) bool {
		if num == 1 && typ == protowire.BytesType {
			url = v
		}
		return true
	})
	return url, ok
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
