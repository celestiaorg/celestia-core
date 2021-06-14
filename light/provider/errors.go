package provider

import (
	"errors"
	"fmt"
)

var (
	// ErrLightBlockNotFound is returned when a provider can't find the
	// requested header.
	ErrLightBlockNotFound = errors.New("light block not found")

	// ErrDAHeaderNotFound is returned when a provider can't find the
	// requested DataAvailabilityHeader.
	ErrDAHeaderNotFound = errors.New("data availability header not found")
	// ErrNoResponse is returned if the provider doesn't respond to the
	// request in a given time
	ErrNoResponse = errors.New("client failed to respond")
)

// ErrBadLightBlock is returned when a provider returns an invalid
// light block.
type ErrBadLightBlock struct {
	Reason error
}

func (e ErrBadLightBlock) Error() string {
	return fmt.Sprintf("client provided bad signed header: %s", e.Reason.Error())
}
