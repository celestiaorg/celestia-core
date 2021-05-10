package ipld

import (
	crand "crypto/rand"
	"math/big"

	"github.com/ipfs/go-cid"
	"github.com/lazyledger/nmt/namespace"

	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
)

// Sample is a point in 2D space over square.
type Sample struct {
	Row, Col uint32
}

// SampleSquare randomly picks *num* unique points from arbitrary *width* square
// and returns them as samples.
func SampleSquare(squareWidth uint32, num int) []Sample {
	ss := newSquareSampler(squareWidth, num)
	ss.sample(num)
	return ss.samples()
}

// Leaf returns leaf info needed for retrieval using data provided with DAHeader.
func (s Sample) Leaf(dah *types.DataAvailabilityHeader) (cid.Cid, uint32, error) {
	var (
		leaf uint32
		root namespace.IntervalDigest
	)

	// spread leaves retrieval from both Row and Column roots
	if randUint32(2) == 0 {
		root = dah.ColumnRoots[s.Col]
		leaf = s.Row
	} else {
		root = dah.RowsRoots[s.Row]
		leaf = s.Col
	}

	rootCid, err := nodes.CidFromNamespacedSha256(root.Bytes())
	if err != nil {
		return cid.Undef, 0, err
	}

	return rootCid, leaf, nil
}

// Equals check whenever to samples are equal.
func (s Sample) Equals(to Sample) bool {
	return s.Row == to.Row && s.Col == to.Col
}

type squareSampler struct {
	squareWidth uint32
	smpls       map[Sample]struct{}
}

func newSquareSampler(squareWidth uint32, expectedSamples int) *squareSampler {
	return &squareSampler{
		squareWidth: squareWidth,
		smpls:       make(map[Sample]struct{}, expectedSamples),
	}
}

func (ss *squareSampler) sample(num int) {
	if uint32(num) > ss.squareWidth*ss.squareWidth {
		panic("number of samples must be less than square width")
	}

	done := 0
	for done < num {
		s := Sample{
			Row: randUint32(ss.squareWidth),
			Col: randUint32(ss.squareWidth),
		}

		if _, ok := ss.smpls[s]; ok {
			continue
		}

		done++
		ss.smpls[s] = struct{}{}
	}
}

func (ss *squareSampler) samples() []Sample {
	samples := make([]Sample, 0, len(ss.smpls))
	for s := range ss.smpls {
		samples = append(samples, s)
	}
	return samples
}

func randUint32(max uint32) uint32 {
	n, err := crand.Int(crand.Reader, big.NewInt(int64(max)))
	if err != nil {
		panic(err) // won't panic as rand.Reader is endless
	}

	return uint32(n.Int64())
}
