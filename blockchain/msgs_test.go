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
			BlockResponse: &bcproto.BlockResponse{Block: bpb}}}, "1ac0020abd020a5b0a02080b1803220b088092b8c398feffffff012a0212003a2016106406aededa633a265efd5833af53775af5a27c803518313c9b77e68875926a20e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85512130a0b48656c6c6f20576f726c6412001a0022001ac8010a3000000000000000010000000000000001266fcca850585e31b54074399b0dbb5b04b408b77d5f357a528ed3e04b63c3c10a30ffffffffffffffffffffffffffffffffe04e02811b28234428f1c1219d5627f61c02c415c175bdabd9e7934519baef07123000000000000000010000000000000001266fcca850585e31b54074399b0dbb5b04b408b77d5f357a528ed3e04b63c3c11230ffffffffffffffffffffffffffffffffe04e02811b28234428f1c1219d5627f61c02c415c175bdabd9e7934519baef07"},
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
