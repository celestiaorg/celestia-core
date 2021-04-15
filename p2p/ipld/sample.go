package ipld

import (
	"math/rand"

	"github.com/ipfs/go-cid"
	"github.com/lazyledger/nmt/namespace"

	crand "github.com/lazyledger/lazyledger-core/crypto"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"
	"github.com/lazyledger/lazyledger-core/types"
)

// Sample is a point in 2D space over square.
type Sample struct {
	Row, Col uint32

	// src defines the source for sampling, either from column(true) or row(false) root
	src bool
}

// SampleSquare randomly picks *num* unique points from arbitrary *width* square
// and returns them as samples.
func SampleSquare(squareWidth uint32, num int) []Sample {
	ss := newSquareSampler(squareWidth, num, crand.CRandSeed(8))
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
	if s.src {
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
	rand        *rand.Rand //nolint:gosec // as under we use cryptographically secure seed
	squareWidth uint32
	smpls       map[Sample]struct{}
}

func newSquareSampler(squareWidth uint32, expectedSamples int, seed int64) *squareSampler {
	return &squareSampler{
		rand:        rand.New(rand.NewSource(seed)), //nolint:gosec // as under we use cryptographically secure seed
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
			Row: uint32(ss.rand.Int31n(int32(ss.squareWidth))),
			Col: uint32(ss.rand.Int31n(int32(ss.squareWidth))),
			src: ss.rand.Intn(2) == 0,
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
