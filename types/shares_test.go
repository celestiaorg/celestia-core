package types

import (
	"reflect"
	"testing"
)

func Test_zeroPadIfNecessary(t *testing.T) {
	type args struct {
		share []byte
		width int
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		{ "pad", args{[]byte{1,2,3},  6}, []byte{1,2,3,0,0,0}},
		{ "not necessary (equal to shareSize)", args{[]byte{1,2,3},  3}, []byte{1,2,3}},
		{ "not necessary (greater shareSize)", args{[]byte{1,2,3},  2}, []byte{1,2,3}},
	}
		for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := zeroPadIfNecessary(tt.args.share, tt.args.width); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("zeroPadIfNecessary() = %v, want %v", got, tt.want)
			}
		})
	}
}
