package types

import (
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// protoSeeds builds valid wire bytes for the block-level types that peers
// send: a block with txs and a commit, its first part, a vote, a proposal,
// the commit on its own, and duplicate vote evidence.
func protoSeeds(tb testing.TB) (block, part, vote, proposal, commit, evidence []byte) {
	tb.Helper()
	marshal := func(msg proto.Message) []byte {
		raw, err := proto.Marshal(msg)
		require.NoError(tb, err)
		return raw
	}
	const chainID = "fuzz-chain"
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pv := NewMockPV()
	pubKey, err := pv.GetPubKey()
	require.NoError(tb, err)
	valSet := NewValidatorSet([]*Validator{NewValidator(pubKey, 10)})

	blockID := BlockID{Hash: make([]byte, 32), PartSetHeader: PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	voteSet := NewVoteSet(chainID, 9, 0, cmtproto.PrecommitType, valSet)
	extCommit, err := MakeExtCommit(blockID, 9, 0, voteSet, []PrivValidator{pv}, now, false)
	require.NoError(tb, err)
	lastCommit := extCommit.ToCommit()

	blk := MakeBlock(10, Data{Txs: MakeTxs(10, 3)}, lastCommit, nil)
	blk.ValidatorsHash = valSet.Hash()
	blk.NextValidatorsHash = valSet.Hash()
	blk.ProposerAddress = pubKey.Address()
	pbBlock, err := blk.ToProto()
	require.NoError(tb, err)
	partSet, err := blk.MakePartSet(BlockPartSizeBytes)
	require.NoError(tb, err)
	pbPart, err := partSet.GetPart(0).ToProto()
	require.NoError(tb, err)

	v, err := MakeVote(pv, chainID, 0, 10, 0, cmtproto.PrecommitType, blockID, now)
	require.NoError(tb, err)

	p := NewProposal(10, 0, -1, blockID)
	p.Timestamp = now
	pbProposal := p.ToProto()
	require.NoError(tb, pv.SignProposal(chainID, pbProposal))

	dve, err := NewMockDuplicateVoteEvidenceWithValidator(10, now, pv, chainID)
	require.NoError(tb, err)
	pbEvidence, err := EvidenceToProto(dve)
	require.NoError(tb, err)

	return marshal(pbBlock), marshal(pbPart), marshal(v.ToProto()), marshal(pbProposal), marshal(lastCommit.ToProto()), marshal(pbEvidence)
}

// proofRoot computes the root a proof commits to. ComputeRootHash is
// documented to panic on a proof whose index, total and aunts disagree, and
// ValidateBasic does not rule that out, so the panic is turned into !ok here.
func proofRoot(proof *merkle.Proof) (root []byte, ok bool) {
	defer func() {
		if recover() != nil {
			root, ok = nil, false
		}
	}()
	return proof.ComputeRootHash(), true
}

// FuzzBlockFromProto decodes peer-supplied block bytes the way blocksync and
// the propagation reactor do, then validates and re-encodes the result.
func FuzzBlockFromProto(f *testing.F) {
	block, _, _, _, _, _ := protoSeeds(f)
	f.Add(block)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.Block
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		blk, err := BlockFromProto(&pb)
		if err != nil {
			return
		}
		if err := blk.ValidateBasic(); err != nil {
			return
		}
		_ = blk.Hash()
		_, _ = blk.MakePartSet(BlockPartSizeBytes)
		again, err := blk.ToProto()
		require.NoError(t, err)
		back, err := BlockFromProto(again)
		require.NoError(t, err)
		require.Equal(t, blk.Hash(), back.Hash(), "block hash changed across a proto round trip")
	})
}

// FuzzPartFromProto decodes a block part and adds it to a part set whose
// header is taken from the part's own proof, which is what a receiving node
// does for parts of a proposal it has only the header for.
func FuzzPartFromProto(f *testing.F) {
	_, part, _, _, _, _ := protoSeeds(f)
	f.Add(part)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.Part
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		p, err := PartFromProto(&pb)
		if err != nil {
			return
		}
		root, ok := proofRoot(&p.Proof)
		if !ok || p.Proof.Total <= 0 || p.Proof.Total > int64(MaxBlockPartsCount) {
			return
		}
		ps := NewPartSetFromHeader(PartSetHeader{Total: uint32(p.Proof.Total), Hash: root}, BlockPartSizeBytes)
		added, err := ps.AddPart(p)
		if err == nil && added {
			require.True(t, ps.HasPart(int(p.Index)))
			_ = ps.GetPart(int(p.Index))
			_ = ps.BitArray()
		}
		again, err := p.ToProto()
		require.NoError(t, err)
		_, err = PartFromProto(again)
		require.NoError(t, err)
	})
}

// FuzzVoteFromProto decodes a vote the way the consensus reactor does.
func FuzzVoteFromProto(f *testing.F) {
	_, _, vote, _, _, _ := protoSeeds(f)
	f.Add(vote)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.Vote
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		v, err := VoteFromProto(&pb)
		if err != nil {
			return
		}
		if err := v.ValidateBasic(); err != nil {
			return
		}
		_ = v.String()
		_ = VoteSignBytes("fuzz-chain", v.ToProto())
		back, err := VoteFromProto(v.ToProto())
		require.NoError(t, err)
		require.Equal(t, v.ToProto(), back.ToProto())
	})
}

// FuzzProposalFromProto decodes a proposal the way the consensus and
// propagation reactors do.
func FuzzProposalFromProto(f *testing.F) {
	_, _, _, proposal, _, _ := protoSeeds(f)
	f.Add(proposal)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.Proposal
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		p, err := ProposalFromProto(&pb)
		if err != nil {
			return
		}
		if err := p.ValidateBasic(); err != nil {
			return
		}
		_ = p.String()
		_ = ProposalSignBytes("fuzz-chain", p.ToProto())
		back, err := ProposalFromProto(p.ToProto())
		require.NoError(t, err)
		require.Equal(t, p.ToProto(), back.ToProto())
	})
}

// FuzzCommitFromProto decodes a commit the way blocksync and the light client
// do and checks the vote-set helpers on it.
func FuzzCommitFromProto(f *testing.F) {
	_, _, _, _, commit, _ := protoSeeds(f)
	f.Add(commit)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.Commit
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		c, err := CommitFromProto(&pb)
		if err != nil {
			return
		}
		if err := c.ValidateBasic(); err != nil {
			return
		}
		_ = c.Hash()
		_ = c.Size()
		for i := range c.Signatures {
			_ = c.GetVote(int32(i))
		}
		back, err := CommitFromProto(c.ToProto())
		require.NoError(t, err)
		require.Equal(t, c.Hash(), back.Hash(), "commit hash changed across a proto round trip")
	})
}

// FuzzEvidenceFromProto decodes evidence the way the evidence reactor does.
func FuzzEvidenceFromProto(f *testing.F) {
	_, _, _, _, _, evidence := protoSeeds(f)
	f.Add(evidence)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var pb cmtproto.Evidence
		if err := proto.Unmarshal(raw, &pb); err != nil {
			return
		}
		ev, err := EvidenceFromProto(&pb)
		if err != nil {
			return
		}
		if err := ev.ValidateBasic(); err != nil {
			return
		}
		_ = ev.Hash()
		_ = ev.String()
		again, err := EvidenceToProto(ev)
		require.NoError(t, err)
		back, err := EvidenceFromProto(again)
		require.NoError(t, err)
		require.Equal(t, ev.Hash(), back.Hash(), "evidence hash changed across a proto round trip")
	})
}

// TestProtoFuzzSeeds keeps the seed corpus meaningful: every seed decodes and
// validates, so the fuzzer starts from accepted inputs.
func TestProtoFuzzSeeds(t *testing.T) {
	block, part, vote, proposal, commit, evidence := protoSeeds(t)

	var pbBlock cmtproto.Block
	require.NoError(t, proto.Unmarshal(block, &pbBlock))
	blk, err := BlockFromProto(&pbBlock)
	require.NoError(t, err)
	require.NoError(t, blk.ValidateBasic())

	var pbPart cmtproto.Part
	require.NoError(t, proto.Unmarshal(part, &pbPart))
	_, err = PartFromProto(&pbPart)
	require.NoError(t, err)

	var pbVote cmtproto.Vote
	require.NoError(t, proto.Unmarshal(vote, &pbVote))
	v, err := VoteFromProto(&pbVote)
	require.NoError(t, err)
	require.NoError(t, v.ValidateBasic())

	var pbProposal cmtproto.Proposal
	require.NoError(t, proto.Unmarshal(proposal, &pbProposal))
	p, err := ProposalFromProto(&pbProposal)
	require.NoError(t, err)
	require.NoError(t, p.ValidateBasic())

	var pbCommit cmtproto.Commit
	require.NoError(t, proto.Unmarshal(commit, &pbCommit))
	c, err := CommitFromProto(&pbCommit)
	require.NoError(t, err)
	require.NoError(t, c.ValidateBasic())

	var pbEvidence cmtproto.Evidence
	require.NoError(t, proto.Unmarshal(evidence, &pbEvidence))
	ev, err := EvidenceFromProto(&pbEvidence)
	require.NoError(t, err)
	require.NoError(t, ev.ValidateBasic())
}
