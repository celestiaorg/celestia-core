package tests

import (
	"fmt"
	"github.com/cometbft/cometbft/crypto/merkle"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"runtime"
	"testing"
	"time"
)

func TestName(t *testing.T) {
	data := make([][]byte, 0)
	for i := 0; i < 2050; i++ {
		data = append(data, cmtrand.Bytes(int(types.BlockPartSizeBytes)))
	}
	s := time.Now()
	root, proofs := merkle.ParallelProofsFromLeafHashes(data)
	fmt.Println("proofs: ", time.Since(s))
	protoProof := proofs[2000].ToProto()
	fmt.Println(4000 * protoProof.Size())
	grp := errgroup.Group{}
	grp.SetLimit(runtime.NumCPU())
	s = time.Now()
	for i := 0; i < 2050; i++ {
		grp.Go(func() error {
			proofs[i].Verify(root, proofs[i].LeafHash)
			return nil
		})
	}
	err := grp.Wait()
	require.NoError(t, err)
	fmt.Println("proof verification: ", 2*time.Since(s))
}
