package types

import (
	"bytes"
	"encoding/base64"
	"encoding/json"

	"github.com/cosmos/gogoproto/jsonpb"
)

const (
	CodeTypeOK uint32 = 0
)

// IsOK returns true if Code is OK.
func (r ResponseCheckTx) IsOK() bool {
	return r.Code == CodeTypeOK
}

// IsErr returns true if Code is something other than OK.
func (r ResponseCheckTx) IsErr() bool {
	return r.Code != CodeTypeOK
}

// IsOK returns true if Code is OK.
func (r ExecTxResult) IsOK() bool {
	return r.Code == CodeTypeOK
}

// IsErr returns true if Code is something other than OK.
func (r ExecTxResult) IsErr() bool {
	return r.Code != CodeTypeOK
}

// IsOK returns true if Code is OK.
func (r ResponseQuery) IsOK() bool {
	return r.Code == CodeTypeOK
}

// IsErr returns true if Code is something other than OK.
func (r ResponseQuery) IsErr() bool {
	return r.Code != CodeTypeOK
}

// IsAccepted returns true if Code is ACCEPT
func (r ResponseProcessProposal) IsAccepted() bool {
	return r.Status == ResponseProcessProposal_ACCEPT
}

// IsStatusUnknown returns true if Code is UNKNOWN
func (r ResponseProcessProposal) IsStatusUnknown() bool {
	return r.Status == ResponseProcessProposal_UNKNOWN
}

func (r ResponseVerifyVoteExtension) IsAccepted() bool {
	return r.Status == ResponseVerifyVoteExtension_ACCEPT
}

// IsStatusUnknown returns true if Code is Unknown
func (r ResponseVerifyVoteExtension) IsStatusUnknown() bool {
	return r.Status == ResponseVerifyVoteExtension_UNKNOWN
}

//---------------------------------------------------------------------------
// override JSON marshaling so we emit defaults (ie. disable omitempty)

var (
	jsonpbMarshaller = jsonpb.Marshaler{
		EnumsAsInts:  true,
		EmitDefaults: true,
	}
	jsonpbUnmarshaller = jsonpb.Unmarshaler{}
)

func (r *ResponseCheckTx) MarshalJSON() ([]byte, error) {
	s, err := jsonpbMarshaller.MarshalToString(r)
	return []byte(s), err
}

func (r *ResponseCheckTx) UnmarshalJSON(b []byte) error {
	reader := bytes.NewBuffer(b)
	return jsonpbUnmarshaller.Unmarshal(reader, r)
}

func (r *ExecTxResult) MarshalJSON() ([]byte, error) {
	s, err := jsonpbMarshaller.MarshalToString(r)
	return []byte(s), err
}

func (r *ExecTxResult) UnmarshalJSON(b []byte) error {
	reader := bytes.NewBuffer(b)
	return jsonpbUnmarshaller.Unmarshal(reader, r)
}

func (r *ResponseQuery) MarshalJSON() ([]byte, error) {
	s, err := jsonpbMarshaller.MarshalToString(r)
	return []byte(s), err
}

func (r *ResponseQuery) UnmarshalJSON(b []byte) error {
	reader := bytes.NewBuffer(b)
	return jsonpbUnmarshaller.Unmarshal(reader, r)
}

func (r *ResponseCommit) MarshalJSON() ([]byte, error) {
	s, err := jsonpbMarshaller.MarshalToString(r)
	return []byte(s), err
}

func (r *ResponseCommit) UnmarshalJSON(b []byte) error {
	reader := bytes.NewBuffer(b)
	return jsonpbUnmarshaller.Unmarshal(reader, r)
}

func (r *EventAttribute) MarshalJSON() ([]byte, error) {
	s, err := jsonpbMarshaller.MarshalToString(r)
	return []byte(s), err
}

// UnmarshalJSON was modified in the Celestia fork to be backwards compatible
// with the EventAttribute from CometBFT v0.34.x. CometBFT v0.38.x uses the type
// string for keys and values. CometBFT v0.34.x used the type bytes for keys and
// values. CometBFT v0.34.x event attributes that were marshaled to JSON
// previously encoded the keys and values as base64 strings so this method
// attempts to base64 decode the keys and values if they look like base64
// encoded data.
func (r *EventAttribute) UnmarshalJSON(b []byte) error {
	var eventAttribute struct {
		Key   string `json:"key,omitempty"`
		Value string `json:"value,omitempty"`
		Index bool   `json:"index,omitempty"`
	}

	if err := json.Unmarshal(b, &eventAttribute); err != nil {
		return err
	}

	r.Key = maybeBase64Decode(eventAttribute.Key)
	r.Value = maybeBase64Decode(eventAttribute.Value)
	r.Index = eventAttribute.Index
	return nil
}

func maybeBase64Decode(input string) string {
	if isLikelyBase64Encoded(input) {
		bytes, err := base64.StdEncoding.DecodeString(input)
		if err != nil {
			return input // input is not a base64 encoded string so return the input
		}
		decoded := string(bytes)
		return decoded
	}
	return input
}

// isLikelyBase64Encoded returns true if input is likely a base64 encoded string.
func isLikelyBase64Encoded(input string) bool {
	bytes, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return false
	}
	decoded := string(bytes)
	if input == decoded {
		return false
	}

	// Only decode if the result is printable ASCII/UTF-8 text
	for _, r := range decoded {
		// Allow printable ASCII characters, spaces, and common unicode
		if r < 32 && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
		// Reject high-value bytes that are likely binary garbage
		if r > 127 && r == '\ufffd' {
			return false
		}
	}

	// Additional heuristic: old format base64 was typically longer
	// and had padding or specific characteristics
	if len(input) < 8 {
		// Short strings that happen to be valid base64 are probably just normal strings
		return false
	}
	return true
}

// Some compile time assertions to ensure we don't
// have accidental runtime surprises later on.

// jsonEncodingRoundTripper ensures that asserted
// interfaces implement both MarshalJSON and UnmarshalJSON
type jsonRoundTripper interface {
	json.Marshaler
	json.Unmarshaler
}

var _ jsonRoundTripper = (*ResponseCommit)(nil)
var _ jsonRoundTripper = (*ResponseQuery)(nil)
var _ jsonRoundTripper = (*ExecTxResult)(nil)
var _ jsonRoundTripper = (*ResponseCheckTx)(nil)

var _ jsonRoundTripper = (*EventAttribute)(nil)

// deterministicExecTxResult constructs a copy of response that omits
// non-deterministic fields. The input response is not modified.
func deterministicExecTxResult(response *ExecTxResult) *ExecTxResult {
	return &ExecTxResult{
		Code:      response.Code,
		Data:      response.Data,
		GasWanted: response.GasWanted,
		GasUsed:   response.GasUsed,
	}
}

// MarshalTxResults encodes the the TxResults as a list of byte
// slices. It strips off the non-deterministic pieces of the TxResults
// so that the resulting data can be used for hash comparisons and used
// in Merkle proofs.
func MarshalTxResults(r []*ExecTxResult) ([][]byte, error) {
	s := make([][]byte, len(r))
	for i, e := range r {
		d := deterministicExecTxResult(e)
		b, err := d.Marshal()
		if err != nil {
			return nil, err
		}
		s[i] = b
	}
	return s, nil
}

// -----------------------------------------------
// construct Result data
