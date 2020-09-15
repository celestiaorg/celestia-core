package types

import (
	"github.com/lazyledger/nmt/namespace"
)

// TODO list:
// * pass in a way to extract / get the namespace ID of an element
// * data structures & algos:
//   - Iterator for each list in Block.Data (instead of passing in Data directly) ?
//   - define Share as pair (namespace, rawData)
//   - padding
//   - abstract away encoding (?)

type Share []byte

func makeShares(data Data, shareSize int, nameSpaceSize int) ([]Share, map[int]namespace.ID) {
	//	shares := make([]Share,0)
	//	shareIdxToNID := make(map[int]namespace.ID)
	//	shareIdx := 0
	//	for _, field := range data.FieldsAsList() {
	//		for _, element := range field {
	//			rawData := serialize(element) // data without nid
	//			if len(rawData) < shareSize {
	//				share := encode(0) || rawData
	//				share = pad(share, shareSize)
	//				shares = append(shares, share)
	//				shareIdxToNID[shareIdx] = nid(element)
	//				shareIdx++
	//			} else { // len(rawData) >= shareWithoutNidSize:
	//				nid := nid(element)
	//				splitIntoShares := ceil((len(rawData) + 1) / shareSize)
	//				firstShare := encode(splitIntoShares) || rawData[:shareSize-1]
	//				shares = append(shares, firstShare)
	//				shareIdxToNID[shareIdx] = nid
	//				shareIdx++
	//				rawData := rawData[:shareSize-1]
	//				for len(rawData) > 0 {
	//					share := rawData[:shareSize-1]
	//					share = pad(share, shareSize) // pad if necessary
	//					shares = append(shares, share)
	//					shareIdxToNID[shareIdx] = nid
	//					shareIdx++
	//				}
	//			}
	//		}
	//	}
	//	return shares
	panic("todo")
}
