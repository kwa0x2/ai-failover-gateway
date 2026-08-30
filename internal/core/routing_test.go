package core_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/outbound/mock"
	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// discardLogger keeps test output readable; flip to os.Stdout when debugging.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fastConfig shrinks every delay so the suite runs in milliseconds.
func fastConfig() core.RouteConfig {
	cfg := core.DefaultRouteConfig()
	cfg.InitialBackoff = time.Millisecond
	cfg.MaxBackoff = 2 * time.Millisecond
	cfg.AttemptTimeout = 500 * time.Millisecond
	cfg.Breaker = core.BreakerConfig{
		MinRequests:      2,
		FailureRatio:     0.5,
		OpenTimeout:      50 * time.Millisecond,
		HalfOpenMaxCalls: 1,
	}
	return cfg
}

func aRequest() core.Request {
	return core.Request{Messages: []core.Message{{Role: "user", Content: "hi"}}}
}

// The happy path must not touch the fallback at all.
func TestPrimarySucceeds_NoFailover(t *testing.T) {
	primary := mock.New("primary", "from primary")
	secondary := mock.New("secondary", "from secondary")

	r := core.New(discardLogger(), nil, fastConfig(), primary, secondary)

	res, err := r.Complete(context.Background(), aRequest())
	require.NoError(t, err)
	assert.Equal(t, "primary", res.Provider)
	assert.Equal(t, 0, res.Failovers)
	assert.Equal(t, 0, secondary.Calls(), "fallback must stay untouched")
}

// A transient blip is absorbed by retry on the SAME provider — no failover.
func TestRetry_RecoversOnSameProvider(t *testing.T) {
	primary := mock.NewScripted("primary",
		mock.Behaviour{Err: mock.Retryable("primary", 503, "service unavailable")},
		mock.Behaviour{Err: mock.Retryable("primary", 503, "service unavailable")},
		mock.Behaviour{Text: "recovered"},
	)
	secondary := mock.New("secondary", "from secondary")

	r := core.New(discardLogger(), nil, fastConfig(), primary, secondary)

	res, err := r.Complete(context.Background(), aRequest())
	require.NoError(t, err)
	assert.Equal(t, "primary", res.Provider)
	assert.Equal(t, "recovered", res.Response.Text)
	assert.Equal(t, 3, primary.Calls(), "two retries, then success")
	assert.Equal(t, 0, secondary.Calls())
}

// Once retries are exhausted the fallback takes over.
func TestFailover_WhenRetriesExhausted(t *testing.T) {
	primary := mock.NewScripted("primary",
		mock.Behaviour{Err: mock.Retryable("primary", 503, "down")})
	secondary := mock.New("secondary", "from secondary")

	cfg := fastConfig()
	r := core.New(discardLogger(), nil, cfg, primary, secondary)

	res, err := r.Complete(context.Background(), aRequest())
	require.NoError(t, err)
	assert.Equal(t, "secondary", res.Provider)
	assert.Equal(t, 1, res.Failovers)
	assert.Equal(t, cfg.MaxAttempts, primary.Calls(), "primary tried MaxAttempts times")
}

// A 400 is the caller's fault: retrying wastes quota and failing over just
// reproduces it somewhere else.
func TestPermanentError_NotRetriedAndNotFailedOver(t *testing.T) {
	primary := mock.NewScripted("primary",
		mock.Behaviour{Err: mock.Permanent("primary", 400, "malformed request")})
	secondary := mock.New("secondary", "from secondary")

	r := core.New(discardLogger(), nil, fastConfig(), primary, secondary)

	_, err := r.Complete(context.Background(), aRequest())
	require.Error(t, err)
	assert.False(t, core.IsRetryable(err))
	assert.Equal(t, 1, primary.Calls(), "permanent errors are never retried")
	assert.Equal(t, 0, secondary.Calls(), "and never failed over")
}

func TestAllProvidersDown(t *testing.T) {
	primary := mock.NewScripted("primary", mock.Behaviour{Err: mock.Retryable("primary", 503, "down")})
	secondary := mock.NewScripted("secondary", mock.Behaviour{Err: mock.Retryable("secondary", 500, "down")})

	r := core.New(discardLogger(), nil, fastConfig(), primary, secondary)

	_, err := r.Complete(context.Background(), aRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNoHealthyProvider, "callers can detect this with errors.Is")
}

// A provider that hangs past AttemptTimeout is abandoned and retried.
func TestAttemptTimeout_IsRetried(t *testing.T) {
	cfg := fastConfig()
	cfg.AttemptTimeout = 20 * time.Millisecond

	primary := mock.NewScripted("primary",
		mock.Behaviour{Delay: 200 * time.Millisecond, Text: "too slow"},
		mock.Behaviour{Text: "fast enough"},
	)

	r := core.New(discardLogger(), nil, cfg, primary)

	res, err := r.Complete(context.Background(), aRequest())
	require.NoError(t, err)
	assert.Equal(t, "fast enough", res.Response.Text)
	assert.Equal(t, 2, primary.Calls())
}

// A dead caller context stops the chain instead of burning the fallback.
func TestCallerCancellation_StopsImmediately(t *testing.T) {
	primary := mock.NewScripted("primary", mock.Behaviour{Delay: time.Second, Text: "never"})
	secondary := mock.New("secondary", "from secondary")

	r := core.New(discardLogger(), nil, fastConfig(), primary, secondary)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := r.Complete(ctx, aRequest())
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.Equal(t, 0, secondary.Calls(), "no failover on a dead context")
}

// Once the circuit opens, the sick provider is not called at all — that is
// where the wasted latency disappears.
func TestBreaker_OpensAndStopsCallingProvider(t *testing.T) {
	primary := mock.NewScripted("primary",
		mock.Behaviour{Err: mock.Retryable("primary", 503, "down")})
	secondary := mock.New("secondary", "from secondary")

	cfg := fastConfig()
	r := core.New(discardLogger(), nil, cfg, primary, secondary)

	// Drive enough logical failures to satisfy MinRequests and trip the circuit.
	for range int(cfg.Breaker.MinRequests) {
		res, err := r.Complete(context.Background(), aRequest())
		require.NoError(t, err)
		require.Equal(t, "secondary", res.Provider)
	}

	callsBeforeOpen := primary.Calls()

	res, err := r.Complete(context.Background(), aRequest())
	require.NoError(t, err)
	assert.Equal(t, "secondary", res.Provider)
	assert.Equal(t, callsBeforeOpen, primary.Calls(),
		"an open circuit must short-circuit: no new calls to the sick provider")
}

// After the open window elapses, one probe is allowed through; if it succeeds
// the primary is back in service.
func TestBreaker_HalfOpenRecovers(t *testing.T) {
	primary := mock.NewScripted("primary",
		mock.Behaviour{Err: mock.Retryable("primary", 503, "down")})
	secondary := mock.New("secondary", "from secondary")

	cfg := fastConfig()
	r := core.New(discardLogger(), nil, cfg, primary, secondary)

	for range int(cfg.Breaker.MinRequests) + 1 {
		_, err := r.Complete(context.Background(), aRequest())
		require.NoError(t, err)
	}

	// The provider heals and the open window expires.
	primary.SetScript(mock.Behaviour{Text: "healthy again"})
	time.Sleep(cfg.Breaker.OpenTimeout + 20*time.Millisecond)

	res, err := r.Complete(context.Background(), aRequest())
	require.NoError(t, err)
	assert.Equal(t, "primary", res.Provider, "the half-open probe should let it back in")
	assert.Equal(t, "healthy again", res.Response.Text)
}

// The one that separates a toy breaker from a correct one: a flood of
// malformed requests must NOT take a healthy provider offline.
func TestBreaker_PermanentErrorsDoNotTrip(t *testing.T) {
	primary := mock.NewScripted("primary",
		mock.Behaviour{Err: mock.Permanent("primary", 400, "malformed request")})

	cfg := fastConfig()
	r := core.New(discardLogger(), nil, cfg, primary)

	const requests = 10
	for range requests {
		_, err := r.Complete(context.Background(), aRequest())
		require.Error(t, err)
		require.False(t, core.IsRetryable(err))
	}

	// Still being called on every request => the circuit never opened.
	assert.Equal(t, requests, primary.Calls(),
		"client errors say nothing about provider health and must not trip the breaker")
}

func TestEmptyRouter(t *testing.T) {
	r := core.New(discardLogger(), nil, fastConfig())
	_, err := r.Complete(context.Background(), aRequest())
	assert.ErrorIs(t, err, core.ErrNoHealthyProvider)
}
