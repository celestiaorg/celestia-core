package blocksync

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	smmocks "github.com/cometbft/cometbft/state/mocks"
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

func TestValidateBlockSyncTxLimitActivation(t *testing.T) {
	for _, version := range []uint64{9, 10, 11} {
		for _, count := range []int{types.MaxBlockTxs, types.MaxBlockTxs + 1, 100_000, types.MaxBlockTxsV10 + 1} {
			payload := blockResponseWithTxs(t, count, nil)
			stub, err := scanBlockSyncBytes(payload)
			require.NoError(t, err)
			err = validateMaxBlockTxs(stub.BlockResponse, version)
			if limit := types.MaxBlockTxsForAppVersion(version); limit > 0 && count > limit {
				require.ErrorIs(t, err, errTooManyTxs)
			} else {
				require.NoError(t, err)
			}
		}
	}
}

func TestBlocksyncChannelTxLimitUsesCommittedVersion(t *testing.T) {
	for _, version := range []uint64{9, 10, 11} {
		stateStore := &smmocks.Store{}
		stateStore.On("Load").Return(sm.State{Version: cmtstate.Version{
			Consensus: cmtversion.Consensus{App: version},
		}}, nil).Once()
		blockExec := sm.NewBlockExecutor(stateStore, nil, nil, nil, nil, nil)
		channel := (&Reactor{blockExec: blockExec}).GetChannels()[0]

		count := types.MaxBlockTxs + 1
		if version == types.BlockTxSafetyLimitAppVersion {
			count = types.MaxBlockTxsV10 + 1
		}
		err := channel.RecvMessagePrecheck(blockResponseWithTxs(t, count, nil))
		if limit := types.MaxBlockTxsForAppVersion(version); limit > 0 && count > limit {
			require.ErrorIs(t, err, errTooManyTxs)
		} else {
			require.NoError(t, err, "the response must remain valid at this app version")
		}
		stateStore.AssertExpectations(t)
	}

	// Ordinary responses do not add a state-store read to the hot path.
	stateStore := &smmocks.Store{}
	blockExec := sm.NewBlockExecutor(stateStore, nil, nil, nil, nil, nil)
	channel := (&Reactor{blockExec: blockExec}).GetChannels()[0]
	require.NoError(t, channel.RecvMessagePrecheck(blockResponseWithTxs(t, types.MaxBlockTxs, nil)))
	stateStore.AssertNotCalled(t, "Load")
}

func TestValidateBlockSyncTxLimitDuplicateFields(t *testing.T) {
	half := types.MaxBlockTxs/2 + 1
	data := mustMarshal(t, &cmtproto.Data{Txs: make([][]byte, half)})
	dataNum := protoFieldNum(t, cmtproto.Block{}, "Data")
	blockNum := protoFieldNum(t, bcproto.BlockResponse{}, "Block")
	responseNum := protoFieldNum(t, bcproto.Message_BlockResponse{}, "BlockResponse")

	for _, payload := range [][]byte{
		wireField(responseNum, wireField(blockNum, wireField(dataNum, data, data))),
		wireField(responseNum, wireField(blockNum, wireField(dataNum, data), wireField(dataNum, data))),
		wireField(responseNum, wireField(blockNum, wireField(dataNum, data)), wireField(blockNum, wireField(dataNum, data))),
	} {
		stub, err := scanBlockSyncBytes(payload)
		require.NoError(t, err)
		require.ErrorIs(t, validateMaxBlockTxs(stub.BlockResponse, types.BlockTxLimitAppVersion), errTooManyTxs)
	}
}

func TestValidateBlockSyncEvidenceCountBeforeAllocation(t *testing.T) {
	evidence := bytes.Repeat([]byte{0x0a, 0}, maxEvidenceSigs+1)
	wire := wireField(3, wireField(1, wireField(3, evidence)))
	allocs := testing.AllocsPerRun(5, func() {
		err := validateBlockSyncBytes(wire)
		if !errors.Is(err, errTooManyEvidenceSigs) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	require.Less(t, allocs, float64(20))
}

func TestValidateBlockSyncEvidenceCountSkipsUnknownGroups(t *testing.T) {
	group := protowire.AppendTag(nil, 100, protowire.StartGroupType)
	group = protowire.AppendTag(group, 1, protowire.VarintType)
	group = protowire.AppendVarint(group, 1)
	group = protowire.AppendTag(group, 100, protowire.EndGroupType)
	wire := append(blockResponseWithTxs(t, 0, nil), group...)
	require.NoError(t, validateBlockSyncBytes(wire))
}

func TestValidateBlockSyncCardinalityAcrossDuplicateBlocks(t *testing.T) {
	entries := bytes.Repeat([]byte{0x0a, 0}, cmtproto.MaxBlockEvidenceItems/2+1)
	block := wireField(3, entries)
	part := wireField(3, wireField(1, block))
	wire := append(append([]byte{}, part...), part...)
	require.ErrorIs(t, validateBlockSyncBytes(wire), errTooManyEvidenceSigs)
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
		{"Block.data", cmtproto.Block{}, "Data", bcproto.SigCountBlock{}, "Data"},
		{"Data.txs", cmtproto.Data{}, "Txs", bcproto.SigCountData{}, "Txs"},
		{"Block.last_commit", cmtproto.Block{}, "LastCommit", bcproto.SigCountBlock{}, "LastCommit"},
		{"Block.evidence", cmtproto.Block{}, "Evidence", bcproto.SigCountBlock{}, "Evidence"},
		{"EvidenceList.evidence", cmtproto.EvidenceList{}, "Evidence", bcproto.SigCountEvidenceList{}, "Evidence"},
		{"Evidence.light_client_attack_evidence", cmtproto.Evidence_LightClientAttackEvidence{}, "LightClientAttackEvidence", bcproto.SigCountEvidence{}, "LightClientAttackEvidence"},
		{"LightClientAttackEvidence.conflicting_block", cmtproto.LightClientAttackEvidence{}, "ConflictingBlock", bcproto.SigCountLightClientAttackEvidence{}, "ConflictingBlock"},
		{"LightClientAttackEvidence.byzantine_validators", cmtproto.LightClientAttackEvidence{}, "ByzantineValidators", bcproto.SigCountLightClientAttackEvidence{}, "ByzantineValidators"},
		{"LightBlock.signed_header", cmtproto.LightBlock{}, "SignedHeader", bcproto.SigCountLightBlock{}, "SignedHeader"},
		{"LightBlock.validator_set", cmtproto.LightBlock{}, "ValidatorSet", bcproto.SigCountLightBlock{}, "ValidatorSet"},
		{"SignedHeader.commit", cmtproto.SignedHeader{}, "Commit", bcproto.SigCountSignedHeader{}, "Commit"},
		{"ValidatorSet.validators", cmtproto.ValidatorSet{}, "Validators", bcproto.SigCountValidatorSet{}, "Validators"},
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
		{"1m transactions", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := blockResponseBytes(t, tc.nSigs, tc.nExtSigs)
			if tc.name == "1m transactions" {
				payload = blockResponseWithTxs(t, 1_000_000, nil)
			}
			allocs := testing.AllocsPerRun(20, func() {
				var stub bcproto.SigCountMessage
				require.NoError(t, stub.Unmarshal(payload))
				if tc.name == "1m transactions" {
					require.Len(t, stub.BlockResponse.Block.Data.Txs, 1_000_000)
				} else {
					require.Len(t, stub.BlockResponse.Block.LastCommit.Signatures, tc.nSigs)
					require.Len(t, stub.BlockResponse.ExtCommit.ExtendedSignatures, tc.nExtSigs)
				}
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

func blockResponseWithTxs(t *testing.T, count int, tx []byte) []byte {
	t.Helper()
	txs := make([][]byte, count)
	for i := range txs {
		txs[i] = tx
	}
	return mustMarshal(t, blockResponseMsg(&bcproto.BlockResponse{
		Block: &cmtproto.Block{Data: cmtproto.Data{Txs: txs}},
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
