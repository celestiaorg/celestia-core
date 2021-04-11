package ipld

import (
	"context"
	"errors"
	"time"

	format "github.com/ipfs/go-ipld-format"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/lazyledger/nmt/namespace"

	"github.com/lazyledger/lazyledger-core/types"
)

// ValidationTimeout specifies timeout for DA validation during which data have to be found on the network,
// otherwise ErrValidationFailed is thrown.
// TODO: github.com/lazyledger/lazyledger-core/issues/280
const ValidationTimeout = time.Minute

// ErrValidationFailed is returned whenever DA validation fails
var ErrValidationFailed = errors.New("validation failed")

// ValidateAvailability implements the protocol described in https://fc21.ifca.ai/papers/83.pdf.
// Specifically all steps of the protocol described in section
// _5.2 Random Sampling and Network Block Recovery_ are carried out.
//
// In more detail it will first create numSamples random unique coordinates.
// Then, it will ask the network for the leaf data corresponding to these coordinates.
// Additionally to the number of requests, the caller can pass in a callback,
// which will be called on for each retrieved leaf with a verified Merkle proof.
//
// Among other use-cases, the callback can be useful to monitoring (progress), or,
// to process the leaf data the moment it was validated.
// The context can be used to provide a timeout.
// TODO: Should there be a constant = lower bound for #samples
func ValidateAvailability(
	ctx context.Context,
	api coreiface.CoreAPI,
	dah *types.DataAvailabilityHeader,
	numSamples int,
	onLeafValidity func(namespace.PrefixedData8),
) error {
	// TODO(@Wondertan): Ensure data is fetched within one DAG session
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

			data, err := GetLeafData(ctx, root, leaf, squareWidth, api)
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
				if errors.Is(r.err, format.ErrNotFound) {
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
				return ErrValidationFailed
			}

			return err
		}
	}

	return nil
}
