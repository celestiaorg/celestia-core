package types

import "testing"

func TestInfoReservedByte(t *testing.T) {
	type testCase struct {
		version        uint8
		isMessageStart bool
	}
	tests := []testCase{
		{0, false},
		{1, false},
		{2, false},
		{127, false},
		{0, true},
		{1, true},
		{2, true},
		{127, true},
	}

	for _, test := range tests {
		irb, err := NewInfoReservedByte(test.version, test.isMessageStart)
		if err != nil {
			t.Errorf("got %v want no error", err)
		}
		if got := irb.Version(); got != test.version {
			t.Errorf("got version %v want %v", got, test.version)
		}
		if got := irb.IsMessageStart(); got != test.isMessageStart {
			t.Errorf("got isMessageStart %v want %v", got, test.isMessageStart)
		}
	}
}

func TestNewInfoReservedByte(t *testing.T) {
	_, err := NewInfoReservedByte(128, false)
	if err == nil {
		t.Errorf("got nil but want error when version > 127")
	}
}
