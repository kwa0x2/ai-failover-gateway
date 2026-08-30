// Package app is the composition root: the one place that knows every other
// package and wires them together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/inbound/rest"
	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/outbound/mock"
	"github.com/kwa0x2/ai-failover-gateway/internal/config"
	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// Run builds everything and blocks until the process is asked to stop.
func Run() error {
	cfg := config.Load()
	log := newLogger(cfg.LogLevel)

	providers, err := buildProviders(cfg)
	if err != nil {
		return err
	}

	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name()
	}
	log.Info("starting gateway",
		slog.String("addr", cfg.Addr),
		slog.Any("providers", names))

	router := core.New(log, nil, routeConfig(cfg), providers...)
	server := rest.NewServer(router, log, cfg.RequestTimeout)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: server.Handler(),
		// Without this, a client can open a connection, send no headers and
		// hold a goroutine indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}

	return serve(srv, log, cfg.ShutdownTimeout)
}

// routeConfig maps flat config values onto the domain's policy type.
func routeConfig(cfg config.Config) core.RouteConfig {
	return core.RouteConfig{
		MaxAttempts:    cfg.MaxAttempts,
		InitialBackoff: cfg.InitialBackoff,
		MaxBackoff:     cfg.MaxBackoff,
		AttemptTimeout: cfg.AttemptTimeout,
		Breaker: core.BreakerConfig{
			MinRequests:      uint32(cfg.BreakerMinRequests),
			FailureRatio:     cfg.BreakerFailureRatio,
			OpenTimeout:      cfg.BreakerOpenTimeout,
			HalfOpenMaxCalls: 1,
			Interval:         cfg.BreakerInterval,
		},
	}
}

// buildProviders resolves configured names into adapters.
func buildProviders(cfg config.Config) ([]core.Provider, error) {
	var out []core.Provider
	for _, name := range cfg.Providers {
		switch name {
		case "mock":
			out = append(out, mock.New("mock", "hello from the mock provider"))
		case "mock-flaky":
			out = append(out, mock.NewScripted("mock-flaky",
				mock.Behaviour{Err: mock.Retryable("mock-flaky", 503, "simulated outage")},
				mock.Behaviour{Err: mock.Retryable("mock-flaky", 503, "simulated outage")},
				mock.Behaviour{Err: mock.Retryable("mock-flaky", 503, "simulated outage")},
				mock.Behaviour{Text: "hello from the recovered flaky provider"},
			))
		case "mock-dead":
			out = append(out, mock.NewScripted("mock-dead",
				mock.Behaviour{Err: mock.Retryable("mock-dead", 503, "permanently down")}))
		case "bedrock", "anthropic":
			return nil, fmt.Errorf("provider %q is not implemented yet", name)
		default:
			return nil, fmt.Errorf("unknown provider %q", name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no providers configured")
	}
	return out, nil
}

// serve runs the HTTP server and drains in-flight requests on SIGINT/SIGTERM,
// which is what makes a Kubernetes rollout drop no connections.
func serve(srv *http.Server, log *slog.Logger, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
