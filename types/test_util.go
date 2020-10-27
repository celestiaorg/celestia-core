package types

import (
	"fmt"
	"time"

	tmbytes "github.com/lazyledger/lazyledger-core/libs/bytes"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	tmversion "github.com/lazyledger/lazyledger-core/proto/tendermint/version"
	"github.com/lazyledger/lazyledger-core/version"
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
			ValidatorIndex:   int32(i),
			Height:           height,
			Round:            round,
			Type:             tmproto.PrecommitType,
			BlockID:          blockID,
			Timestamp:        now,
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
		Type:             tmproto.PrecommitType,
		BlockID:          blockID,
	}
	v := vote.ToProto()

	if err := privVal.SignVote(chainID, v); err != nil {
		return nil, err
	}

	vote.Signature = v.Signature

	return vote, nil
}

// MakeBlock returns a new block with an empty header, except what can be
// computed from itself.
// It populates the same set of fields validated by ValidateBasic.
// TODO(ismail): tell the IG team that this method isn't only used in tests
// hence, test_util.go is quite misleading.
func MakeBlock(
	height int64,
	txs []Tx, evidence []Evidence, intermediateStateRoots []tmbytes.HexBytes, messages []Message,
	lastCommit *Commit) *Block {
	block := &Block{
		Header: Header{
			Version: tmversion.Consensus{Block: version.BlockProtocol, App: 0},
			Height:  height,
		},
		Data: Data{
			Txs:                    txs,
			IntermediateStateRoots: IntermediateStateRoots{RawRootsList: intermediateStateRoots},
			Evidence:               EvidenceData{Evidence: evidence},
			Messages:               Messages{MessagesList: messages},
		},
		LastCommit: lastCommit,
	}
	block.fillHeader()
	return block
}
