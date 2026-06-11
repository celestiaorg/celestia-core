package blocksync

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// The RecvMessagePrecheck function wiring should be done correctly on the channel
func TestBlocksyncChannelPrecheck(t *testing.T) {
	channel := (&Reactor{}).GetChannels()[0]
	require.NotNil(t, channel, "blocksync channel descriptor not found")
	require.NotNil(t, channel.RecvMessagePrecheck, "blocksync channel must set RecvMessagePrecheck")

	require.ErrorIs(t, channel.RecvMessagePrecheck(blockResponseBytes(t, types.MaxVotesCount+1, 0)), errTooManySigs)
	require.NoError(t, channel.RecvMessagePrecheck(blockResponseBytes(t, 10, 0)))
}

func TestValidateBlockSyncBytes(t *testing.T) {
	tests := []struct {
		name     string
		nSigs    int
		nExtSigs int
		wantErr  bool
	}{
		{"empty", 0, 0, false},
		{"under limit", 100, 100, false},
		{"at limit", types.MaxVotesCount, types.MaxVotesCount, false},
		{"commit over limit", types.MaxVotesCount + 1, 0, true},
		{"ext commit over limit", 0, types.MaxVotesCount + 1, true},
		{"both over limit", types.MaxVotesCount + 1, types.MaxVotesCount + 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBlockSyncBytes(blockResponseBytes(t, tc.nSigs, tc.nExtSigs))
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Other blocksync proto messages should always pass.
func TestValidateBlockSyncBytes_OtherMessages(t *testing.T) {
	msgs := []*bcproto.Message{
		{Sum: &bcproto.Message_BlockRequest{BlockRequest: &bcproto.BlockRequest{Height: 5}}},
		{Sum: &bcproto.Message_NoBlockResponse{NoBlockResponse: &bcproto.NoBlockResponse{Height: 5}}},
		{Sum: &bcproto.Message_StatusRequest{StatusRequest: &bcproto.StatusRequest{}}},
		{Sum: &bcproto.Message_StatusResponse{StatusResponse: &bcproto.StatusResponse{Height: 5, Base: 1}}},
	}
	for _, m := range msgs {
		require.NoError(t, validateBlockSyncBytes(mustMarshal(t, m)))
	}
}

// Arbitrary bytes are rejected. Empty message(nil struct) is always valid.
func TestValidateBlockSyncBytes_Malformed(t *testing.T) {
	require.NoError(t, validateBlockSyncBytes(nil))

	require.Error(t, validateBlockSyncBytes([]byte{0xff, 0xff, 0xff}))
	require.Error(t, validateBlockSyncBytes([]byte{0x1a, 0x05}))
}

// Makes sure that a peer can't dodge the signature cap by splitting the
// signatures across duplicate fields: proto decoding merges duplicates at
// every level, so the counts must sum. The marshaller never emits duplicates,
// so the wire bytes are hand-encoded.
func TestValidateBlockSyncBytes_DuplicatedFields(t *testing.T) {
	half := types.MaxVotesCount/2 + 1 // each copy is legal on its own; the merged sum is not

	commit := mustMarshal(t, commitWithSigs(half))
	block := mustMarshal(t, &cmtproto.Block{LastCommit: commitWithSigs(half)})
	extCommit := mustMarshal(t, extCommitWithSigs(half))
	message := blockResponseBytes(t, half, 0)

	// Field numbers are read off the real generated protos, not hardcoded.
	blockResponseNum := protoFieldNum(t, bcproto.Message_BlockResponse{}, "BlockResponse")
	blockNum := protoFieldNum(t, bcproto.BlockResponse{}, "Block")
	extCommitNum := protoFieldNum(t, bcproto.BlockResponse{}, "ExtCommit")
	lastCommitNum := protoFieldNum(t, cmtproto.Block{}, "LastCommit")

	twoMessages := append(append([]byte{}, message...), message...)
	twoBlockResponses := wireField(blockResponseNum, wireField(blockNum, block), wireField(blockNum, block))
	twoBlocks := wireField(blockResponseNum, wireField(blockNum, block, block))
	twoLastCommits := wireField(blockResponseNum, wireField(blockNum, wireField(lastCommitNum, commit, commit)))
	twoExtCommits := wireField(blockResponseNum, wireField(extCommitNum, extCommit, extCommit))

	tests := []struct {
		name    string
		msg     []byte
		wantErr error
	}{
		{"two Messages concatenated", twoMessages, errTooManySigs},
		{"duplicate block_response in one Message", twoBlockResponses, errTooManySigs},
		{"duplicate block in one BlockResponse", twoBlocks, errTooManySigs},
		{"duplicate last_commit in one Block", twoLastCommits, errTooManySigs},
		{"duplicate ext_commit in one BlockResponse", twoExtCommits, errTooManyExtendedsigs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, validateBlockSyncBytes(tc.msg), tc.wantErr)
		})
	}
}

// Precheck should not return error when sub messages are missing
func TestValidateBlockSyncBytes_AbsentSubMessages(t *testing.T) {
	emptyBlockResponse := mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{})) // Block and ExtCommit nil
	extCommitOnly := mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{ExtCommit: &cmtproto.ExtendedCommit{}}))
	blockWithoutLastCommit := mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{Block: &cmtproto.Block{}}))

	require.NoError(t, validateBlockSyncBytes(emptyBlockResponse))
	require.NoError(t, validateBlockSyncBytes(extCommitOnly))
	require.NoError(t, validateBlockSyncBytes(blockWithoutLastCommit))
}

// Simulates a future proto upgrade: a newly added field is unknown to the
// SigCount stubs, gets skipped, and the signature cap still applies.
func TestValidateBlockSyncBytes_NewFieldsStillCapped(t *testing.T) {
	newField := wireField(15, []byte("future field")) // no message on the path uses field 15

	// New field on the outer Message, next to block_response.
	overLimit := append(blockResponseBytes(t, types.MaxVotesCount+1, 0), newField...)
	require.ErrorIs(t, validateBlockSyncBytes(overLimit), errTooManySigs)

	// New field inside the Commit, right next to the signatures.
	commit := append(mustMarshal(t, commitWithSigs(types.MaxVotesCount+1)), newField...)
	overLimit = wireField(3, wireField(1, wireField(4, commit))) // Message.block_response > BlockResponse.block > Block.last_commit
	require.ErrorIs(t, validateBlockSyncBytes(overLimit), errTooManySigs)

	// A legal count still passes with the new field present.
	underLimit := append(blockResponseBytes(t, 10, 0), newField...)
	require.NoError(t, validateBlockSyncBytes(underLimit))
}

// Catches BlockResponse proto diverging from the stub.
func TestSigCountStubFieldNumbersMatchRealProtos(t *testing.T) {
	pairs := []struct {
		name      string
		real      interface{}
		realField string
		stub      interface{}
		stubField string
	}{
		{"Message.block_response", bcproto.Message_BlockResponse{}, "BlockResponse", bcproto.SigCountMessage{}, "BlockResponse"},
		{"BlockResponse.block", bcproto.BlockResponse{}, "Block", bcproto.SigCountBlockResponse{}, "Block"},
		{"BlockResponse.ext_commit", bcproto.BlockResponse{}, "ExtCommit", bcproto.SigCountBlockResponse{}, "ExtCommit"},
		{"Block.last_commit", cmtproto.Block{}, "LastCommit", bcproto.SigCountBlock{}, "LastCommit"},
		{"Commit.signatures", cmtproto.Commit{}, "Signatures", bcproto.SigCountCommit{}, "Signatures"},
		{"ExtendedCommit.extended_signatures", cmtproto.ExtendedCommit{}, "ExtendedSignatures", bcproto.SigCountExtendedCommit{}, "ExtendedSignatures"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			require.Equal(t, protoFieldNum(t, p.real, p.realField), protoFieldNum(t, p.stub, p.stubField),
				"stub field number drifted from the real proto — update stub.proto and regenerate")
		})
	}
}

// Unmarshalling into the stub must cost O(1) allocations regardless of how
// many signatures the message encodes — that's the entire point of NoSig.
func TestStubUnmarshalAllocs(t *testing.T) {
	tests := []struct {
		name     string
		nSigs    int
		nExtSigs int
	}{
		{"10k commit sigs", 10_000, 0},
		{"100k commit sigs", 100_000, 0},
		{"1m commit sigs", 1_000_000, 0},
		{"10k ext commit sigs", 0, 10_000},
		{"100k ext commit sigs", 0, 100_000},
		{"1m ext commit sigs", 0, 1_000_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := blockResponseBytes(t, tc.nSigs, tc.nExtSigs)
			allocs := testing.AllocsPerRun(20, func() {
				var stub bcproto.SigCountMessage
				require.NoError(t, stub.Unmarshal(payload))
				require.Len(t, stub.BlockResponse.Block.LastCommit.Signatures, tc.nSigs)
				require.Len(t, stub.BlockResponse.ExtCommit.ExtendedSignatures, tc.nExtSigs)
			})
			const maxAllocs = 50
			require.LessOrEqualf(t, int(allocs), maxAllocs, "unmarshal allocated %d times (max %d)", int(allocs), maxAllocs)
		})
	}
}

// protoFieldNum extracts the wire field number from a generated struct's
// protobuf tag, e.g. `protobuf:"bytes,3,opt,name=block_response"` -> 3.
func protoFieldNum(t *testing.T, msg interface{}, goField string) protowire.Number {
	t.Helper()
	f, ok := reflect.TypeOf(msg).FieldByName(goField)
	require.Truef(t, ok, "field %s not found on %T", goField, msg)
	parts := strings.Split(f.Tag.Get("protobuf"), ",")
	require.GreaterOrEqualf(t, len(parts), 2, "field %s on %T has no protobuf tag", goField, msg)
	num, err := strconv.Atoi(parts[1])
	require.NoError(t, err)
	return protowire.Number(num)
}

// blockResponseMsg wraps a BlockResponse in the blocksync Message oneof.
func blockResponseMsg(br *bcproto.BlockResponse) *bcproto.Message {
	return &bcproto.Message{Sum: &bcproto.Message_BlockResponse{BlockResponse: br}}
}

// blockResponseBytes encodes a blocksync Message carrying a BlockResponse with
// the given numbers of commit and extended-commit signatures.
func blockResponseBytes(t *testing.T, nSigs, nExtSigs int) []byte {
	t.Helper()
	return mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{
		Block:     &cmtproto.Block{LastCommit: commitWithSigs(nSigs)},
		ExtCommit: extCommitWithSigs(nExtSigs),
	}))
}

func commitWithSigs(n int) *cmtproto.Commit {
	sigs := make([]cmtproto.CommitSig, n)
	for i := range sigs {
		sigs[i] = cmtproto.CommitSig{BlockIdFlag: cmtproto.BlockIDFlagAbsent}
	}
	return &cmtproto.Commit{Signatures: sigs}
}

func extCommitWithSigs(n int) *cmtproto.ExtendedCommit {
	sigs := make([]cmtproto.ExtendedCommitSig, n)
	for i := range sigs {
		sigs[i] = cmtproto.ExtendedCommitSig{BlockIdFlag: cmtproto.BlockIDFlagAbsent}
	}
	return &cmtproto.ExtendedCommit{ExtendedSignatures: sigs}
}

func mustMarshal(t *testing.T, pb proto.Message) []byte {
	t.Helper()
	bz, err := proto.Marshal(pb)
	require.NoError(t, err)
	return bz
}

// wireField hand encodes payloads as consecutive fields with
// the given field number. Passing the same payload twice produces a duplicate field.
func wireField(num protowire.Number, payloads ...[]byte) []byte {
	var bz []byte
	for _, p := range payloads {
		bz = protowire.AppendTag(bz, num, protowire.BytesType)
		bz = protowire.AppendBytes(bz, p)
	}
	return bz
}
