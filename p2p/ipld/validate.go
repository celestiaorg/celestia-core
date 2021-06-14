package ipld

import (
	"context"
	"errors"
	"fmt"
	"time"

	ipld "github.com/ipfs/go-ipld-format"
	"github.com/lazyledger/nmt/namespace"

	"github.com/lazyledger/lazyledger-core/types"
)

// ValidationTimeout specifies timeout for DA validation during which data have to be found on the network,
// otherwise ErrValidationFailed is thrown.
// TODO: github.com/lazyledger/lazyledger-core/issues/280
const ValidationTimeout = 10 * time.Minute

// ErrValidationFailed is returned whenever DA validation fails
var ErrValidationFailed = errors.New("validation failed")

// ValidateAvailability randomly samples the block data that composes a provided
// data availability header. It only returns when all samples have been completed
// successfully. `onLeafValidity` is called on each sampled leaf after
// retrieval. Implements the protocol described in
// https://fc21.ifca.ai/papers/83.pdf.
func ValidateAvailability(
	ctx context.Context,
	dag ipld.NodeGetter,
	dah *types.DataAvailabilityHeader,
	numSamples int,
	onLeafValidity func(namespace.PrefixedData8),
) error {
	ctx, cancel := context.WithTimeout(ctx, ValidationTimeout)
	defer cancel()

	squareWidth := uint32(len(dah.ColumnRoots))
	samples := SampleSquare(squareWidth, numSamples)

	type res struct {
		data []byte
		err  error
	}
	resCh := make(chan res, len(samples))
	for _, s := range samples {
		go func(s Sample) {
			root, leaf, err := s.Leaf(dah)
			if err != nil {
				select {
				case resCh <- res{err: err}:
				case <-ctx.Done():
				}
				return
			}

			data, err := GetLeafData(ctx, root, leaf, squareWidth, dag)
			select {
			case resCh <- res{data: data, err: err}:
			case <-ctx.Done():
			}
		}(s)
	}

	for range samples {
		select {
		case r := <-resCh:
			if r.err != nil {
				if errors.Is(r.err, ipld.ErrNotFound) {
					return ErrValidationFailed
				}

				return r.err
			}

			// the fact that we read the data, already gives us Merkle proof,
			// thus the data availability is successfully validated :)
			onLeafValidity(r.data)
		case <-ctx.Done():
			err := ctx.Err()
			if err == context.DeadlineExceeded {
				return fmt.Errorf("%v: %w", ErrValidationFailed, err)
			}

			return err
		}
	}

	return nil
}
