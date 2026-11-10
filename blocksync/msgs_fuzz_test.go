package blocksync

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/fuzzinput"
	"github.com/cometbft/cometbft/p2p"
	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// maxFuzzEdits bounds the field edits applied to one structured message.
const maxFuzzEdits = 16

// blockSyncEditSeeds are structured inputs: a fixture kind, an edit count,
// then per edit a field selector and its value.
var blockSyncEditSeeds = [][]byte{
	{0, 1, 0},                            // BlockRequest: height -1
	{4, 1, 1, 4, 11},                     // StatusResponse: base 11 above height 10
	{2, 1, 3, 0, 3, 'a', 'b', 'c'},       // BlockResponse: transaction appended
	{2, 1, 5, 1, 3, 1},                   // BlockResponse: last commit round 1
	{2, 1, 6, 1, 0x11, 0x27},             // BlockResponse: one commit signature over MaxVotesCount
	{2, 1, 6, 2, 0, 0, 3, 2},             // BlockResponse: absent signature flagged as commit
	{2, 1, 7, 0, 2},                      // BlockResponse: light client attack with two commit signatures
	{2, 2, 7, 0, 1, 7, 1, 0, 0x11, 0x27}, // BlockResponse: evidence commit one signature over MaxVotesCount
	{2, 1, 7, 2},                         // BlockResponse: duplicate vote evidence
	{2, 1, 8, 1, 0x11, 0x27},             // BlockResponse: one extended signature over MaxVotesCount
	{2, 2, 0, 1, 9, 0},                   // BlockResponse: block height 0, extended commit cleared
	{2, 1, 1, 1, 3, 'f', 'o', 'o'},       // BlockResponse: chain ID changed
	{2, 2, 4, 1, 0, 4, 8, 1, 0, 0},       // BlockResponse: data hash cleared, proposer changed
	{2, 1, 9, 1},                         // BlockResponse: nil block
	{2, 1, 8, 2, 0, 3, 3, 1, 'e'},        // BlockResponse: extension on an absent extended signature
}

// blockSyncFixtures returns one valid wire message of each blocksync type.
func blockSyncFixtures(tb testing.TB) [][]byte {
	tb.Helper()
	commit := &cmttypes.Commit{
		Height: 1,
		BlockID: cmttypes.BlockID{
			Hash:          bytes.Repeat([]byte{1}, 32),
			PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{2}, 32)},
		},
		Signatures: []cmttypes.CommitSig{cmttypes.NewCommitSigAbsent()},
	}
	block := cmttypes.MakeBlock(2, cmttypes.Data{Txs: cmttypes.Txs{cmttypes.Tx("key=value")}}, commit, nil)
	block.ValidatorsHash = bytes.Repeat([]byte{1}, 32)
	block.NextValidatorsHash = bytes.Repeat([]byte{2}, 32)
	block.ProposerAddress = bytes.Repeat([]byte{3}, 20)
	require.NoError(tb, block.ValidateBasic())
	pbBlock, err := block.ToProto()
	require.NoError(tb, err)
	seeds := []p2p.Wrapper{
		&bcproto.BlockRequest{Height: 1},
		&bcproto.NoBlockResponse{Height: 1},
		&bcproto.BlockResponse{Block: pbBlock, ExtCommit: commit.WrappedExtendedCommit().ToProto()},
		&bcproto.StatusRequest{},
		&bcproto.StatusResponse{Height: 10, Base: 1},
	}
	fixtures := make([][]byte, 0, len(seeds))
	for _, seed := range seeds {
		raw, err := proto.Marshal(seed.Wrap())
		require.NoError(tb, err)
		require.NoError(tb, validateBlockSyncBytes(raw))
		require.NoError(tb, ValidateMsg(seed.(proto.Message)))
		var decoded bcproto.Message
		require.NoError(tb, proto.Unmarshal(raw, &decoded))
		inner, err := decoded.Unwrap()
		require.NoError(tb, err)
		if response, ok := inner.(*bcproto.BlockResponse); ok {
			_, err = cmttypes.BlockFromProto(response.Block)
			require.NoError(tb, err)
			_, err = cmttypes.ExtendedCommitFromProto(response.ExtCommit)
			require.NoError(tb, err)
		}
		fixtures = append(fixtures, raw)
	}
	return fixtures
}

// checkBlockSyncMessage runs the signature-count precheck, unwrap and
// ValidateMsg the reactor applies to peer bytes. It returns the accepted
// message, or nil, after checking that it survives a proto round trip.
func checkBlockSyncMessage(t *testing.T, raw []byte) proto.Message {
	t.Helper()
	if err := validateBlockSyncBytes(raw); err != nil {
		return nil
	}
	var envelope bcproto.Message
	if err := proto.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	msg, err := envelope.Unwrap()
	if err != nil {
		return nil
	}
	if err := ValidateMsg(msg); err != nil {
		return nil
	}
	if response, ok := msg.(*bcproto.BlockResponse); ok {
		// ValidateMsg deliberately leaves block conversion to the reactor.
		_, _ = cmttypes.BlockFromProto(response.Block)
		if response.ExtCommit != nil {
			_, _ = cmttypes.ExtendedCommitFromProto(response.ExtCommit)
		}
	}
	encoded, err := proto.Marshal(msg.(p2p.Wrapper).Wrap())
	require.NoError(t, err)
	require.NoError(t, validateBlockSyncBytes(encoded))
	var decoded bcproto.Message
	require.NoError(t, proto.Unmarshal(encoded, &decoded))
	back, err := decoded.Unwrap()
	require.NoError(t, err)
	require.NoError(t, ValidateMsg(back))
	require.True(t, proto.Equal(msg, back), "message changed across proto round trip")
	return msg
}

// FuzzBlockSyncMessage includes the signature-count precheck that runs on raw
// peer bytes before the full decoder, then follows the reactor's unwrap path.
//
// With empty edits, raw is fed to the decoder as wire bytes. Otherwise edits
// selects a valid message, applies field-level edits to its proto form and
// sends the re-encoded envelope down the same path, so that the precheck
// bounds and block conversion are reached far more often.
func FuzzBlockSyncMessage(f *testing.F) {
	fixtures := blockSyncFixtures(f)
	for _, raw := range fixtures {
		f.Add(raw, []byte(nil))
	}
	f.Add([]byte{}, []byte(nil))
	f.Add([]byte{0xff}, []byte(nil))
	// A BlockResponse with absent nested pointers must also reach conversion.
	raw, err := proto.Marshal((&bcproto.BlockResponse{}).Wrap())
	require.NoError(f, err)
	f.Add(raw, []byte(nil))
	for kind := range fixtures {
		f.Add([]byte(nil), []byte{byte(kind), 0})
	}
	for _, edits := range blockSyncEditSeeds {
		f.Add([]byte(nil), edits)
	}
	f.Fuzz(func(t *testing.T, raw []byte, edits []byte) {
		if len(edits) == 0 {
			// Bound per-worker memory and time; this is smaller than the network's
			// block-size ceiling and is not intended to test maximum-size blocks.
			if len(raw) > 1<<20 {
				return
			}
			checkBlockSyncMessage(t, raw)
			return
		}
		r := fuzzinput.New(edits)
		var envelope bcproto.Message
		require.NoError(t, proto.Unmarshal(fixtures[r.Intn(len(fixtures))], &envelope))
		n := r.Intn(maxFuzzEdits + 1)
		for i := 0; i < n; i++ {
			editBlockSyncMessage(r, &envelope)
		}
		wire, err := proto.Marshal(&envelope)
		require.NoError(t, err)
		msg := checkBlockSyncMessage(t, wire)
		if n > 0 {
			return
		}
		require.NotNil(t, msg, "unedited fixture rejected")
		if response, ok := msg.(*bcproto.BlockResponse); ok {
			_, err = cmttypes.BlockFromProto(response.Block)
			require.NoError(t, err)
			_, err = cmttypes.ExtendedCommitFromProto(response.ExtCommit)
			require.NoError(t, err)
		}
	})
}

// editBlockSyncMessage applies one field edit to the message in envelope.
func editBlockSyncMessage(r *fuzzinput.Reader, envelope *bcproto.Message) {
	switch m := envelope.Sum.(type) {
	case *bcproto.Message_BlockRequest:
		m.BlockRequest.Height = pickInt64(r)
	case *bcproto.Message_NoBlockResponse:
		m.NoBlockResponse.Height = pickInt64(r)
	case *bcproto.Message_StatusResponse:
		if r.Bool() {
			m.StatusResponse.Base = pickInt64(r)
		} else {
			m.StatusResponse.Height = pickInt64(r)
		}
	case *bcproto.Message_BlockResponse:
		editBlockResponse(r, m.BlockResponse)
	}
}

func editBlockResponse(r *fuzzinput.Reader, br *bcproto.BlockResponse) {
	field := r.Intn(10)
	switch field {
	case 8:
		if br.ExtCommit == nil {
			br.ExtCommit = &cmtproto.ExtendedCommit{}
		}
		editExtendedCommit(r, br.ExtCommit)
		return
	case 9:
		if r.Bool() {
			br.Block = nil
		} else {
			br.ExtCommit = nil
		}
		return
	}
	if br.Block == nil {
		br.Block = &cmtproto.Block{}
	}
	b := br.Block
	h := &b.Header
	switch field {
	case 0:
		h.Height = pickInt64(r)
	case 1:
		if r.Bool() {
			h.ChainID = string(r.Bytes(64))
		} else {
			h.Time = pickTime(r)
		}
	case 2:
		if r.Bool() {
			h.Version.Block = r.Uint64()
		} else {
			editBlockID(r, &h.LastBlockId)
		}
	case 3:
		// Transactions: append, drop, or change one.
		switch r.Intn(3) {
		case 0:
			b.Data.Txs = append(b.Data.Txs, r.Bytes(128))
		case 1:
			if len(b.Data.Txs) > 0 {
				b.Data.Txs = b.Data.Txs[:len(b.Data.Txs)-1]
			}
		case 2:
			if len(b.Data.Txs) > 0 {
				i := r.Intn(len(b.Data.Txs))
				b.Data.Txs[i] = editBytes(r, b.Data.Txs[i])
			}
		}
	case 4:
		hashes := []*[]byte{
			&h.LastCommitHash, &h.DataHash, &h.ValidatorsHash, &h.NextValidatorsHash,
			&h.ConsensusHash, &h.AppHash, &h.LastResultsHash, &h.EvidenceHash, &h.ProposerAddress,
		}
		p := hashes[r.Intn(len(hashes))]
		*p = editBytes(r, *p)
	case 5:
		if b.LastCommit == nil {
			b.LastCommit = &cmtproto.Commit{}
		}
		editCommit(r, b.LastCommit)
	case 6:
		if b.LastCommit == nil {
			b.LastCommit = &cmtproto.Commit{}
		}
		b.LastCommit.Signatures = editCommitSigs(r, b.LastCommit.Signatures)
	case 7:
		editEvidence(r, &b.Evidence)
	}
}

func editCommit(r *fuzzinput.Reader, c *cmtproto.Commit) {
	switch r.Intn(3) {
	case 0:
		c.Height = pickInt64(r)
	case 1:
		c.Round = pickInt32(r)
	case 2:
		editBlockID(r, &c.BlockID)
	}
}

// editCommitSigs resizes the list to at most one past MaxVotesCount, or edits
// one signature.
func editCommitSigs(r *fuzzinput.Reader, sigs []cmtproto.CommitSig) []cmtproto.CommitSig {
	switch r.Intn(3) {
	case 0:
		return append(sigs, cmtproto.CommitSig{BlockIdFlag: cmtproto.BlockIDFlagAbsent})
	case 1:
		n := int(r.Uint16()) % (cmttypes.MaxVotesCount + 2)
		out := make([]cmtproto.CommitSig, n)
		for i := range out {
			out[i].BlockIdFlag = cmtproto.BlockIDFlagAbsent
		}
		return out
	}
	if len(sigs) == 0 {
		return sigs
	}
	s := &sigs[r.Intn(len(sigs))]
	switch r.Intn(4) {
	case 0:
		s.BlockIdFlag = cmtproto.BlockIDFlag(pickInt32(r))
	case 1:
		s.ValidatorAddress = editBytes(r, s.ValidatorAddress)
	case 2:
		s.Signature = editBytes(r, s.Signature)
	case 3:
		s.Timestamp = pickTime(r)
	}
	return sigs
}

func editExtendedCommit(r *fuzzinput.Reader, ec *cmtproto.ExtendedCommit) {
	switch r.Intn(3) {
	case 0:
		c := cmtproto.Commit{Height: ec.Height, Round: ec.Round, BlockID: ec.BlockID}
		editCommit(r, &c)
		ec.Height, ec.Round, ec.BlockID = c.Height, c.Round, c.BlockID
	case 1:
		n := int(r.Uint16()) % (cmttypes.MaxVotesCount + 2)
		ec.ExtendedSignatures = make([]cmtproto.ExtendedCommitSig, n)
		for i := range ec.ExtendedSignatures {
			ec.ExtendedSignatures[i].BlockIdFlag = cmtproto.BlockIDFlagAbsent
		}
	case 2:
		if len(ec.ExtendedSignatures) == 0 {
			return
		}
		s := &ec.ExtendedSignatures[r.Intn(len(ec.ExtendedSignatures))]
		switch r.Intn(5) {
		case 0:
			s.BlockIdFlag = cmtproto.BlockIDFlag(pickInt32(r))
		case 1:
			s.ValidatorAddress = editBytes(r, s.ValidatorAddress)
		case 2:
			s.Signature = editBytes(r, s.Signature)
		case 3:
			s.Extension = editBytes(r, s.Extension)
		case 4:
			s.ExtensionSignature = editBytes(r, s.ExtensionSignature)
		}
	}
}

// editEvidence appends light client attack evidence carrying a commit of a
// chosen size, resizes the last attack's nested lists, or appends duplicate
// vote evidence. Every list stays within one of MaxVotesCount.
func editEvidence(r *fuzzinput.Reader, el *cmtproto.EvidenceList) {
	switch r.Intn(3) {
	case 0:
		n := r.Intn(4)
		lcae := &cmtproto.LightClientAttackEvidence{
			ConflictingBlock: &cmtproto.LightBlock{SignedHeader: &cmtproto.SignedHeader{
				Header: &cmtproto.Header{Height: 1},
				Commit: &cmtproto.Commit{Height: 1, Signatures: make([]cmtproto.CommitSig, n)},
			}},
			CommonHeight: 1,
		}
		el.Evidence = append(el.Evidence, cmtproto.Evidence{
			Sum: &cmtproto.Evidence_LightClientAttackEvidence{LightClientAttackEvidence: lcae},
		})
	case 1:
		var lcae *cmtproto.LightClientAttackEvidence
		for i := range el.Evidence {
			if e, ok := el.Evidence[i].Sum.(*cmtproto.Evidence_LightClientAttackEvidence); ok {
				lcae = e.LightClientAttackEvidence
			}
		}
		if lcae == nil || lcae.ConflictingBlock == nil {
			return
		}
		which := r.Intn(3)
		n := int(r.Uint16()) % (cmttypes.MaxVotesCount + 2)
		switch which {
		case 0:
			if sh := lcae.ConflictingBlock.SignedHeader; sh != nil && sh.Commit != nil {
				sh.Commit.Signatures = make([]cmtproto.CommitSig, n)
			}
		case 1:
			lcae.ConflictingBlock.ValidatorSet = &cmtproto.ValidatorSet{Validators: make([]*cmtproto.Validator, n)}
			for i := range lcae.ConflictingBlock.ValidatorSet.Validators {
				lcae.ConflictingBlock.ValidatorSet.Validators[i] = &cmtproto.Validator{}
			}
		case 2:
			lcae.ByzantineValidators = make([]*cmtproto.Validator, n)
			for i := range lcae.ByzantineValidators {
				lcae.ByzantineValidators[i] = &cmtproto.Validator{}
			}
		}
	case 2:
		vote := &cmtproto.Vote{
			Type: cmtproto.PrevoteType, Height: 1, ValidatorAddress: bytes.Repeat([]byte{3}, 20),
			Signature: bytes.Repeat([]byte{4}, 64),
		}
		el.Evidence = append(el.Evidence, cmtproto.Evidence{
			Sum: &cmtproto.Evidence_DuplicateVoteEvidence{DuplicateVoteEvidence: &cmtproto.DuplicateVoteEvidence{
				VoteA: vote, VoteB: vote, TotalVotingPower: 1, ValidatorPower: 1,
			}},
		})
	}
}

// pickInt64 returns a boundary value or one read from r.
func pickInt64(r *fuzzinput.Reader) int64 {
	switch r.Intn(6) {
	case 0:
		return -1
	case 1:
		return 0
	case 2:
		return math.MaxInt64
	case 3:
		return math.MinInt64
	case 4:
		return int64(r.Byte())
	default:
		return r.Int64()
	}
}

// pickInt32 returns a boundary value or one read from r.
func pickInt32(r *fuzzinput.Reader) int32 {
	switch r.Intn(5) {
	case 0:
		return -1
	case 1:
		return math.MaxInt32
	case 2:
		return math.MinInt32
	case 3:
		return int32(r.Byte())
	default:
		return r.Int32()
	}
}

// pickTime returns a timestamp within the range protobuf can encode.
func pickTime(r *fuzzinput.Reader) time.Time {
	return time.Unix(int64(r.Int32())*16, int64(r.Uint32()%1e9)).UTC()
}

// editBytes returns cur with one byte flipped, truncated by one, cleared, or
// replaced by up to 128 bytes from r.
func editBytes(r *fuzzinput.Reader, cur []byte) []byte {
	switch r.Intn(4) {
	case 0:
		return nil
	case 1:
		if len(cur) == 0 {
			return nil
		}
		out := append([]byte(nil), cur...)
		out[r.Intn(len(out))] ^= 1 | r.Byte()
		return out
	case 2:
		if len(cur) == 0 {
			return nil
		}
		return append([]byte(nil), cur[:len(cur)-1]...)
	default:
		return r.Bytes(128)
	}
}

func editBlockID(r *fuzzinput.Reader, id *cmtproto.BlockID) {
	switch r.Intn(4) {
	case 0:
		id.PartSetHeader.Total = r.Uint32()
	case 1:
		id.PartSetHeader.Hash = editBytes(r, id.PartSetHeader.Hash)
	case 2:
		id.Hash = editBytes(r, id.Hash)
	case 3:
		*id = cmtproto.BlockID{}
	}
}
