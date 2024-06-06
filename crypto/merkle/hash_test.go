package merkle

import (
	"testing"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/stretchr/testify/require"
)

func TestHash(t *testing.T) {
	leaf := cmtrand.Bytes(tmhash.Size)
	left := cmtrand.Bytes(tmhash.Size)
	right := cmtrand.Bytes(tmhash.Size)

	require.Equal(t,
		leafHash(leaf),
		leafHashOpt(tmhash.New(), leaf),
	)
	require.Equal(t,
		innerHash(left, right),
		innerHashOpt(tmhash.New(), left, right),
	)
	require.NotEqual(t,
		innerHash(right, left),
		innerHashOpt(tmhash.New(), left, right),
	)
}
