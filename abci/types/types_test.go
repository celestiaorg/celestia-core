package types_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"encoding/base64"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/merkle"
)

func TestHashAndProveResults(t *testing.T) {
	trs := []*abci.ExecTxResult{
		// Note, these tests rely on the first two entries being in this order.
		{Code: 0, Data: nil},
		{Code: 0, Data: []byte{}},

		{Code: 0, Data: []byte("one")},
		{Code: 14, Data: nil},
		{Code: 14, Data: []byte("foo")},
		{Code: 14, Data: []byte("bar")},
	}

	// Nil and []byte{} should produce the same bytes
	bz0, err := trs[0].Marshal()
	require.NoError(t, err)
	bz1, err := trs[1].Marshal()
	require.NoError(t, err)
	require.Equal(t, bz0, bz1)

	// Make sure that we can get a root hash from results and verify proofs.
	rs, err := abci.MarshalTxResults(trs)
	require.NoError(t, err)
	root := merkle.HashFromByteSlices(rs)
	assert.NotEmpty(t, root)

	_, proofs := merkle.ProofsFromByteSlices(rs)
	for i, tr := range trs {
		bz, err := tr.Marshal()
		require.NoError(t, err)

		valid := proofs[i].Verify(root, bz)
		assert.NoError(t, valid, "%d", i)
	}
}

func TestHashDeterministicFieldsOnly(t *testing.T) {
	tr1 := abci.ExecTxResult{
		Code:      1,
		Data:      []byte("transaction"),
		Log:       "nondeterministic data: abc",
		Info:      "nondeterministic data: abc",
		GasWanted: 1000,
		GasUsed:   1000,
		Events:    []abci.Event{},
		Codespace: "nondeterministic.data.abc",
	}
	tr2 := abci.ExecTxResult{
		Code:      1,
		Data:      []byte("transaction"),
		Log:       "nondeterministic data: def",
		Info:      "nondeterministic data: def",
		GasWanted: 1000,
		GasUsed:   1000,
		Events:    []abci.Event{},
		Codespace: "nondeterministic.data.def",
	}
	r1, err := abci.MarshalTxResults([]*abci.ExecTxResult{&tr1})
	require.NoError(t, err)
	r2, err := abci.MarshalTxResults([]*abci.ExecTxResult{&tr2})
	require.NoError(t, err)
	require.Equal(t, merkle.HashFromByteSlices(r1), merkle.HashFromByteSlices(r2))
}

// OldEventAttribute is the type of EventAttribute from CometBFT v0.34.x.
type OldEventAttribute struct {
	Key   []byte `json:"key,omitempty"`
	Value []byte `json:"value,omitempty"`
	Index bool   `json:"index,omitempty"`
}

func TestV0_34JsonEventDecoding(t *testing.T) {
	// 1) JSON from the string‐field type:
	eventAttr := &abci.EventAttribute{Key: "foo", Value: "bar!", Index: true}
	stringJSON, err := json.Marshal(eventAttr)
	require.NoError(t, err)
	// Try to unmarshal that into the bytes‐field type:
	var intoOld OldEventAttribute
	err = json.Unmarshal(stringJSON, &intoOld)
	// encoding/json for []byte will try to base64‐decode "bar!" and fail:
	require.Error(t, err)

	// 2) JSON from the bytes‐field type (base64):
	oldEventAttr := &OldEventAttribute{
		Key:   []byte("foo"),
		Value: []byte("bar!"),
		Index: true,
	}
	bytesJSON, err := json.Marshal(oldEventAttr)
	require.NoError(t, err)
	// That JSON.Value is base64("bar!") == "YmFyIQ==".
	// Unmarshal into the string‐field type:
	var intoNew abci.EventAttribute
	err = json.Unmarshal(bytesJSON, &intoNew)
	require.NoError(t, err)
	// But intoNew.Value is now the base64 decoded string, not the original:
	require.Equal(t, "bar!", intoNew.Value,
		"base64 input becomes the decoded string when unmarshaled into a Go string field")
}

func TestUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		jsonData string
		expected abci.EventAttribute
	}{
		{
			name:     "normal string 'minter' works fine",
			jsonData: `{"key":"minter","value":"celestia1m3h30wlvsf8llruxtpukdvsy0km2kum8emkgad","index":true}`,
			expected: abci.EventAttribute{
				Key:   "minter",
				Value: "celestia1m3h30wlvsf8llruxtpukdvsy0km2kum8emkgad",
				Index: true,
			},
		},
		{
			name:     "normal string 'receiver' gets corrupted",
			jsonData: `{"key":"receiver","value":"celestia1m3h30wlvsf8llruxtpukdvsy0km2kum8emkgad","index":true}`,
			expected: abci.EventAttribute{
				Key:   "receiver",
				Value: "celestia1m3h30wlvsf8llruxtpukdvsy0km2kum8emkgad",
				Index: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var event abci.EventAttribute
			err := json.Unmarshal([]byte(tt.jsonData), &event)
			require.NoError(t, err)
			require.Equal(t, tt.expected.Key, event.Key)
			require.Equal(t, tt.expected.Value, event.Value)
			require.Equal(t, tt.expected.Index, event.Index)
		})
	}
}

func TestEventAttributeBase64Encoding_Analysis(t *testing.T) {
	// Test to understand why "receiver" gets corrupted but "minter" doesn't
	tests := []string{"receiver", "minter", "spender", "amount"}

	for _, str := range tests {
		t.Run(str, func(t *testing.T) {
			// Try to base64 decode the string
			if decoded, err := base64.StdEncoding.DecodeString(str); err == nil {
				t.Logf("String %q can be base64-decoded to: %v (bytes: %v)",
					str, string(decoded), decoded)
				t.Logf("Decoded != original: %v", string(decoded) != str)
			} else {
				t.Logf("String %q cannot be base64-decoded: %v", str, err)
			}
		})
	}
}
