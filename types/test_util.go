package types

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/tmhash"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

func MakeCommit(blockID BlockID, height int64, round int32,
	voteSet *VoteSet, validators []PrivValidator, now time.Time) (*Commit, error) {

	// all sign
	for i := 0; i < len(validators); i++ {
		pubKey, err := validators[i].GetPubKey()
		if err != nil {
			return nil, fmt.Errorf("can't get pubkey: %w", err)
		}
		vote := &Vote{
			ValidatorAddress: pubKey.Address(),
			//nolint:gosec
			ValidatorIndex: int32(i),
			Height:         height,
			Round:          round,
			Type:           cmtproto.PrecommitType,
			BlockID:        blockID,
			Timestamp:      now,
		}

		_, err = signAddVote(validators[i], vote, voteSet)
		if err != nil {
			return nil, err
		}
	}

	return voteSet.MakeCommit(), nil
}

func signAddVote(privVal PrivValidator, vote *Vote, voteSet *VoteSet) (signed bool, err error) {
	v := vote.ToProto()
	err = privVal.SignVote(voteSet.ChainID(), v)
	if err != nil {
		return false, err
	}
	vote.Signature = v.Signature
	return voteSet.AddVote(vote)
}

func MakeVote(
	height int64,
	blockID BlockID,
	valSet *ValidatorSet,
	privVal PrivValidator,
	chainID string,
	now time.Time,
) (*Vote, error) {
	pubKey, err := privVal.GetPubKey()
	if err != nil {
		return nil, fmt.Errorf("can't get pubkey: %w", err)
	}
	addr := pubKey.Address()
	idx, _ := valSet.GetByAddress(addr)
	vote := &Vote{
		ValidatorAddress: addr,
		ValidatorIndex:   idx,
		Height:           height,
		Round:            0,
		Timestamp:        now,
		Type:             cmtproto.PrecommitType,
		BlockID:          blockID,
	}
	v := vote.ToProto()

	if err := privVal.SignVote(chainID, v); err != nil {
		return nil, err
	}

	vote.Signature = v.Signature

	return vote, nil
}

func makeData(txs []Tx) Data {
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
