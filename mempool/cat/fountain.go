package cat

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"

	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/types"
)

const maxFountainDataSymbols = 64

type fountainRow struct {
	coeff []uint64
	data  []byte
}

func buildFountainSymbols(txKey types.TxKey, chunks [][]byte, chunkSize, parityCount int) [][]byte {
	if chunkSize <= 0 || len(chunks) == 0 || len(chunks) > maxFountainDataSymbols {
		return nil
	}
	if parityCount < 0 {
		parityCount = 0
	}
	symbols := make([][]byte, len(chunks)+parityCount)
	for i, chunk := range chunks {
		symbol := make([]byte, chunkSize)
		copy(symbol, chunk)
		symbols[i] = symbol
	}
	for i := 0; i < parityCount; i++ {
		index := uint32(len(chunks) + i)
		symbol := make([]byte, chunkSize)
		for chunkIndex := range chunks {
			if fountainHasCoefficient(txKey, index, chunkIndex, len(chunks)) {
				xorBytes(symbol, symbols[chunkIndex])
			}
		}
		symbols[len(chunks)+i] = symbol
	}
	return symbols
}

func fountainSymbolCoefficients(txKey types.TxKey, symbolIndex uint32, dataCount int) []uint64 {
	coeff := make([]uint64, (dataCount+63)/64)
	if dataCount == 0 {
		return coeff
	}
	if int(symbolIndex) < dataCount {
		setCoeffBit(coeff, int(symbolIndex))
		return coeff
	}
	for chunkIndex := 0; chunkIndex < dataCount; chunkIndex++ {
		if fountainHasCoefficient(txKey, symbolIndex, chunkIndex, dataCount) {
			setCoeffBit(coeff, chunkIndex)
		}
	}
	return coeff
}

func fountainHasCoefficient(txKey types.TxKey, symbolIndex uint32, chunkIndex, dataCount int) bool {
	if dataCount <= 1 {
		return true
	}
	var seed [tmhash.Size + 8]byte
	copy(seed[:], txKey[:])
	binary.BigEndian.PutUint32(seed[tmhash.Size:], symbolIndex)
	binary.BigEndian.PutUint32(seed[tmhash.Size+4:], uint32(chunkIndex))
	hash := sha256.Sum256(seed[:])

	// Always include one deterministic pivot so every repair symbol is non-empty,
	// then add roughly half of the remaining chunks. The pivot rotation prevents
	// repeated low-degree repair symbols from collapsing onto the same chunk.
	pivot := int(symbolIndex) % dataCount
	return chunkIndex == pivot || hash[0]&1 == 1
}

func tryDecodeFountain(txKey types.TxKey, symbols [][]byte, received []bool, chunkSize, dataCount int) ([][]byte, bool) {
	if dataCount <= 0 || chunkSize <= 0 || len(symbols) != len(received) || dataCount > maxFountainDataSymbols {
		return nil, false
	}
	pivots := make([]*fountainRow, dataCount)
	rank := 0
	for symbolIndex, data := range symbols {
		if !received[symbolIndex] || len(data) != chunkSize {
			continue
		}
		coeff := fountainSymbolCoefficients(txKey, uint32(symbolIndex), dataCount)
		rowData := append([]byte(nil), data...)
		for {
			pivot := firstCoeffBit(coeff)
			if pivot < 0 {
				break
			}
			if pivots[pivot] == nil {
				pivots[pivot] = &fountainRow{coeff: coeff, data: rowData}
				rank++
				break
			}
			xorWords(coeff, pivots[pivot].coeff)
			xorBytes(rowData, pivots[pivot].data)
		}
		if rank == dataCount {
			break
		}
	}
	if rank < dataCount {
		return nil, false
	}

	decoded := make([][]byte, dataCount)
	for pivot := dataCount - 1; pivot >= 0; pivot-- {
		row := pivots[pivot]
		if row == nil {
			return nil, false
		}
		data := append([]byte(nil), row.data...)
		for bitIndex := pivot + 1; bitIndex < dataCount; bitIndex++ {
			if hasCoeffBit(row.coeff, bitIndex) {
				if decoded[bitIndex] == nil {
					return nil, false
				}
				xorBytes(data, decoded[bitIndex])
			}
		}
		decoded[pivot] = data
	}
	return decoded, true
}

func setCoeffBit(words []uint64, bitIndex int) {
	words[bitIndex/64] |= uint64(1) << uint(bitIndex%64)
}

func hasCoeffBit(words []uint64, bitIndex int) bool {
	return words[bitIndex/64]&(uint64(1)<<uint(bitIndex%64)) != 0
}

func firstCoeffBit(words []uint64) int {
	for wordIndex, word := range words {
		if word != 0 {
			return wordIndex*64 + bits.TrailingZeros64(word)
		}
	}
	return -1
}

func xorWords(dst, src []uint64) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

func xorBytes(dst, src []byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}
