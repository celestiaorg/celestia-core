package types

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/lazyledger/lazyledger-core/libs/protoio"
	"github.com/lazyledger/nmt/namespace"
)

func TestMakeShares(t *testing.T) {
	// resveredTxNamespaceID := append(bytes.Repeat([]byte{0}, 7), 1)
	// resveredIntermediateStateRootsNamespaceID := append(bytes.Repeat([]byte{0}, 7), 2)
	val := NewMockPV()
	blockID := makeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	blockID2 := makeBlockID([]byte("blockhash2"), 1000, []byte("partshash"))
	vote1 := makeVote(t, val, "chainID", 0, 10, 2, 1, blockID, defaultVoteTime)
	vote2 := makeVote(t, val, "chainID", 0, 10, 2, 1, blockID2, defaultVoteTime)
	testEvidence := &DuplicateVoteEvidence{
		VoteA:     vote1,
		VoteB:     vote2,
		Timestamp: defaultVoteTime,
	}
	testEvidenceBytes, err := protoio.MarshalDelimited(testEvidence.ToProto())
	if err != nil {
		t.Fatalf("Could not encode evidence: %v, error: %v", testEvidence, err)
	}
	resveredEvidenceNamespaceID := append(bytes.Repeat([]byte{0}, 7), 3)
	type args struct {
		data      []proto.Message
		shareSize int
		nidFunc   func(elem interface{}) namespace.ID
	}
	tests := []struct {
		name string
		args args
		want NamespacedShares
	}{
		{"evidence", args{
			data:      []proto.Message{testEvidence.ToProto()},
			shareSize: ShareSize,
			nidFunc: func(elem interface{}) namespace.ID {
				return resveredEvidenceNamespaceID
			},
		}, NamespacedShares{NamespacedShare{
			Share: testEvidenceBytes[:ShareSize],
			ID:    resveredEvidenceNamespaceID,
		}, NamespacedShare{
			Share: zeroPadIfNecessary(testEvidenceBytes[ShareSize:], ShareSize),
			ID:    resveredEvidenceNamespaceID,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MakeShares(tt.args.data, tt.args.shareSize, tt.args.nidFunc); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("len(MakeShares()) = %v want %v", len(got), len(tt.want))
				t.Errorf("MakeShares() = %v\n want %v", got, tt.want)
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
		t.Run(tt.name, func(t *testing.T) {
			if got := zeroPadIfNecessary(tt.args.share, tt.args.width); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("zeroPadIfNecessary() = %v, want %v", got, tt.want)
			}
		})
	}
}
