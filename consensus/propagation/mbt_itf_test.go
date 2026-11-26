package propagation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mbtTraceDir holds the committed ITF traces of the Quint model of the
// compact-block hand-off (spec/consensus/propagation, `make traces`).
const mbtTraceDir = "../../spec/consensus/propagation/traces"

// The types below mirror the variables of spec/consensus/propagation's
// propagation.qnt as its README ("State", "Trace schema (ITF)") describes
// them. Field names match the ITF record fields case-insensitively.

type itfEntry struct {
	Kind      string // "none" | "full" | "stub"
	Block     string // identity of a full entry: "A" | "B" | "X"
	Psh       string // part set header: "A" | "B"
	Forwarded bool
	Catchup   bool
	Orig      []int // held original indexes (canonical, see README abstraction 5)
	Par       int   // held parity parts, indexes K..K+Par-1
}

type itfUnverified struct {
	Has   bool
	Round int
	Block string
}

// itfPeerRound is a (peer, round) tuple.
type itfPeerRound struct {
	Peer  string
	Round int
}

// itfRoundBlock is a (round, block) tuple.
type itfRoundBlock struct {
	Round int
	Block string
}

type itfProp struct {
	Round      int
	Entries    map[int]itfEntry
	Unverified map[string]itfUnverified
	ReqO       []itfPeerRound
	ReqP       []itfPeerRound
	Gossiped   []itfRoundBlock
}

type itfProposal struct {
	Round int
	Block string
	From  string // peer, or "self" for a cached proposal
}

type itfMsg struct {
	Kind  string
	Round int
	Block string
	From  string
}

type itfCons struct {
	Round     int
	PC        string
	CbRound   int
	CbBlock   string
	Peer      string
	Retried   bool
	N         int
	Committed map[int]string
}

type itfSync struct {
	PC  string
	Msg itfMsg
}

type itfRecv struct {
	PC          string
	Round       int
	Block       string
	From        string
	Added       bool
	N           int
	DecodeAfter bool
}

type itfDeliv struct {
	PC    string
	Block string
	N     int
}

type itfHist struct {
	Fwd            []itfRoundBlock
	DupForward     bool
	S3Decision     bool
	S3Gossip       bool
	S3Deliver      bool
	DelivOverwrite bool
}

type itfObs struct {
	PropLog []itfProposal
	ConsLog []itfProposal
}

// itfAction is the model's lastAction: the step that led to a state.
type itfAction struct {
	Kind     string
	Env      bool
	Peer     string
	Round    int
	Block    string
	Psh      string
	Part     string
	Idx      int
	Corrupt  bool
	Proof    bool
	Accepted bool
}

type itfState struct {
	Prop       itfProp
	Cons       itfCons
	Syncd      itfSync
	Recv       itfRecv
	Deliv      map[int]itfDeliv
	PropChan   []itfProposal
	PartChan   []int
	Pmq        []itfMsg
	Hist       itfHist
	Obs        itfObs
	LastAction itfAction
}

type itfTrace struct {
	Name   string
	States []itfState
}

func (t *itfPeerRound) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) != 2 {
		return fmt.Errorf("(peer, round) tuple: %s", b)
	}
	if err := json.Unmarshal(raw[0], &t.Peer); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &t.Round)
}

func (t *itfRoundBlock) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) != 2 {
		return fmt.Errorf("(round, block) tuple: %s", b)
	}
	if err := json.Unmarshal(raw[0], &t.Round); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &t.Block)
}

// readITFTrace reads an ITF trace. Variable names are module-qualified
// (fixedTraced::propagation::prop); the qualifier is stripped.
func readITFTrace(path string) (*itfTrace, error) {
	bz, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(bz))
	dec.UseNumber()
	var doc struct {
		States []map[string]any `json:"states"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tr := &itfTrace{Name: strings.TrimSuffix(filepath.Base(path), ".itf.json")}
	for i, raw := range doc.States {
		vars := make(map[string]any, len(raw))
		for k, v := range raw {
			if strings.HasPrefix(k, "#") {
				continue
			}
			if j := strings.LastIndex(k, "::"); j >= 0 {
				k = k[j+2:]
			}
			nv, err := normalizeITF(v)
			if err != nil {
				return nil, fmt.Errorf("%s: state %d, %s: %w", path, i, k, err)
			}
			vars[k] = nv
		}
		plain, err := json.Marshal(vars)
		if err != nil {
			return nil, err
		}
		var st itfState
		if err := json.Unmarshal(plain, &st); err != nil {
			return nil, fmt.Errorf("%s: state %d: %w", path, i, err)
		}
		for r, e := range st.Prop.Entries {
			sort.Ints(e.Orig)
			st.Prop.Entries[r] = e
		}
		tr.States = append(tr.States, st)
	}
	if len(tr.States) == 0 {
		return nil, fmt.Errorf("%s: no states", path)
	}
	return tr, nil
}

// normalizeITF rewrites ITF encodings into plain JSON: {"#bigint": "n"} to a
// number, {"#set": [...]} and {"#tup": [...]} to arrays, and {"#map": [[k, v],
// ...]} to an object (keys are strings or integers in this model).
func normalizeITF(v any) (any, error) {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			n, err := normalizeITF(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case map[string]any:
		if s, ok := x["#bigint"]; ok {
			str, ok := s.(string)
			if !ok {
				return nil, fmt.Errorf("bad #bigint %v", s)
			}
			return json.Number(str), nil
		}
		if s, ok := x["#set"]; ok {
			return normalizeITF(s)
		}
		if s, ok := x["#tup"]; ok {
			return normalizeITF(s)
		}
		if s, ok := x["#map"]; ok {
			pairs, ok := s.([]any)
			if !ok {
				return nil, fmt.Errorf("bad #map %v", s)
			}
			out := make(map[string]any, len(pairs))
			for _, p := range pairs {
				kv, ok := p.([]any)
				if !ok || len(kv) != 2 {
					return nil, fmt.Errorf("bad #map pair %v", p)
				}
				k, err := normalizeITF(kv[0])
				if err != nil {
					return nil, err
				}
				val, err := normalizeITF(kv[1])
				if err != nil {
					return nil, err
				}
				out[fmt.Sprint(k)] = val
			}
			return out, nil
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			n, err := normalizeITF(e)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	default:
		return v, nil
	}
}

// TestMBTITFReader checks the ITF reader on a committed trace of the model in
// spec/consensus/propagation (see its README, "Trace schema (ITF)").
func TestMBTITFReader(t *testing.T) {
	tr, err := readITFTrace(filepath.Join(mbtTraceDir, "fixed_noWaitCycleTest.itf.json"))
	require.NoError(t, err)
	require.Len(t, tr.States, 15)

	s0 := tr.States[0]
	require.Equal(t, "init", s0.LastAction.Kind)
	require.Equal(t, 0, s0.Prop.Round)
	require.Equal(t, "none", s0.Prop.Entries[0].Kind)
	require.Equal(t, "none", s0.Prop.Entries[1].Kind)
	require.False(t, s0.Prop.Unverified["p2"].Has)
	require.Equal(t, "none", s0.Deliv[1].PC)

	a1 := tr.States[1].LastAction
	require.Equal(t, "compactBlock", a1.Kind)
	require.True(t, a1.Env)
	require.Equal(t, "p1", a1.Peer)
	require.Equal(t, 0, a1.Round)
	require.Equal(t, "A", a1.Block)
	require.True(t, a1.Accepted)
	require.True(t, tr.States[1].Prop.Entries[0].Forwarded)
	require.Equal(t, "sendProp", tr.States[1].Recv.PC)

	// recvSendProp put A into proposalChan
	s2 := tr.States[2]
	require.Equal(t, []itfProposal{{Round: 0, Block: "A", From: "p1"}}, s2.PropChan)
	require.Equal(t, []itfProposal{{Round: 0, Block: "A", From: "p1"}}, s2.Obs.PropLog)

	// recvGossip gossiped A and rebuilt the mempool original
	s3 := tr.States[3]
	require.Equal(t, []itfRoundBlock{{Round: 0, Block: "A"}}, s3.Prop.Gossiped)
	require.Equal(t, []int{0}, s3.Prop.Entries[0].Orig)

	// p2's compact block for round 1 is cached
	s5 := tr.States[5]
	require.Equal(t, itfUnverified{Has: true, Round: 1, Block: "A"}, s5.Prop.Unverified["p2"])

	last := tr.States[len(tr.States)-1]
	require.Equal(t, "delivSendProp", last.LastAction.Kind)
	require.ElementsMatch(t, []itfRoundBlock{{0, "A"}, {1, "A"}}, last.Hist.Fwd)
	require.Empty(t, last.PartChan)
	require.Equal(t, []itfProposal{{Round: 1, Block: "A", From: "self"}}, last.PropChan)
}
