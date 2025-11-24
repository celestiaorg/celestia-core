package provider

import (
	"errors"
	"fmt"
	// Removed: "runtime/debug" as it should not be used in public error messages
)

// Standard, well-defined provider errors.
var (
	// ErrHeightTooHigh indicates the requested block height is beyond the provider's known state.
	// The light client should typically not penalize the provider for this.
	ErrHeightTooHigh = errors.New("height requested is too high")
	
	// ErrLightBlockNotFound means the provider does not have the requested header,
	// likely due to pruning. The provider should not be removed for this.
	ErrLightBlockNotFound = errors.New("light block not found")
	
	// ErrNoResponse indicates the provider failed to respond within the allocated time.
	ErrNoResponse = errors.New("client failed to respond")
)

// ErrBadLightBlock represents a structurally or cryptographically invalid light block 
// returned by the provider (e.g., bad signature, invalid fields).
type ErrBadLightBlock struct {
	// Reason holds the underlying specific error (e.g., tendermint/merkle validation error).
	Reason error
}

// Error implements the standard Go error interface.
// It also ensures proper error wrapping by leveraging the `%w` directive in Go 1.13+.
func (e ErrBadLightBlock) Error() string {
	// Use fmt.Errorf to implicitly support error wrapping when returning the error
	// from higher layers. However, since the struct is public, the original form
	// is kept clean, and we add an Unwrap method for explicit wrapping support.
	return fmt.Sprintf("client provided bad signed header: %s", e.Reason.Error())
}

// Unwrap allows errors.Is and errors.As to look up the chain of errors 
// and check the underlying reason. (Go best practice for custom error structs)
func (e ErrBadLightBlock) Unwrap() error {
	return e.Reason
}

// NewNotFound wraps ErrLightBlockNotFound with a contextual message.
// The use of debug.Stack() is removed as it exposes sensitive path information 
// and should only be used for internal logging/debugging, not in the error value itself.
func NewNotFound() error {
	// Use Go's standard wrapping mechanism (%w) for clarity and testability.
	return fmt.Errorf("lookup failed: %w", ErrLightBlockNotFound)
}
