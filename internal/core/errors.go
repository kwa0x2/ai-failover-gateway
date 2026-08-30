package core

import (
	"errors"
	"fmt"
)

// Error is a provider failure classified for the router. Adapters translate
// their SDK errors into this shape.
type Error struct {
	Provider   string
	StatusCode int
	// Retryable drives three separate decisions: whether to retry, whether the
	// circuit breaker counts it as a failure, and whether to fail over.
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider %s: status=%d retryable=%t: %v",
		e.Provider, e.StatusCode, e.Retryable, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsRetryable reports whether err is classified retryable. Unclassified errors
// default to retryable: an unknown transport failure is more likely transient
// than permanent.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Retryable
	}
	return true
}

// ErrNoHealthyProvider is returned when every provider was skipped or failed.
var ErrNoHealthyProvider = errors.New("no healthy provider available")
