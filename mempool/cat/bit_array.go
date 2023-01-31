package cat

import (
	"errors"
	"fmt"
)

type BitArray struct {
	staticSize int
	elements   []byte
}

func NewBitArray(size int) *BitArray {
	cap := size / 8
	if size%8 != 0 {
		cap++
	}
	return &BitArray{
		staticSize: size,
		elements:   make([]byte, cap),
	}
}

func NewBitArrayFromBytes(size int, bytes []byte) (*BitArray, error) {
	if int(size) > len(bytes)*8 {
		return nil, fmt.Errorf("incorrect format: size larger than byte length")
	}
	if int(size) <= (len(bytes)-1)*8 {
		return nil, fmt.Errorf("incorrect format: size smaller than byte length")
	}
	return &BitArray{
		staticSize: size,
		elements:   bytes,
	}, nil
}

func NewFullBitArray(size int) *BitArray {
	cap := size / 8
	if size%8 != 0 {
		cap++
	}
	elements := make([]byte, cap)
	for i := 0; i < cap; i++ {
		elements[i] = 0xff
	}
	return &BitArray{
		staticSize: size,
		elements:   elements,
	}
}

func (ba *BitArray) Size() int {
	return int(ba.staticSize)
}

func (ba *BitArray) Set(index int) error {
	if index < 0 {
		return errors.New("negative index")
	}
	if index >= ba.staticSize {
		return errors.New("index out of range")
	}
	ba.elements[index/8] |= 1 << (index % 8)
	return nil
}

func (ba *BitArray) Get(index int) bool {
	if index < 0 {
		return false
	}
	if index > ba.staticSize {
		return false
	}
	return ba.elements[index/8]&(1<<(index%8)) != 0
}

func (ba *BitArray) Bytes() []byte {
	return ba.elements
}

func (ba *BitArray) GetAll() []int {
	result := make([]int, 0, ba.staticSize)
	for i := 0; i < ba.staticSize; i++ {
		if ba.elements[i/8]&(1<<(i%8)) != 0 {
			result = append(result, i)
		}
	}
	return result
}
