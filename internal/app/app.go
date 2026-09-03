// Package app is the composition root: the one place that knows every other
// package and wires them together.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/inbound/rest"
	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/outbound/anthropic"
	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/outbound/bedrock"
	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/outbound/metrics"
	"github.com/kwa0x2/ai-failover-gateway/internal/adapter/outbound/mock"
	"github.com/kwa0x2/ai-failover-gateway/internal/config"
	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// Run builds everything and blocks until the process is asked to stop.
func Run() error {
	ctx := context.Background()

	if err := loadDotEnv(); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := newLogger(cfg.LogLevel)

	providers, err := buildProviders(ctx, cfg)
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

	metricsAdapter := metrics.New()
	for _, p := range providers {
		metricsAdapter.InitProvider(p.Name())
	}

	router := core.New(log, metricsAdapter, routeConfig(cfg), providers...)
	server := rest.NewServer(router, log, cfg.RequestTimeout, metricsAdapter)
	server.Mount("/metrics", metricsAdapter.Handler())

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return serve(srv, log, cfg.ShutdownTimeout)
}

// loadDotEnv reads a .env file if one is present.
func loadDotEnv() error {
	values, err := godotenv.Read()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("load .env: %w", err)
	}

	for key, val := range values {
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	return nil
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
func buildProviders(ctx context.Context, cfg config.Config) ([]core.Provider, error) {
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
		case "mock-slow":
			out = append(out, mock.NewScripted("mock-slow",
				mock.Behaviour{Delay: time.Hour}))
		case "mock-dead":
			out = append(out, mock.NewScripted("mock-dead",
				mock.Behaviour{Err: mock.Retryable("mock-dead", 503, "permanently down")}))
		case "bedrock":
			p, err := bedrock.New(ctx, bedrock.Config{
				Name:      "bedrock",
				Region:    cfg.BedrockRegion,
				Model:     cfg.BedrockModel,
				MaxTokens: cfg.MaxTokens,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		case "anthropic":
			p, err := anthropic.New(anthropic.Config{
				Name:      "anthropic",
				Model:     cfg.AnthropicModel,
				MaxTokens: cfg.MaxTokens,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, p)
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
