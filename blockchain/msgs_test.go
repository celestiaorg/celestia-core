package blockchain

import (
	"encoding/hex"
	"math"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bcproto "github.com/lazyledger/lazyledger-core/proto/tendermint/blockchain"
	"github.com/lazyledger/lazyledger-core/types"
)

func TestBcBlockRequestMessageValidateBasic(t *testing.T) {
	testCases := []struct {
		testName      string
		requestHeight int64
		expectErr     bool
	}{
		{"Valid Request Message", 0, false},
		{"Valid Request Message", 1, false},
		{"Invalid Request Message", -1, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			request := bcproto.BlockRequest{Height: tc.requestHeight}
			assert.Equal(t, tc.expectErr, ValidateMsg(&request) != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestBcNoBlockResponseMessageValidateBasic(t *testing.T) {
	testCases := []struct {
		testName          string
		nonResponseHeight int64
		expectErr         bool
	}{
		{"Valid Non-Response Message", 0, false},
		{"Valid Non-Response Message", 1, false},
		{"Invalid Non-Response Message", -1, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			nonResponse := bcproto.NoBlockResponse{Height: tc.nonResponseHeight}
			assert.Equal(t, tc.expectErr, ValidateMsg(&nonResponse) != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestBcStatusRequestMessageValidateBasic(t *testing.T) {
	request := bcproto.StatusRequest{}
	assert.NoError(t, ValidateMsg(&request))
}

func TestBcStatusResponseMessageValidateBasic(t *testing.T) {
	testCases := []struct {
		testName       string
		responseHeight int64
		expectErr      bool
	}{
		{"Valid Response Message", 0, false},
		{"Valid Response Message", 1, false},
		{"Invalid Response Message", -1, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			response := bcproto.StatusResponse{Height: tc.responseHeight}
			assert.Equal(t, tc.expectErr, ValidateMsg(&response) != nil, "Validate Basic had an unexpected result")
		})
	}
}

// nolint:lll // ignore line length in tests
func TestBlockchainMessageVectors(t *testing.T) {
	block := types.MakeBlock(int64(3), []types.Tx{types.Tx("Hello World")}, nil, nil, types.Messages{}, nil)
	block.Version.Block = 11 // overwrite updated protocol version

	bpb, err := block.ToProto()
	require.NoError(t, err)

	testCases := []struct {
		testName string
		bmsg     proto.Message
		expBytes string
	}{
		{"BlockRequestMessage", &bcproto.Message{Sum: &bcproto.Message_BlockRequest{
			BlockRequest: &bcproto.BlockRequest{Height: 1}}}, "0a020801"},
		{"BlockRequestMessage", &bcproto.Message{Sum: &bcproto.Message_BlockRequest{
			BlockRequest: &bcproto.BlockRequest{Height: math.MaxInt64}}},
			"0a0a08ffffffffffffffff7f"},
		{"BlockResponseMessage", &bcproto.Message{Sum: &bcproto.Message_BlockResponse{
			BlockResponse: &bcproto.BlockResponse{Block: bpb}}}, "1ac0020abd020a5b0a02080b1803220b088092b8c398feffffff012a0212003a20ba28ef83fed712be8a128587469a4effda9f4f4895bd5638dab1f10775c9bed66a20e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85512130a0b48656c6c6f20576f726c6412001a0022001ac8010a300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d0a30ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e812300000000000000001000000000000000119251d276ad8a1831db7b86ead3f42c4e03093d50ecf026da7ecc3b0da8ec87d1230ffffffffffffffffffffffffffffffff12d55aea72367d0d6a7899103a437c913ee5a6f9e86f42e0fe8743b0d8d3a1e8"},
		{"NoBlockResponseMessage", &bcproto.Message{Sum: &bcproto.Message_NoBlockResponse{
			NoBlockResponse: &bcproto.NoBlockResponse{Height: 1}}}, "12020801"},
		{"NoBlockResponseMessage", &bcproto.Message{Sum: &bcproto.Message_NoBlockResponse{
			NoBlockResponse: &bcproto.NoBlockResponse{Height: math.MaxInt64}}},
			"120a08ffffffffffffffff7f"},
		{"StatusRequestMessage", &bcproto.Message{Sum: &bcproto.Message_StatusRequest{
			StatusRequest: &bcproto.StatusRequest{}}},
			"2200"},
		{"StatusResponseMessage", &bcproto.Message{Sum: &bcproto.Message_StatusResponse{
			StatusResponse: &bcproto.StatusResponse{Height: 1, Base: 2}}},
			"2a0408011002"},
		{"StatusResponseMessage", &bcproto.Message{Sum: &bcproto.Message_StatusResponse{
			StatusResponse: &bcproto.StatusResponse{Height: math.MaxInt64, Base: math.MaxInt64}}},
			"2a1408ffffffffffffffff7f10ffffffffffffffff7f"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			bz, _ := proto.Marshal(tc.bmsg)

			require.Equal(t, tc.expBytes, hex.EncodeToString(bz))
		})
	}
}
