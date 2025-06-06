package bits

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
)

var (
	empty16Bits = "________________"
	empty64Bits = empty16Bits + empty16Bits + empty16Bits + empty16Bits
	full16bits  = "xxxxxxxxxxxxxxxx"
	full64bits  = full16bits + full16bits + full16bits + full16bits
)

func randBitArray(bits int) *BitArray {
	src := cmtrand.Bytes((bits + 7) / 8)
	srcIndexToBit := func(i int) bool {
		return src[i/8]&(1<<uint(i%8)) > 0
	}
	return NewBitArrayFromFn(bits, srcIndexToBit)
}

func TestAnd(t *testing.T) {
	bA1 := randBitArray(51)
	bA2 := randBitArray(31)
	bA3 := bA1.And(bA2)

	var bNil *BitArray
	require.Equal(t, bNil.And(bA1), (*BitArray)(nil))
	require.Equal(t, bA1.And(nil), (*BitArray)(nil))
	require.Equal(t, bNil.And(nil), (*BitArray)(nil))

	if bA3.Bits != 31 {
		t.Error("Expected min bits", bA3.Bits)
	}
	if len(bA3.Elems) != len(bA2.Elems) {
		t.Error("Expected min elems length")
	}
	for i := 0; i < bA3.Bits; i++ {
		expected := bA1.GetIndex(i) && bA2.GetIndex(i)
		if bA3.GetIndex(i) != expected {
			t.Error("Wrong bit from bA3", i, bA1.GetIndex(i), bA2.GetIndex(i), bA3.GetIndex(i))
		}
	}
}

func TestOr(t *testing.T) {
	bA1 := randBitArray(57)
	bA2 := randBitArray(31)
	bA3 := bA1.Or(bA2)

	bNil := (*BitArray)(nil)
	require.Equal(t, bNil.Or(bA1), bA1)
	require.Equal(t, bA1.Or(nil), bA1)
	require.Equal(t, bNil.Or(nil), (*BitArray)(nil))

	if bA3.Bits != 57 {
		t.Error("Expected max bits")
	}
	if len(bA3.Elems) != len(bA1.Elems) {
		t.Error("Expected max elems length")
	}
	for i := 0; i < bA3.Bits; i++ {
		expected := bA1.GetIndex(i) || bA2.GetIndex(i)
		if bA3.GetIndex(i) != expected {
			t.Error("Wrong bit from bA3", i, bA1.GetIndex(i), bA2.GetIndex(i), bA3.GetIndex(i))
		}
	}
	if bA3.getNumTrueIndices() == 0 {
		t.Error("Expected at least one true bit. " +
			"This has a false positive rate that is less than 1 in 2^80 (cryptographically improbable).")
	}
}

func TestSub(t *testing.T) {
	testCases := []struct {
		initBA        string
		subtractingBA string
		expectedBA    string
	}{
		{`null`, `null`, `null`},
		{`"x"`, `null`, `null`},
		{`null`, `"x"`, `null`},
		{`"x"`, `"x"`, `"_"`},
		{`"xxxxxx"`, `"x_x_x_"`, `"_x_x_x"`},
		{`"x_x_x_"`, `"xxxxxx"`, `"______"`},
		{`"xxxxxx"`, `"x_x_x_xxxx"`, `"_x_x_x"`},
		{`"x_x_x_xxxx"`, `"xxxxxx"`, `"______xxxx"`},
		{`"xxxxxxxxxx"`, `"x_x_x_"`, `"_x_x_xxxxx"`},
		{`"x_x_x_"`, `"xxxxxxxxxx"`, `"______"`},
	}
	for _, tc := range testCases {
		var bA *BitArray
		err := json.Unmarshal([]byte(tc.initBA), &bA)
		require.Nil(t, err)

		var o *BitArray
		err = json.Unmarshal([]byte(tc.subtractingBA), &o)
		require.Nil(t, err)

		got, _ := json.Marshal(bA.Sub(o))
		require.Equal(
			t,
			tc.expectedBA,
			string(got),
			"%s minus %s doesn't equal %s",
			tc.initBA,
			tc.subtractingBA,
			tc.expectedBA,
		)
	}
}

func TestPickRandom(t *testing.T) {
	testCases := []struct {
		bA string
		ok bool
	}{
		{`null`, false},
		{`"x"`, true},
		{`"` + empty16Bits + `"`, false},
		{`"x` + empty16Bits + `"`, true},
		{`"` + empty16Bits + `x"`, true},
		{`"x` + empty16Bits + `x"`, true},
		{`"` + empty64Bits + `"`, false},
		{`"x` + empty64Bits + `"`, true},
		{`"` + empty64Bits + `x"`, true},
		{`"x` + empty64Bits + `x"`, true},
		{`"` + empty64Bits + `___x"`, true},
	}
	for _, tc := range testCases {
		var bitArr *BitArray
		err := json.Unmarshal([]byte(tc.bA), &bitArr)
		require.NoError(t, err)
		_, ok := bitArr.PickRandom()
		require.Equal(t, tc.ok, ok, "PickRandom got an unexpected result on input %s", tc.bA)
	}
}

func TestGetNumTrueIndices(t *testing.T) {
	type testcase struct {
		Input          string
		ExpectedResult int
	}
	testCases := []testcase{
		{"x_x_x_", 3},
		{"______", 0},
		{"xxxxxx", 6},
		{"x_x_x_x_x_x_x_x_x_", 9},
	}
	numOriginalTestCases := len(testCases)
	for i := 0; i < numOriginalTestCases; i++ {
		testCases = append(testCases, testcase{testCases[i].Input + "x", testCases[i].ExpectedResult + 1})
		testCases = append(testCases, testcase{full64bits + testCases[i].Input, testCases[i].ExpectedResult + 64})
		testCases = append(testCases, testcase{empty64Bits + testCases[i].Input, testCases[i].ExpectedResult})
	}

	for _, tc := range testCases {
		var bitArr *BitArray
		err := json.Unmarshal([]byte(`"`+tc.Input+`"`), &bitArr)
		require.NoError(t, err)
		result := bitArr.getNumTrueIndices()
		require.Equal(t, tc.ExpectedResult, result, "for input %s, expected %d, got %d", tc.Input, tc.ExpectedResult, result)
		result = bitArr.Not().getNumTrueIndices()
		require.Equal(t, bitArr.Bits-result, bitArr.getNumTrueIndices())
	}
}

func TestGetNthTrueIndex(t *testing.T) {
	type testcase struct {
		Input          string
		N              int
		ExpectedResult int
	}
	testCases := []testcase{
		// Basic cases
		{"x_x_x_", 0, 0},
		{"x_x_x_", 1, 2},
		{"x_x_x_", 2, 4},
		{"______", 1, -1},         // No true indices
		{"xxxxxx", 5, 5},          // Last true index
		{"x_x_x_x_x_x_x_", 9, -1}, // Out-of-range

		// Edge cases
		{"xxxxxx", 7, -1}, // Out-of-range
		{"______", 0, -1}, // No true indices
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 49, 49},  // Last true index
		{"____________________________________________", 1, -1},                               // No true indices
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 63, 63},  // last index of first word
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 64, 64},  // first index of second word
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 100, -1}, // Out-of-range

		// Input beyond 64 bits
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 99, 99}, // Last true index

		// Input less than 64 bits
		{"x_x_x_", 3, -1}, // Out-of-range
	}

	numOriginalTestCases := len(testCases)
	// Add 64 underscores to each test case
	for i := 0; i < numOriginalTestCases; i++ {
		expectedResult := testCases[i].ExpectedResult
		if expectedResult != -1 {
			expectedResult += 64
		}
		testCases = append(testCases, testcase{empty64Bits + testCases[i].Input, testCases[i].N, expectedResult})
	}

	for _, tc := range testCases {
		var bitArr *BitArray
		err := json.Unmarshal([]byte(`"`+tc.Input+`"`), &bitArr)
		require.NoError(t, err)

		// Get the nth true index
		result := bitArr.getNthTrueIndex(tc.N)

		require.Equal(t, tc.ExpectedResult, result, "for bit array %s, input %d,  expected %d, got %d", tc.Input, tc.N, tc.ExpectedResult, result)
	}
}

func TestBytes(_ *testing.T) {
	bA := NewBitArray(4)
	bA.SetIndex(0, true)
	check := func(bA *BitArray, bz []byte) {
		if !bytes.Equal(bA.Bytes(), bz) {
			panic(fmt.Sprintf("Expected %X but got %X", bz, bA.Bytes()))
		}
	}
	check(bA, []byte{0x01})
	bA.SetIndex(3, true)
	check(bA, []byte{0x09})

	bA = NewBitArray(9)
	check(bA, []byte{0x00, 0x00})
	bA.SetIndex(7, true)
	check(bA, []byte{0x80, 0x00})
	bA.SetIndex(8, true)
	check(bA, []byte{0x80, 0x01})

	bA = NewBitArray(16)
	check(bA, []byte{0x00, 0x00})
	bA.SetIndex(7, true)
	check(bA, []byte{0x80, 0x00})
	bA.SetIndex(8, true)
	check(bA, []byte{0x80, 0x01})
	bA.SetIndex(9, true)
	check(bA, []byte{0x80, 0x03})
}

func TestEmptyFull(t *testing.T) {
	ns := []int{47, 123}
	for _, n := range ns {
		bA := NewBitArray(n)
		if !bA.IsEmpty() {
			t.Fatal("Expected bit array to be empty")
		}
		for i := 0; i < n; i++ {
			bA.SetIndex(i, true)
		}
		if !bA.IsFull() {
			t.Fatal("Expected bit array to be full")
		}
	}
}

func TestUpdateNeverPanics(_ *testing.T) {
	newRandBitArray := func(n int) *BitArray {
		ba := randBitArray(n)
		return ba
	}
	pairs := []struct {
		a, b *BitArray
	}{
		{nil, nil},
		{newRandBitArray(10), newRandBitArray(12)},
		{newRandBitArray(23), newRandBitArray(23)},
		{newRandBitArray(37), nil},
		{nil, NewBitArray(10)},
	}

	for _, pair := range pairs {
		a, b := pair.a, pair.b
		a.Update(b)
		b.Update(a)
	}
}

func TestNewBitArrayNeverCrashesOnNegatives(_ *testing.T) {
	bitList := []int{-127, -128, -1 << 31}
	for _, bits := range bitList {
		_ = NewBitArray(bits)
	}
}

func TestJSONMarshalUnmarshal(t *testing.T) {
	bA1 := NewBitArray(0)

	bA2 := NewBitArray(1)

	bA3 := NewBitArray(1)
	bA3.SetIndex(0, true)

	bA4 := NewBitArray(5)
	bA4.SetIndex(0, true)
	bA4.SetIndex(1, true)

	testCases := []struct {
		bA           *BitArray
		marshalledBA string
	}{
		{nil, `null`},
		{bA1, `null`},
		{bA2, `"_"`},
		{bA3, `"x"`},
		{bA4, `"xx___"`},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.bA.String(), func(t *testing.T) {
			bz, err := json.Marshal(tc.bA)
			require.NoError(t, err)

			assert.Equal(t, tc.marshalledBA, string(bz))

			var unmarshalledBA *BitArray
			err = json.Unmarshal(bz, &unmarshalledBA)
			require.NoError(t, err)

			if tc.bA == nil {
				require.Nil(t, unmarshalledBA)
			} else {
				require.NotNil(t, unmarshalledBA)
				assert.EqualValues(t, tc.bA.Bits, unmarshalledBA.Bits)
				if assert.EqualValues(t, tc.bA.String(), unmarshalledBA.String()) {
					assert.EqualValues(t, tc.bA.Elems, unmarshalledBA.Elems)
				}
			}
		})
	}
}

func TestBitArrayProtoBuf(t *testing.T) {
	testCases := []struct {
		msg     string
		bA1     *BitArray
		expPass bool
	}{
		{"success empty", &BitArray{}, true},
		{"success", NewBitArray(1), true},
		{"success", NewBitArray(2), true},
		{"negative", NewBitArray(-1), false},
	}
	for _, tc := range testCases {
		protoBA := tc.bA1.ToProto()
		ba := new(BitArray)
		ba.FromProto(protoBA)
		if tc.expPass {
			require.Equal(t, tc.bA1, ba, tc.msg)
		} else {
			require.NotEqual(t, tc.bA1, ba, tc.msg)
		}
	}
}

func TestAddBitArray(t *testing.T) {
	// Helper function to create a BitArray with specified bits
	createBitArray := func(bits int, elems []uint64) *BitArray {
		return &BitArray{
			Bits:  bits,
			Elems: elems,
		}
	}

	tests := []struct {
		name     string
		bA       *BitArray
		b        *BitArray
		expected *BitArray
	}{
		{
			name:     "same length",
			bA:       createBitArray(8, []uint64{0b10101010}),
			b:        createBitArray(8, []uint64{0b01010101}),
			expected: createBitArray(8, []uint64{0b11111111}),
		},
		{
			name:     "b is longer than bA",
			bA:       createBitArray(8, []uint64{0b1010}),
			b:        createBitArray(16, []uint64{0b0101, 0b1111}),
			expected: createBitArray(16, []uint64{0b1111, 0b1111}),
		},
		{
			name:     "bA is longer than b",
			bA:       createBitArray(16, []uint64{0b1010, 0b0001}),
			b:        createBitArray(8, []uint64{0b0101}),
			expected: createBitArray(16, []uint64{0b1111, 0b0001}),
		},
		{
			name:     "both empty",
			bA:       createBitArray(0, []uint64{}),
			b:        createBitArray(0, []uint64{}),
			expected: createBitArray(0, []uint64{}),
		},
		{
			name:     "bA empty, b non-empty",
			bA:       createBitArray(0, []uint64{}),
			b:        createBitArray(8, []uint64{0b1111}),
			expected: createBitArray(8, []uint64{0b1111}),
		},
		{
			name:     "b empty, bA non-empty",
			bA:       createBitArray(8, []uint64{0b1111}),
			b:        createBitArray(0, []uint64{}),
			expected: createBitArray(8, []uint64{0b1111}),
		},
		{
			name:     "bA with nil elements, b non-empty",
			bA:       createBitArray(8, nil),
			b:        createBitArray(8, []uint64{0b1010}),
			expected: createBitArray(8, []uint64{0b1010}),
		},
		{
			name:     "self addition (should remain the same)",
			bA:       createBitArray(8, []uint64{0b1010}),
			b:        createBitArray(8, []uint64{0b1010}),
			expected: createBitArray(8, []uint64{0b1010}),
		},
		{
			name:     "different bit lengths, same element count",
			bA:       createBitArray(10, []uint64{0b10101010}),
			b:        createBitArray(8, []uint64{0b01010101}),
			expected: createBitArray(10, []uint64{0b11111111}),
		},
		{
			name:     "larger arrays",
			bA:       createBitArray(64, []uint64{0xAAAAAAAA, 0x55555555}),
			b:        createBitArray(64, []uint64{0x55555555, 0xAAAAAAAA}),
			expected: createBitArray(64, []uint64{0xFFFFFFFF, 0xFFFFFFFF}),
		},
		{
			name:     "bA shorter than b with multiple elements",
			bA:       createBitArray(32, []uint64{0x000000FF}),
			b:        createBitArray(64, []uint64{0xFF000000, 0xFFFFFFFF}),
			expected: createBitArray(64, []uint64{0xFF0000FF, 0xFFFFFFFF}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.bA.AddBitArray(tt.b)

			// Check if bit count is correct
			if tt.bA.Bits != tt.expected.Bits {
				t.Errorf("expected bits %d, got %d", tt.expected.Bits, tt.bA.Bits)
			}

			// Check if elements match expected result
			if len(tt.bA.Elems) != len(tt.expected.Elems) {
				t.Errorf("expected elems length %d, got %d", len(tt.expected.Elems), len(tt.bA.Elems))
			}
			for i := range tt.expected.Elems {
				if i < len(tt.bA.Elems) && tt.bA.Elems[i] != tt.expected.Elems[i] {
					t.Errorf("expected elems %v, got %v", tt.expected.Elems, tt.bA.Elems)
					break
				}
			}
		})
	}
}

// Tests that UnmarshalJSON doesn't crash when no bits are passed into the JSON.
// See issue https://github.com/cometbft/cometbft/issues/2658
func TestUnmarshalJSONDoesntCrashOnZeroBits(t *testing.T) {
	type indexCorpus struct {
		BitArray *BitArray `json:"ba"`
		Index    int       `json:"i"`
	}

	ic := new(indexCorpus)
	blob := []byte(`{"BA":""}`)
	err := json.Unmarshal(blob, ic)
	require.NoError(t, err)
	require.Equal(t, ic.BitArray, &BitArray{Bits: 0, Elems: nil})
}

func BenchmarkPickRandomBitArray(b *testing.B) {
	// A random 150 bit string to use as the benchmark bit array
	benchmarkBitArrayStr := "_______xx__xxx_xx__x_xx_x_x_x__x_x_x_xx__xx__xxx__xx_x_xxx_x__xx____x____xx__xx____x_x__x_____xx_xx_xxxxxxx__xx_x_xxxx_x___x_xxxxx_xx__xxxx_xx_x___x_x"
	var bitArr *BitArray
	err := json.Unmarshal([]byte(`"`+benchmarkBitArrayStr+`"`), &bitArr)
	require.NoError(b, err)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bitArr.PickRandom()
	}
}

func TestBitArray_Fill(t *testing.T) {
	t.Run("fill sets all bits true", func(t *testing.T) {
		bA := NewBitArray(5)
		require.NotNil(t, bA)
		// Initially, all bits are false
		for i := 0; i < bA.Size(); i++ {
			require.False(t, bA.GetIndex(i))
		}

		bA.Fill()
		// Now all bits should be true.
		for i := 0; i < bA.Size(); i++ {
			require.True(t, bA.GetIndex(i), "bit at index %d should be true after Fill()", i)
		}
		// Also verify IsFull() returns true.
		require.True(t, bA.IsFull(), "bit array should be full after Fill()")
	})

	t.Run("partial set then fill", func(t *testing.T) {
		bA := NewBitArray(5)
		bA.SetIndex(1, true)
		bA.SetIndex(3, true)
		// Only indexes 1 and 3 are true
		require.True(t, bA.GetIndex(1))
		require.True(t, bA.GetIndex(3))
		require.False(t, bA.GetIndex(0))
		require.False(t, bA.GetIndex(2))
		require.False(t, bA.GetIndex(4))

		bA.Fill()
		// Fill should now set all bits to true
		for i := 0; i < bA.Size(); i++ {
			require.True(t, bA.GetIndex(i), "bit at index %d should be true after Fill()", i)
		}
		require.True(t, bA.IsFull())
	})
}
