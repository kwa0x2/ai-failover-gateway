package core

import (
	"errors"
	"fmt"
)

// Error is a provider failure classified for the router.
type Error struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider %s: status=%d retryable=%t: %v",
		e.Provider, e.StatusCode, e.Retryable, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsRetryable reports whether err is classified retryable.
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
