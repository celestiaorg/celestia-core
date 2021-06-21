package ipld

import (
	"context"
	"testing"
	"time"

	mdutils "github.com/ipfs/go-merkledag/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO(@Wondertan): Add test to simulate ErrValidationFailed

func TestValidateAvailability(t *testing.T) {
	const (
		shares     = 16
		squareSize = 8 * 8
	)

	dag := mdutils.Mock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	blockData := RandNamespacedShares(t, squareSize)
	eds, err := PutData(ctx, blockData, dag)
	require.NoError(t, err)

	calls := 0
	err = ValidateAvailability(ctx, dag, MakeDataHeader(eds), shares, func(share NamespacedShare) {
		calls++
	})
	assert.NoError(t, err)
	assert.Equal(t, shares, calls)
}
