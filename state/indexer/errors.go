package indexer

import "errors"

// ErrTooManyResults is returned when the query matches more than the allowed limit.
var ErrTooManyResults = errors.New("search query too broad: narrow the query")

// IsLimitReached reports whether count has exceeded the configured
// limit. A zero or negative limit means unlimited.
func IsLimitReached(count, limit int) bool {
	return limit > 0 && count > limit
}
