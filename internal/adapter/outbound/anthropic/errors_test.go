package anthropic

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

func TestTranslateError(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		wantRetryable bool
		wantStatus    int
	}{
		{"rate limited", &anthropic.Error{StatusCode: 429}, true, 429},
		{"server error", &anthropic.Error{StatusCode: 500}, true, 500},
		{"overloaded", &anthropic.Error{StatusCode: 529}, true, 529},
		{"request timeout", &anthropic.Error{StatusCode: 408}, true, 408},

		{"malformed request", &anthropic.Error{StatusCode: 400}, false, 400},
		{"bad api key", &anthropic.Error{StatusCode: 401}, false, 401},
		{"forbidden", &anthropic.Error{StatusCode: 403}, false, 403},
		{"unknown model", &anthropic.Error{StatusCode: 404}, false, 404},
		{"prompt too large", &anthropic.Error{StatusCode: 413}, false, 413},

		{"network failure", errors.New("dial tcp: connection refused"), true, 0},
		{"caller deadline", context.DeadlineExceeded, true, 0},
		{"caller cancelled", context.Canceled, true, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateError("anthropic", tc.err)
			require.Error(t, got)

			var cErr *core.Error
			require.ErrorAs(t, got, &cErr)
			assert.Equal(t, "anthropic", cErr.Provider)
			assert.Equal(t, tc.wantStatus, cErr.StatusCode)
			assert.Equal(t, tc.wantRetryable, cErr.Retryable)
			assert.Equal(t, tc.wantRetryable, core.IsRetryable(got),
				"the router must read the same classification")
		})
	}
}

func TestTranslateError_NilStaysNil(t *testing.T) {
	assert.NoError(t, translateError("anthropic", nil))
}

// The original SDK error must stay reachable for logs and for errors.Is.
func TestTranslateError_PreservesCause(t *testing.T) {
	cause := context.DeadlineExceeded
	got := translateError("anthropic", fmt.Errorf("request failed: %w", cause))

	assert.ErrorIs(t, got, cause)
}
