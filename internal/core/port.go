package core

import (
	"context"
	"time"
)

// Provider is an outbound port: one AI backend.
type Provider interface {
	Name() string
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

// Recorder is an outbound port for telemetry.
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
