package cat

import "fmt"

type BitArray struct {
	staticSize uint64
	elements   []byte
}

func NewBitArray(size uint64) *BitArray {
	return &BitArray{
		staticSize: size,
		elements:   make([]byte, size/8+1),
	}
}

func NewBitArrayFromBytes(size uint64, bytes []byte) (*BitArray, error) {
	if int(size) > len(bytes)*8 {
		return nil, fmt.Errorf("incorrect format: size larger than byte length")
	}
	if int(size) < (len(bytes)-1)*8 {
		return nil, fmt.Errorf("incorrect format: size far smaller than byte length")
	}
	return &BitArray{
		staticSize: size,
		elements:   bytes,
	}, nil
}

func (ba *BitArray) Size() int {
	return int(ba.staticSize)
}

func (ba *BitArray) Set(index uint64) {
	ba.elements[index/8] |= 1 << (index % 8)
}

func (ba *BitArray) Get(index uint64) bool {
	return ba.elements[index/8]&(1<<(index%8)) != 0
}

func (ba *BitArray) Bytes() []byte {
	return ba.elements
}
