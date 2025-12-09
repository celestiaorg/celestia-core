package headersync

import (
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/assert"

	hsproto "github.com/cometbft/cometbft/proto/tendermint/headersync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

func TestValidateMsg(t *testing.T) {
	testCases := []struct {
		name    string
		msg     proto.Message
		wantErr bool
	}{
		{
			name:    "nil message",
			msg:     nil,
			wantErr: true,
		},
		{
			name:    "valid status response",
			msg:     &hsproto.StatusResponse{Base: 1, Height: 100},
			wantErr: false,
		},
		{
			name:    "invalid status response - negative base",
			msg:     &hsproto.StatusResponse{Base: -1, Height: 100},
			wantErr: true,
		},
		{
			name:    "invalid status response - negative height",
			msg:     &hsproto.StatusResponse{Base: 1, Height: -1},
			wantErr: true,
		},
		{
			name:    "invalid status response - height less than base",
			msg:     &hsproto.StatusResponse{Base: 100, Height: 50},
			wantErr: true,
		},
		{
			name:    "valid get headers",
			msg:     &hsproto.GetHeaders{StartHeight: 1, Count: 10},
			wantErr: false,
		},
		{
			name:    "invalid get headers - start height < 1",
			msg:     &hsproto.GetHeaders{StartHeight: 0, Count: 10},
			wantErr: true,
		},
		{
			name:    "invalid get headers - count < 1",
			msg:     &hsproto.GetHeaders{StartHeight: 1, Count: 0},
			wantErr: true,
		},
		{
			name:    "invalid get headers - count exceeds max",
			msg:     &hsproto.GetHeaders{StartHeight: 1, Count: MaxHeaderBatchSize + 1},
			wantErr: true,
		},
		{
			name:    "valid headers response - empty",
			msg:     &hsproto.HeadersResponse{Headers: []*hsproto.SignedHeader{}},
			wantErr: false,
		},
		{
			name: "valid headers response - with headers",
			msg: &hsproto.HeadersResponse{
				Headers: []*hsproto.SignedHeader{
					{Header: &cmtproto.Header{}, Commit: &cmtproto.Commit{}},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid headers response - nil header in array",
			msg: &hsproto.HeadersResponse{
				Headers: []*hsproto.SignedHeader{nil},
			},
			wantErr: true,
		},
		{
			name: "invalid headers response - nil Header field",
			msg: &hsproto.HeadersResponse{
				Headers: []*hsproto.SignedHeader{
					{Header: nil, Commit: &cmtproto.Commit{}},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid headers response - nil Commit field",
			msg: &hsproto.HeadersResponse{
				Headers: []*hsproto.SignedHeader{
					{Header: &cmtproto.Header{}, Commit: nil},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMsg(tc.msg)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
