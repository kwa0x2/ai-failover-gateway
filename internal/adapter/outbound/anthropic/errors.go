package anthropic

import (
	"context"
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// translateError turns an SDK error into the classification the router needs.
func translateError(name string, err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Provider: name, Retryable: true, Err: err}
	}

	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return &core.Error{Provider: name, Retryable: true, Err: err}
	}

	return &core.Error{
		Provider:   name,
		StatusCode: apiErr.StatusCode,
		Retryable:  retryableStatus(apiErr.StatusCode),
		Err:        err,
	}
}

// retryableStatus decides whether trying the same provider again could work.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusConflict,
		http.StatusTooManyRequests:
		return true
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return false
	}
	return status >= http.StatusInternalServerError || status == 0
}
