package types

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/lazyledger/lazyledger-core/libs/protoio"
	"github.com/lazyledger/nmt/namespace"
)

func TestMakeShares(t *testing.T) {
	reservedTxNamespaceID := append(bytes.Repeat([]byte{0}, 7), 1)
	reservedEvidenceNamespaceID := append(bytes.Repeat([]byte{0}, 7), 3)
	evidenceNidFunc := func(elem interface{}) namespace.ID {
		return reservedEvidenceNamespaceID
	}
	txNidFunc := func(elem interface{}) namespace.ID {
		return reservedTxNamespaceID
	}
	msgNidFunc := func(elem interface{}) namespace.ID {
		msg, ok := elem.(Message)
		if !ok {
			panic("method called on other type than Message")
		}
		return msg.NamespaceID
	}
	// resveredIntermediateStateRootsNamespaceID := append(bytes.Repeat([]byte{0}, 7), 2)
	val := NewMockPV()
	blockID := makeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	blockID2 := makeBlockID([]byte("blockhash2"), 1000, []byte("partshash"))
	vote1 := makeVote(t, val, "chainID", 0, 10, 2, 1, blockID, defaultVoteTime)
	vote2 := makeVote(t, val, "chainID", 0, 10, 2, 1, blockID2, defaultVoteTime)
	testEvidence := &DuplicateVoteEvidence{
		VoteA:     vote1,
		VoteB:     vote2,
	}
	testEvidenceBytes, err := protoio.MarshalDelimited(testEvidence.ToProto())
	largeTx := Tx(bytes.Repeat([]byte("large Tx"), 50))
	largeTxLenDelimited, _ := largeTx.MarshalDelimited()
	smolTx := Tx("small Tx")
	smolTxLenDelimited, _ := smolTx.MarshalDelimited()
	msg1 := Message{
		NamespaceID: namespace.ID("8bytesss"),
		Data:        []byte("some data"),
	}
	msg1Marshaled, _ := msg1.MarshalDelimited()
	if err != nil {
		t.Fatalf("Could not encode evidence: %v, error: %v", testEvidence, err)
	}
	type args struct {
		data      []LenDelimitedMarshaler
		shareSize int
		nidFunc   func(elem interface{}) namespace.ID
	}
	tests := []struct {
		name string
		args args
		want NamespacedShares
	}{
		{"evidence",
			args{
				data:      []LenDelimitedMarshaler{ProtoLenDelimitedMarshaler{testEvidence.ToProto()}},
				shareSize: ShareSize,
				nidFunc:   evidenceNidFunc,
			}, NamespacedShares{NamespacedShare{
				Share: testEvidenceBytes[:ShareSize],
				ID:    reservedEvidenceNamespaceID,
			}, NamespacedShare{
				Share: zeroPadIfNecessary(testEvidenceBytes[ShareSize:], ShareSize),
				ID:    reservedEvidenceNamespaceID,
			}},
		},
		{"small LL Tx",
			args{
				data:      []LenDelimitedMarshaler{smolTx},
				shareSize: ShareSize,
				nidFunc:   txNidFunc,
			},
			NamespacedShares{
				NamespacedShare{
					Share: zeroPadIfNecessary(smolTxLenDelimited, ShareSize),
					ID:    reservedTxNamespaceID,
				},
			},
		},
		{"one large LL Tx",
			args{
				data:      []LenDelimitedMarshaler{largeTx},
				shareSize: ShareSize,
				nidFunc:   txNidFunc,
			},
			NamespacedShares{
				NamespacedShare{
					Share: Share(largeTxLenDelimited[:ShareSize]),
					ID:    reservedTxNamespaceID,
				},
				NamespacedShare{
					Share: zeroPadIfNecessary(largeTxLenDelimited[ShareSize:], ShareSize),
					ID:    reservedTxNamespaceID,
				},
			},
		},
		{"ll-app message",
			args{
				data:      []LenDelimitedMarshaler{msg1},
				shareSize: ShareSize,
				nidFunc:   msgNidFunc,
			},
			NamespacedShares{
				NamespacedShare{zeroPadIfNecessary(msg1Marshaled, ShareSize), msg1.NamespaceID},
			},
		},
	}
	for _, tt := range tests {
		tt := tt // stupid scopelint :-/
		t.Run(tt.name, func(t *testing.T) {
			if got := makeShares(tt.args.data, tt.args.shareSize, tt.args.nidFunc); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("makeShares() = %v\n want %v", got, tt.want)
			}
		})
	}
}

func Test_zeroPadIfNecessary(t *testing.T) {
	type args struct {
		share []byte
		width int
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		{"pad", args{[]byte{1, 2, 3}, 6}, []byte{1, 2, 3, 0, 0, 0}},
		{"not necessary (equal to shareSize)", args{[]byte{1, 2, 3}, 3}, []byte{1, 2, 3}},
		{"not necessary (greater shareSize)", args{[]byte{1, 2, 3}, 2}, []byte{1, 2, 3}},
	}
	for _, tt := range tests {
		tt := tt // stupid scopelint :-/
		t.Run(tt.name, func(t *testing.T) {
			if got := zeroPadIfNecessary(tt.args.share, tt.args.width); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("zeroPadIfNecessary() = %v, want %v", got, tt.want)
			}
		})
	}
}
