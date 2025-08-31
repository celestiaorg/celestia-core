package merkle

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtcrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
)

const ProofOpDomino = "test:domino"

// Expects given input, produces given output.
// Like the game dominos.
type DominoOp struct {
	key    string // unexported, may be empty
	Input  string
	Output string
}

func NewDominoOp(key, input, output string) DominoOp {
	return DominoOp{
		key:    key,
		Input:  input,
		Output: output,
	}
}

func (dop DominoOp) ProofOp() cmtcrypto.ProofOp {
	dopb := cmtcrypto.DominoOp{
		Key:    dop.key,
		Input:  dop.Input,
		Output: dop.Output,
	}
	bz, err := dopb.Marshal()
	if err != nil {
		panic(err)
	}

	return cmtcrypto.ProofOp{
		Type: ProofOpDomino,
		Key:  []byte(dop.key),
		Data: bz,
	}
}

func (dop DominoOp) Run(input [][]byte) (output [][]byte, err error) {
	if len(input) != 1 {
		return nil, errors.New("expected input of length 1")
	}
	if string(input[0]) != dop.Input {
		return nil, fmt.Errorf("expected input %v, got %v",
			dop.Input, string(input[0]))
	}
	return [][]byte{[]byte(dop.Output)}, nil
}

func (dop DominoOp) GetKey() []byte {
	return []byte(dop.key)
}

//----------------------------------------

func TestProofOperators(t *testing.T) {
	var err error

	// ProofRuntime setup
	// TODO test this somehow.

	// ProofOperators setup
	op1 := NewDominoOp("KEY1", "INPUT1", "INPUT2")
	op2 := NewDominoOp("KEY2", "INPUT2", "INPUT3")
	op3 := NewDominoOp("", "INPUT3", "INPUT4")
	op4 := NewDominoOp("KEY4", "INPUT4", "OUTPUT4")

	// Good
	popz := ProofOperators([]ProofOperator{op1, op2, op3, op4})
	err = popz.Verify(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.Nil(t, err)
	err = popz.VerifyValue(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", bz("INPUT1"))
	assert.Nil(t, err)

	// BAD INPUT
	err = popz.Verify(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1_WRONG")})
	assert.NotNil(t, err)
	err = popz.VerifyValue(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", bz("INPUT1_WRONG"))
	assert.NotNil(t, err)

	// BAD KEY 1
	err = popz.Verify(bz("OUTPUT4"), "/KEY3/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD KEY 2
	err = popz.Verify(bz("OUTPUT4"), "KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD KEY 3
	err = popz.Verify(bz("OUTPUT4"), "/KEY4/KEY2/KEY1/", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD KEY 4
	err = popz.Verify(bz("OUTPUT4"), "//KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD KEY 5
	err = popz.Verify(bz("OUTPUT4"), "/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD OUTPUT 1
	err = popz.Verify(bz("OUTPUT4_WRONG"), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD OUTPUT 2
	err = popz.Verify(bz(""), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD POPZ 1
	popz = []ProofOperator{op1, op2, op4}
	err = popz.Verify(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD POPZ 2
	popz = []ProofOperator{op4, op3, op2, op1}
	err = popz.Verify(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)

	// BAD POPZ 3
	popz = []ProofOperator{}
	err = popz.Verify(bz("OUTPUT4"), "/KEY4/KEY2/KEY1", [][]byte{bz("INPUT1")})
	assert.NotNil(t, err)
}

func bz(s string) []byte {
	return []byte(s)
}

func TestProofValidateBasic(t *testing.T) {
	testCases := []struct {
		testName      string
		malleateProof func(*Proof)
		errStr        string
	}{
		{"Good", func(sp *Proof) {}, ""},
		{"Negative Total", func(sp *Proof) { sp.Total = -1 }, "negative Total"},
		{"Negative Index", func(sp *Proof) { sp.Index = -1 }, "negative Index"},
		{"Invalid LeafHash", func(sp *Proof) { sp.LeafHash = make([]byte, 10) },
			"expected LeafHash size to be 32, got 10"},
		{"Too many Aunts", func(sp *Proof) { sp.Aunts = make([][]byte, MaxAunts+1) },
			"expected no more than 100 aunts, got 101"},
		{"Invalid Aunt", func(sp *Proof) { sp.Aunts[0] = make([]byte, 10) },
			"expected Aunts#0 size to be 32, got 10"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			_, proofs := ProofsFromByteSlices([][]byte{
				[]byte("apple"),
				[]byte("watermelon"),
				[]byte("kiwi"),
			})
			tc.malleateProof(proofs[0])
			err := proofs[0].ValidateBasic()
			if tc.errStr != "" {
				assert.Contains(t, err.Error(), tc.errStr)
			}
		})
	}
}
func TestVoteProtobuf(t *testing.T) {
	_, proofs := ProofsFromByteSlices([][]byte{
		[]byte("apple"),
		[]byte("watermelon"),
		[]byte("kiwi"),
	})

	testCases := []struct {
		testName string
		v1       *Proof
		expPass  bool
	}{
		{"empty proof", &Proof{}, false},
		{"failure nil", nil, false},
		{"success", proofs[0], true},
	}
	for _, tc := range testCases {
		pb := tc.v1.ToProto()

		v, err := ProofFromProto(pb, false)
		if tc.expPass {
			require.NoError(t, err)
			require.Equal(t, tc.v1, v, tc.testName)
		} else {
			require.Error(t, err)
		}
	}
}

// commonProofTestCases contains test cases shared between multiple test functions
var commonProofTestCases = []struct {
	name  string
	input [][]byte
}{
	{
		name:  "Empty input",
		input: [][]byte{},
	},
	{
		name:  "Single element",
		input: [][]byte{[]byte("A")},
	},
	{
		name:  "Two elements",
		input: [][]byte{[]byte("A"), []byte("B")},
	},
	{
		name:  "Three elements",
		input: [][]byte{[]byte("A"), []byte("B"), []byte("C")},
	},
	{
		name:  "Four elements",
		input: [][]byte{[]byte("A"), []byte("B"), []byte("C"), []byte("D")},
	},
	{
		name:  "Duplicates",
		input: [][]byte{[]byte("A"), []byte("A"), []byte("B"), []byte("B")},
	},
	{
		name:  "Varying sizes",
		input: [][]byte{[]byte("short"), []byte("medium-size"), []byte("a much longer string with more entropy")},
	},
	{
		name:  "Non-UTF-8 bytes",
		input: [][]byte{{0xff, 0xfe, 0xfd}, {0x00, 0x01, 0x02}, {0x80, 0x81, 0x82}},
	},
	{
		name:  "Leading/trailing zeros",
		input: [][]byte{{0x00, 0x00, 0x00}, {0xff, 0xff, 0xff}, {0x00, 0x01, 0x02, 0x00}},
	},
}

func TestProofsFromLeafHashesAndByteSlices(t *testing.T) {
	testCases := commonProofTestCases

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			root1, proofs1 := ProofsFromByteSlices(tc.input)

			leafHashes := make([][]byte, len(proofs1))
			for i, proof := range proofs1 {
				leafHashes[i] = proof.LeafHash
			}

			root2, proofs2 := ProofsFromLeafHashes(leafHashes)

			// Root hashes should be identical
			require.Equal(t, root1, root2, "root hashes should be equal")

			// Proofs should be identical in length
			require.Equal(t, len(proofs1), len(proofs2), "proofs length should be equal")

			// Each proof should match exactly
			for i := range proofs1 {
				require.NotNil(t, proofs1[i], "proofs1[%d] should not be nil", i)
				require.NotNil(t, proofs2[i], "proofs2[%d] should not be nil", i)

				assert.Equal(t, proofs1[i].Total, proofs2[i].Total, "Total field mismatch")
				assert.Equal(t, proofs1[i].Index, proofs2[i].Index, "Index field mismatch")
				assert.Equal(t, proofs1[i].LeafHash, proofs2[i].LeafHash, "LeafHash field mismatch")
				assert.Equal(t, proofs1[i].Aunts, proofs2[i].Aunts, "Aunts field mismatch")
			}
		})
	}
}

// TestVsa2022_100 verifies https://blog.verichains.io/p/vsa-2022-100-tendermint-forging-membership-proof
func TestVsa2022_100(t *testing.T) {
	// a fake key-value pair and its hash
	key := []byte{0x13}
	value := []byte{0x37}
	vhash := tmhash.Sum(value)
	bz := new(bytes.Buffer)
	_ = encodeByteSlice(bz, key)
	_ = encodeByteSlice(bz, vhash)
	kvhash := tmhash.Sum(append([]byte{0}, bz.Bytes()...))

	// the malicious `op`
	op := NewValueOp(
		key,
		&Proof{LeafHash: kvhash},
	)

	// the nil root
	var root []byte

	assert.NotNil(t, ProofOperators{op}.Verify(root, "/"+string(key), [][]byte{value}))
}

func TestProofOperatorsFromKeys(t *testing.T) {
	var err error

	// ProofRuntime setup
	// TODO test this somehow.

	// ProofOperators setup
	op1 := NewDominoOp("KEY1", "INPUT1", "INPUT2")
	op2 := NewDominoOp("KEY%2", "INPUT2", "INPUT3")
	op3 := NewDominoOp("", "INPUT3", "INPUT4")
	op4 := NewDominoOp("KEY/4", "INPUT4", "OUTPUT4")

	// add characters to the keys that would otherwise result in bad keypath if
	// processed
	keys1 := [][]byte{bz("KEY/4"), bz("KEY%2"), bz("KEY1")}
	badkeys1 := [][]byte{bz("WrongKey"), bz("KEY%2"), bz("KEY1")}
	keys2 := [][]byte{bz("KEY3"), bz("KEY%2"), bz("KEY1")}
	keys3 := [][]byte{bz("KEY2"), bz("KEY1")}

	// Good
	popz := ProofOperators([]ProofOperator{op1, op2, op3, op4})
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys1, [][]byte{bz("INPUT1")})
	assert.NoError(t, err)

	// BAD INPUT
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys1, [][]byte{bz("INPUT1_WRONG")})
	assert.Error(t, err)

	// BAD KEY 1
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys2, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD KEY 2
	err = popz.VerifyFromKeys(bz("OUTPUT4"), badkeys1, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD KEY 5
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys3, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD OUTPUT 1
	err = popz.VerifyFromKeys(bz("OUTPUT4_WRONG"), keys1, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD OUTPUT 2
	err = popz.VerifyFromKeys(bz(""), keys1, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD POPZ 1
	popz = []ProofOperator{op1, op2, op4}
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys1, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD POPZ 2
	popz = []ProofOperator{op4, op3, op2, op1}
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys1, [][]byte{bz("INPUT1")})
	assert.Error(t, err)

	// BAD POPZ 3
	popz = []ProofOperator{}
	err = popz.VerifyFromKeys(bz("OUTPUT4"), keys1, [][]byte{bz("INPUT1")})
	assert.Error(t, err)
}
