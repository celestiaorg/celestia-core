package consensus

import (
	"errors"
	"fmt"

	"github.com/cosmos/gogoproto/proto"

	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/libs/bits"
	cmtmath "github.com/cometbft/cometbft/libs/math"
	"github.com/cometbft/cometbft/p2p"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// MsgToProto takes a consensus message type and returns the proto defined consensus message.
//
// TODO: This needs to be removed, but WALToProto depends on this.
func MsgToProto(msg Message) (proto.Message, error) {
	if msg == nil {
		return nil, errors.New("consensus: message is nil")
	}
	var pb proto.Message

	switch msg := msg.(type) {
	case *NewRoundStepMessage:
		pb = &cmtcons.NewRoundStep{
			Height:                msg.Height,
			Round:                 msg.Round,
			Step:                  uint32(msg.Step),
			SecondsSinceStartTime: msg.SecondsSinceStartTime,
			LastCommitRound:       msg.LastCommitRound,
		}

	case *NewValidBlockMessage:
		pbPartSetHeader := msg.BlockPartSetHeader.ToProto()
		pbBits := msg.BlockParts.ToProto()
		pb = &cmtcons.NewValidBlock{
			Height:             msg.Height,
			Round:              msg.Round,
			BlockPartSetHeader: pbPartSetHeader,
			BlockParts:         pbBits,
			IsCommit:           msg.IsCommit,
		}

	case *ProposalMessage:
		pbP := msg.Proposal.ToProto()
		pb = &cmtcons.Proposal{
			Proposal: *pbP,
		}

	case *ProposalPOLMessage:
		pbBits := msg.ProposalPOL.ToProto()
		pb = &cmtcons.ProposalPOL{
			Height:           msg.Height,
			ProposalPolRound: msg.ProposalPOLRound,
			ProposalPol:      *pbBits,
		}

	case *BlockPartMessage:
		parts, err := msg.Part.ToProto()
		if err != nil {
			return nil, fmt.Errorf("msg to proto error: %w", err)
		}
		pb = &cmtcons.BlockPart{
			Height: msg.Height,
			Round:  msg.Round,
			Part:   *parts,
		}

	case *VoteMessage:
		vote := msg.Vote.ToProto()
		pb = &cmtcons.Vote{
			Vote: vote,
		}

	case *HasVoteMessage:
		pb = &cmtcons.HasVote{
			Height: msg.Height,
			Round:  msg.Round,
			Type:   msg.Type,
			Index:  msg.Index,
		}

	case *VoteSetMaj23Message:
		bi := msg.BlockID.ToProto()
		pb = &cmtcons.VoteSetMaj23{
			Height:  msg.Height,
			Round:   msg.Round,
			Type:    msg.Type,
			BlockID: bi,
		}

	case *VoteSetBitsMessage:
		bi := msg.BlockID.ToProto()
		bits := msg.Votes.ToProto()

		vsb := &cmtcons.VoteSetBits{
			Height:  msg.Height,
			Round:   msg.Round,
			Type:    msg.Type,
			BlockID: bi,
		}

		if bits != nil {
			vsb.Votes = *bits
		}

		pb = vsb

	default:
		return nil, fmt.Errorf("consensus: message not recognized: %T", msg)
	}

	return pb, nil
}

// MsgFromProto takes a consensus proto message and returns the native go type
func MsgFromProto(p proto.Message) (Message, error) {
	if p == nil {
		return nil, errors.New("consensus: nil message")
	}
	var pb Message

	switch msg := p.(type) {
	case *cmtcons.NewRoundStep:
		rs, err := cmtmath.SafeConvertUint8(int64(msg.Step))
		// deny message based on possible overflow
		if err != nil {
			return nil, fmt.Errorf("denying message due to possible overflow: %w", err)
		}
		pb = &NewRoundStepMessage{
			Height:                msg.Height,
			Round:                 msg.Round,
			Step:                  cstypes.RoundStepType(rs),
			SecondsSinceStartTime: msg.SecondsSinceStartTime,
			LastCommitRound:       msg.LastCommitRound,
		}
	case *cmtcons.NewValidBlock:
		pbPartSetHeader, err := types.PartSetHeaderFromProto(&msg.BlockPartSetHeader)
		if err != nil {
			return nil, fmt.Errorf("parts to proto error: %w", err)
		}

		pbBits := new(bits.BitArray)
		pbBits.FromProto(msg.BlockParts)

		pb = &NewValidBlockMessage{
			Height:             msg.Height,
			Round:              msg.Round,
			BlockPartSetHeader: *pbPartSetHeader,
			BlockParts:         pbBits,
			IsCommit:           msg.IsCommit,
		}
	case *cmtcons.Proposal:
		pbP, err := types.ProposalFromProto(&msg.Proposal)
		if err != nil {
			return nil, fmt.Errorf("proposal msg to proto error: %w", err)
		}

		pb = &ProposalMessage{
			Proposal: pbP,
		}
	case *cmtcons.ProposalPOL:
		pbBits := new(bits.BitArray)
		pbBits.FromProto(&msg.ProposalPol)
		pb = &ProposalPOLMessage{
			Height:           msg.Height,
			ProposalPOLRound: msg.ProposalPolRound,
			ProposalPOL:      pbBits,
		}
	case *cmtcons.BlockPart:
		parts, err := types.PartFromProto(&msg.Part)
		if err != nil {
			return nil, fmt.Errorf("blockpart msg to proto error: %w", err)
		}
		pb = &BlockPartMessage{
			Height: msg.Height,
			Round:  msg.Round,
			Part:   parts,
		}
	case *cmtcons.Vote:
		// Vote validation will be handled in the vote message ValidateBasic
		// call below.
		vote, err := types.VoteFromProto(msg.Vote)
		if err != nil {
			return nil, fmt.Errorf("vote msg to proto error: %w", err)
		}

		pb = &VoteMessage{
			Vote: vote,
		}
	case *cmtcons.HasVote:
		pb = &HasVoteMessage{
			Height: msg.Height,
			Round:  msg.Round,
			Type:   msg.Type,
			Index:  msg.Index,
		}
	case *cmtcons.VoteSetMaj23:
		bi, err := types.BlockIDFromProto(&msg.BlockID)
		if err != nil {
			return nil, fmt.Errorf("voteSetMaj23 msg to proto error: %w", err)
		}
		pb = &VoteSetMaj23Message{
			Height:  msg.Height,
			Round:   msg.Round,
			Type:    msg.Type,
			BlockID: *bi,
		}
	case *cmtcons.VoteSetBits:
		bi, err := types.BlockIDFromProto(&msg.BlockID)
		if err != nil {
			return nil, fmt.Errorf("voteSetBits msg to proto error: %w", err)
		}
		bits := new(bits.BitArray)
		bits.FromProto(&msg.Votes)

		pb = &VoteSetBitsMessage{
			Height:  msg.Height,
			Round:   msg.Round,
			Type:    msg.Type,
			BlockID: *bi,
			Votes:   bits,
		}
	default:
		return nil, fmt.Errorf("consensus: message not recognized: %T", msg)
	}

	if err := pb.ValidateBasic(); err != nil {
		return nil, err
	}

	return pb, nil
}

// WALToProto takes a WAL message and return a proto walMessage and error
func WALToProto(msg WALMessage) (*cmtcons.WALMessage, error) {
	var pb cmtcons.WALMessage

	switch msg := msg.(type) {
	case types.EventDataRoundState:
		pb = cmtcons.WALMessage{
			Sum: &cmtcons.WALMessage_EventDataRoundState{
				EventDataRoundState: &cmtproto.EventDataRoundState{
					Height: msg.Height,
					Round:  msg.Round,
					Step:   msg.Step,
				},
			},
		}
	case msgInfo:
		consMsg, err := MsgToProto(msg.Msg)
		if err != nil {
			return nil, err
		}
		if w, ok := consMsg.(p2p.Wrapper); ok {
			consMsg = w.Wrap()
		}
		cm := consMsg.(*cmtcons.Message)
		pb = cmtcons.WALMessage{
			Sum: &cmtcons.WALMessage_MsgInfo{
				MsgInfo: &cmtcons.MsgInfo{
					Msg:    *cm,
					PeerID: string(msg.PeerID),
				},
			},
		}
		if bp, ok := cm.Sum.(*cmtcons.Message_BlockPart); ok {
			if compact := compactBlockPart(bp.BlockPart, string(msg.PeerID)); compact != nil {
				pb = cmtcons.WALMessage{Sum: &cmtcons.WALMessage_BlockPart{BlockPart: compact}}
			}
		}
	case timeoutInfo:
		pb = cmtcons.WALMessage{
			Sum: &cmtcons.WALMessage_TimeoutInfo{
				TimeoutInfo: &cmtcons.TimeoutInfo{
					Duration: msg.Duration,
					Height:   msg.Height,
					Round:    msg.Round,
					Step:     uint32(msg.Step),
				},
			},
		}
	case EndHeightMessage:
		pb = cmtcons.WALMessage{
			Sum: &cmtcons.WALMessage_EndHeight{
				EndHeight: &cmtcons.EndHeight{
					Height: msg.Height,
				},
			},
		}
	default:
		return nil, fmt.Errorf("to proto: wal message not recognized: %T", msg)
	}

	return &pb, nil
}

// WALFromProto takes a proto wal message and return a consensus walMessage and error
func WALFromProto(msg *cmtcons.WALMessage) (WALMessage, error) {
	if msg == nil {
		return nil, errors.New("nil WAL message")
	}
	var pb WALMessage

	switch msg := msg.Sum.(type) {
	case *cmtcons.WALMessage_EventDataRoundState:
		pb = types.EventDataRoundState{
			Height: msg.EventDataRoundState.Height,
			Round:  msg.EventDataRoundState.Round,
			Step:   msg.EventDataRoundState.Step,
		}
	case *cmtcons.WALMessage_MsgInfo:
		um, err := msg.MsgInfo.Msg.Unwrap()
		if err != nil {
			return nil, fmt.Errorf("unwrap message: %w", err)
		}
		walMsg, err := MsgFromProto(um)
		if err != nil {
			return nil, fmt.Errorf("msgInfo from proto error: %w", err)
		}
		pb = msgInfo{
			Msg:    walMsg,
			PeerID: p2p.ID(msg.MsgInfo.PeerID),
		}
	case *cmtcons.WALMessage_BlockPart:
		if err := expandBlockPart(msg.BlockPart); err != nil {
			return nil, fmt.Errorf("block part from proto error: %w", err)
		}
		walMsg, err := MsgFromProto(&msg.BlockPart.BlockPart)
		if err != nil {
			return nil, fmt.Errorf("block part from proto error: %w", err)
		}
		pb = msgInfo{
			Msg:    walMsg,
			PeerID: p2p.ID(msg.BlockPart.PeerID),
		}

	case *cmtcons.WALMessage_TimeoutInfo:
		tis, err := cmtmath.SafeConvertUint8(int64(msg.TimeoutInfo.Step))
		// deny message based on possible overflow
		if err != nil {
			return nil, fmt.Errorf("denying message due to possible overflow: %w", err)
		}
		pb = timeoutInfo{
			Duration: msg.TimeoutInfo.Duration,
			Height:   msg.TimeoutInfo.Height,
			Round:    msg.TimeoutInfo.Round,
			Step:     cstypes.RoundStepType(tis),
		}
		return pb, nil
	case *cmtcons.WALMessage_EndHeight:
		pb := EndHeightMessage{
			Height: msg.EndHeight.Height,
		}
		return pb, nil
	default:
		return nil, fmt.Errorf("from proto: wal message not recognized: %T", msg)
	}
	return pb, nil
}

// minCompactRun is the shortest run worth removing. It comfortably exceeds the
// few bytes of extra fields the compact form adds, so compacting always shrinks
// the entry.
const minCompactRun = 64

// compactBlockPart removes the longest run of a repeated byte from a block part.
// It returns nil when the run is too short to be worth it.
func compactBlockPart(bp *cmtcons.BlockPart, peerID string) *cmtcons.WALBlockPart {
	b := bp.Part.Bytes
	start, count := longestRepeatedByteRun(b)
	if count < minCompactRun {
		return nil
	}
	compact := *bp
	// The capacity limit forces append to copy, leaving the caller's bytes intact.
	compact.Part.Bytes = append(b[:start:start], b[start+count:]...)
	return &cmtcons.WALBlockPart{
		BlockPart:     compact,
		PeerID:        peerID,
		RepeatedStart: uint32(start),
		RepeatedCount: uint32(count),
		RepeatedByte:  []byte{b[start]},
	}
}

func longestRepeatedByteRun(data []byte) (start, count int) {
	for i := 0; i < len(data); {
		j := i + 1
		for j < len(data) && data[j] == data[i] {
			j++
		}
		if j-i > count {
			start, count = i, j-i
		}
		i = j
	}
	return start, count
}

// expandBlockPart restores the repeated run removed by compactBlockPart in place.
func expandBlockPart(c *cmtcons.WALBlockPart) error {
	if c == nil {
		return errors.New("nil compact block part")
	}
	b := c.BlockPart.Part.Bytes
	start, count := int(c.RepeatedStart), int(c.RepeatedCount)
	switch {
	case len(c.RepeatedByte) != 1:
		return fmt.Errorf("repeated byte must contain exactly one byte, got %d", len(c.RepeatedByte))
	case start > len(b):
		return fmt.Errorf("repeated start %d exceeds compact block part length %d", start, len(b))
	case len(b)+count > int(types.BlockPartSizeBytes):
		return fmt.Errorf("expanded block part exceeds maximum size %d", types.BlockPartSizeBytes)
	}
	expanded := make([]byte, len(b)+count)
	copy(expanded, b[:start])
	copy(expanded[start+count:], b[start:])
	run := expanded[start : start+count]
	for i := range run {
		run[i] = c.RepeatedByte[0]
	}
	c.BlockPart.Part.Bytes = expanded
	return nil
}
