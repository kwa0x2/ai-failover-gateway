package bedrock

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
		{"throttled", &anthropic.Error{StatusCode: 429}, true, 429},
		{"server error", &anthropic.Error{StatusCode: 500}, true, 500},
		{"model overloaded", &anthropic.Error{StatusCode: 529}, true, 529},
		{"request timeout", &anthropic.Error{StatusCode: 408}, true, 408},

		{"malformed request", &anthropic.Error{StatusCode: 400}, false, 400},
		{"bad sigv4 signature", &anthropic.Error{StatusCode: 401}, false, 401},
		{"model access not granted", &anthropic.Error{StatusCode: 403}, false, 403},
		{"model not in region", &anthropic.Error{StatusCode: 404}, false, 404},
		{"prompt too large", &anthropic.Error{StatusCode: 413}, false, 413},

		{"network failure", errors.New("dial tcp: connection refused"), true, 0},
		{"error the SDK did not wrap", errors.New("ExpiredTokenException"), true, 0},
		{"caller deadline", context.DeadlineExceeded, true, 0},
		{"caller cancelled", context.Canceled, true, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateError("bedrock", tc.err)
			require.Error(t, got)

			var cErr *core.Error
			require.ErrorAs(t, got, &cErr)
			assert.Equal(t, "bedrock", cErr.Provider)
			assert.Equal(t, tc.wantStatus, cErr.StatusCode)
			assert.Equal(t, tc.wantRetryable, cErr.Retryable)
			assert.Equal(t, tc.wantRetryable, core.IsRetryable(got),
				"the router must read the same classification")
		})
	}
}

func TestTranslateError_NilStaysNil(t *testing.T) {
	assert.NoError(t, translateError("bedrock", nil))
}

func TestTranslateError_PreservesCause(t *testing.T) {
	cause := context.DeadlineExceeded
	got := translateError("bedrock", fmt.Errorf("request failed: %w", cause))

	assert.ErrorIs(t, got, cause)
}

// TestRetryableStatus_BedrockSpecifics pins the classifications a Bedrock
// operator is most likely to hit.
func TestRetryableStatus_BedrockSpecifics(t *testing.T) {
	assert.False(t, retryableStatus(403), "model access not granted in this region")
	assert.False(t, retryableStatus(404), "model id wrong or not available in region")
	assert.True(t, retryableStatus(429), "throttling is transient")
	assert.True(t, retryableStatus(529), "model overloaded is transient")
	assert.True(t, retryableStatus(0), "no HTTP response at all defaults to retryable")
}
