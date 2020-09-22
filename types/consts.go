package types

import "github.com/lazyledger/nmt/namespace"

const (
	// ShareSize is the size of a share (in bytes).
	// see: https://github.com/lazyledger/lazyledger-specs/blob/master/specs/consensus.md#constants
	ShareSize = 256

	// NamespaceSize is the namespace size in bytes.
	NamespaceSize = 8
)

var (
	TxNamespaceID                     = namespace.ID{0, 0, 0, 0, 0, 0, 0, 1}
	IntermediateStateRootsNamespaceID = namespace.ID{0, 0, 0, 0, 0, 0, 0, 2}
	EvidenceNamespaceID               = namespace.ID{0, 0, 0, 0, 0, 0, 0, 3}

	TailPaddingNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE}
	ParitySharesNamespaceID = namespace.ID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	// private helper methods that can be used in MakeShares
	txNIDFunc = func(elem interface{}) namespace.ID {
		return TxNamespaceID
	}
	intermediateRootsNIDFunc = func(elem interface{}) namespace.ID {
		return IntermediateStateRootsNamespaceID
	}
	evidenceNIDFunc = func(elem interface{}) namespace.ID {
		return EvidenceNamespaceID
	}
	msgNidFunc = func(elem interface{}) namespace.ID {
		msg, ok := elem.(Message)
		if !ok {
			panic("method called on other type than Message")
		}
		return msg.NamespaceID
	}
)
