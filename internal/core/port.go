package core

import (
	"context"
	"time"
)

// Provider is an outbound port: one AI backend.
//
// Implementations translate their vendor's protocol and classify its errors.
// Retry, backoff, circuit breaking and failover are policy and belong to the
// router, so they stay consistent across vendors.
type Provider interface {
	Name() string
	// Complete performs exactly one attempt and must not retry internally.
	Complete(ctx context.Context, req Request) (*Response, error)
}

// BreakerState mirrors the circuit breaker's state without leaking the breaker
// library's type out of core.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerHalfOpen
	BreakerOpen
)

// Recorder is an outbound port for telemetry. Core reports what happened; how
// it is stored is an adapter's concern.
type Recorder interface {
	ProviderAttempt(provider, outcome string)
	ProviderLatency(provider string, d time.Duration)
	BreakerStateChanged(provider string, state BreakerState)
	BreakerRejected(provider string)
	Failover(from, to string)
	TokensUsed(provider string, input, output int)
}

// NopRecorder discards everything, so the router never needs a nil check.
type NopRecorder struct{}

var _ Recorder = NopRecorder{}

func (NopRecorder) ProviderAttempt(string, string)           {}
func (NopRecorder) ProviderLatency(string, time.Duration)    {}
func (NopRecorder) BreakerStateChanged(string, BreakerState) {}
func (NopRecorder) BreakerRejected(string)                   {}
func (NopRecorder) Failover(string, string)                  {}
func (NopRecorder) TokensUsed(string, int, int)              {}
