package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/sony/gobreaker/v2"
)

// BreakerConfig tunes one provider's circuit breaker.
type BreakerConfig struct {
	MinRequests      uint32
	FailureRatio     float64
	OpenTimeout      time.Duration
	HalfOpenMaxCalls uint32
	Interval         time.Duration
}

// RouteConfig tunes how one provider is called.
type RouteConfig struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	AttemptTimeout time.Duration
	Breaker        BreakerConfig
}

// DefaultRouteConfig returns sane starting values.
func DefaultRouteConfig() RouteConfig {
	return RouteConfig{
		MaxAttempts:    3,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
		AttemptTimeout: 30 * time.Second,
		Breaker: BreakerConfig{
			MinRequests:      5,
			FailureRatio:     0.5,
			OpenTimeout:      15 * time.Second,
			HalfOpenMaxCalls: 1,
			Interval:         60 * time.Second,
		},
	}
}

type route struct {
	p       Provider
	cfg     RouteConfig
	log     *slog.Logger
	rec     Recorder
	breaker *gobreaker.CircuitBreaker[*Response]
}

// newRoute gives each provider its own breaker: one vendor being down says
// nothing about another's health.
func newRoute(p Provider, cfg RouteConfig, log *slog.Logger, rec Recorder) *route {
	bc := cfg.Breaker

	settings := gobreaker.Settings{
		Name:        p.Name(),
		MaxRequests: bc.HalfOpenMaxCalls,
		Interval:    bc.Interval,
		Timeout:     bc.OpenTimeout,

		ReadyToTrip: func(c gobreaker.Counts) bool {
			if c.Requests < bc.MinRequests {
				return false
			}
			return float64(c.TotalFailures)/float64(c.Requests) >= bc.FailureRatio
		},

		IsSuccessful: func(err error) bool {
			return err == nil || !IsRetryable(err)
		},

		OnStateChange: func(name string, from, to gobreaker.State) {
			log.Warn("circuit breaker state change",
				slog.String("provider", name),
				slog.String("from", from.String()),
				slog.String("to", to.String()))
			rec.BreakerStateChanged(name, toBreakerState(to))
		},
	}

	return &route{
		p:       p,
		cfg:     cfg,
		log:     log,
		rec:     rec,
		breaker: gobreaker.NewCircuitBreaker[*Response](settings),
	}
}

func toBreakerState(s gobreaker.State) BreakerState {
	switch s {
	case gobreaker.StateClosed:
		return BreakerClosed
	case gobreaker.StateHalfOpen:
		return BreakerHalfOpen
	default:
		return BreakerOpen
	}
}

// Router tries its routes in order until one succeeds.
type Router struct {
	routes []*route
	log    *slog.Logger
	rec    Recorder
}

// New builds a router.
func New(log *slog.Logger, rec Recorder, cfg RouteConfig, providers ...Provider) *Router {
	if rec == nil {
		rec = NopRecorder{}
	}
	r := &Router{log: log, rec: rec}
	for _, p := range providers {
		r.routes = append(r.routes, newRoute(p, cfg, log, rec))
	}
	return r
}

// Complete runs the request through the provider chain.
func (r *Router) Complete(ctx context.Context, req Request) (*Result, error) {
	if len(r.routes) == 0 {
		return nil, ErrNoHealthyProvider
	}

	var lastErr error
	prev := ""

	for i, rt := range r.routes {
		if i > 0 {
			r.rec.Failover(prev, rt.p.Name())
		}
		prev = rt.p.Name()

		resp, err := rt.call(ctx, req)
		if err == nil {
			r.rec.TokensUsed(rt.p.Name(), resp.InputTokens, resp.OutputTokens)
			return &Result{Response: resp, Provider: rt.p.Name(), Failovers: i}, nil
		}

		if ctx.Err() != nil {
			return nil, fmt.Errorf("request cancelled during %s: %w", rt.p.Name(), ctx.Err())
		}

		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			r.rec.BreakerRejected(rt.p.Name())
			r.log.Warn("skipping provider, breaker not closed",
				slog.String("provider", rt.p.Name()))
			lastErr = err
			continue
		}

		if !IsRetryable(err) {
			r.log.Info("permanent provider error, not failing over",
				slog.String("provider", rt.p.Name()),
				slog.String("error", err.Error()))
			return nil, err
		}

		r.log.Error("provider exhausted, failing over",
			slog.String("provider", rt.p.Name()),
			slog.String("error", err.Error()))
		lastErr = err
	}

	return nil, fmt.Errorf("%w: last error: %w", ErrNoHealthyProvider, lastErr)
}

// call wraps the retry loop in the breaker, so every attempt for one request
// collapses into a single breaker outcome.
func (rt *route) call(ctx context.Context, req Request) (*Response, error) {
	return rt.breaker.Execute(func() (*Response, error) {
		return rt.withRetry(ctx, req)
	})
}

func (rt *route) withRetry(ctx context.Context, req Request) (*Response, error) {
	expo := backoff.NewExponentialBackOff()
	expo.InitialInterval = rt.cfg.InitialBackoff
	expo.MaxInterval = rt.cfg.MaxBackoff

	attempt := func() (*Response, error) {
		attemptCtx := ctx
		if rt.cfg.AttemptTimeout > 0 {
			var cancel context.CancelFunc
			attemptCtx, cancel = context.WithTimeout(ctx, rt.cfg.AttemptTimeout)
			defer cancel()
		}

		start := time.Now()
		resp, err := rt.p.Complete(attemptCtx, req)
		rt.rec.ProviderLatency(rt.p.Name(), time.Since(start))

		switch {
		case err == nil:
			rt.rec.ProviderAttempt(rt.p.Name(), "success")
			return resp, nil
		case !IsRetryable(err):
			rt.rec.ProviderAttempt(rt.p.Name(), "permanent_error")
			return nil, backoff.Permanent(err)
		default:
			rt.rec.ProviderAttempt(rt.p.Name(), "retryable_error")
			return nil, err
		}
	}

	return backoff.Retry(ctx, attempt,
		backoff.WithBackOff(expo),
		backoff.WithMaxTries(uint(max(rt.cfg.MaxAttempts, 1))),
	)
}
