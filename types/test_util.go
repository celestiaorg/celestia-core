package types

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/version"
	"github.com/stretchr/testify/require"
)

func MakeExtCommit(blockID BlockID, height int64, round int32,
	voteSet *VoteSet, validators []PrivValidator, now time.Time, extEnabled bool) (*ExtendedCommit, error) {

	// all sign
	for i := 0; i < len(validators); i++ {
		pubKey, err := validators[i].GetPubKey()
		if err != nil {
			return nil, fmt.Errorf("can't get pubkey: %w", err)
		}
		vote := &Vote{
			ValidatorAddress: pubKey.Address(),
			ValidatorIndex:   int32(i),
			Height:           height,
			Round:            round,
			Type:             cmtproto.PrecommitType,
			BlockID:          blockID,
			Timestamp:        now,
		}

		_, err = signAddVote(validators[i], vote, voteSet)
		if err != nil {
			return nil, err
		}
	}

	var enableHeight int64
	if extEnabled {
		enableHeight = height
	}

	return voteSet.MakeExtendedCommit(ABCIParams{VoteExtensionsEnableHeight: enableHeight}), nil
}

func signAddVote(privVal PrivValidator, vote *Vote, voteSet *VoteSet) (bool, error) {
	if vote.Type != voteSet.signedMsgType {
		return false, fmt.Errorf("vote and voteset are of different types; %d != %d", vote.Type, voteSet.signedMsgType)
	}
	if _, err := SignAndCheckVote(vote, privVal, voteSet.ChainID(), voteSet.extensionsEnabled); err != nil {
		return false, err
	}
	return voteSet.AddVote(vote)
}

func MakeVote(
	val PrivValidator,
	chainID string,
	valIndex int32,
	height int64,
	round int32,
	step cmtproto.SignedMsgType,
	blockID BlockID,
	time time.Time,
) (*Vote, error) {
	pubKey, err := val.GetPubKey()
	if err != nil {
		return nil, err
	}

	vote := &Vote{
		ValidatorAddress: pubKey.Address(),
		ValidatorIndex:   valIndex,
		Height:           height,
		Round:            round,
		Type:             step,
		BlockID:          blockID,
		Timestamp:        time,
	}

	extensionsEnabled := step == cmtproto.PrecommitType
	if _, err := SignAndCheckVote(vote, val, chainID, extensionsEnabled); err != nil {
		return nil, err
	}

	return vote, nil
}

func MakeVoteNoError(
	t *testing.T,
	val PrivValidator,
	chainID string,
	valIndex int32,
	height int64,
	round int32,
	step cmtproto.SignedMsgType,
	blockID BlockID,
	time time.Time,
) *Vote {
	vote, err := MakeVote(val, chainID, valIndex, height, round, step, blockID, time)
	require.NoError(t, err)
	return vote
}

// MakeBlock returns a new block with an empty header, except what can be
// computed from itself.
// It populates the same set of fields validated by ValidateBasic.
func MakeBlock(height int64, data Data, lastCommit *Commit, evidence []Evidence) *Block {
	block := &Block{
		Header: Header{
			Version: cmtversion.Consensus{Block: version.BlockProtocol, App: 0},
			Height:  height,
		},
		Data:       data,
		Evidence:   EvidenceData{Evidence: evidence},
		LastCommit: lastCommit,
	}
	block.fillHeader()
	return block
}

// MakeTxs is a helper function to generate mock transactions by given the block height
// and the transaction numbers.
func MakeTxs(height int64, num int) (txs []Tx) {
	for i := 0; i < num; i++ {
		txs = append(txs, Tx([]byte{byte(height), byte(i)}))
	}
	return txs
}

func MakeTenTxs(height int64) (txs []Tx) {
	return MakeTxs(height, 10)
}

func MakeData(txs []Tx) Data {
	return Data{
		Txs: txs,
	}
}

func MakeRandProtoBlock(txs []int) *cmtproto.Block {
	bztxs := make([]Tx, len(txs))
	for i, tx := range txs {
		bztxs[i] = cmtrand.Bytes(tx)
	}
	h := cmtrand.Int63()
	c1 := RandCommit(time.Now())
	evidenceTime := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	evi := NewMockDuplicateVoteEvidence(h, evidenceTime, "block-test-chain")
	b1 := MakeBlock(h, makeData(bztxs), c1, []Evidence{evi})
	b1.ProposerAddress = cmtrand.Bytes(crypto.AddressSize)
	pb, err := b1.ToProto()
	if err != nil {
		panic(err)
	}
	return pb
}

func RandCommit(now time.Time) *Commit {
	lastID := MakeBlockIDRandom()
	h := int64(3)
	voteSet, _, vals := RandVoteSet(h-1, 1, cmtproto.PrecommitType, 10, 1)
	commit, err := MakeCommit(lastID, h-1, 1, voteSet, vals, now)
	if err != nil {
		panic(err)
	}
	return commit
}

func RandVoteSet(
	height int64,
	round int32,
	signedMsgType cmtproto.SignedMsgType,
	numValidators int,
	votingPower int64,
) (*VoteSet, *ValidatorSet, []PrivValidator) {
	valSet, privValidators := RandValidatorSet(numValidators, votingPower)
	return NewVoteSet("test_chain_id", height, round, signedMsgType, valSet), valSet, privValidators
}

func MakeBlockIDRandom() BlockID {
	var (
		blockHash   = make([]byte, tmhash.Size)
		partSetHash = make([]byte, tmhash.Size)
	)
	rand.Read(blockHash)   //nolint: errcheck // ignore errcheck for read
	rand.Read(partSetHash) //nolint: errcheck // ignore errcheck for read
	return BlockID{blockHash, PartSetHeader{123, partSetHash}}
}
