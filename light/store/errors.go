package store

import (
	"errors"
	"fmt"
	"runtime/debug"
)

var (
	// ErrLightBlockNotFound is returned when a store does not have the
	// requested header.
	ErrLightBlockNotFound = errors.New("light block not found")
)

func NewNotFound() error {
	return fmt.Errorf("%s: %w", string(debug.Stack()), ErrLightBlockNotFound)
}
