// Package config loads settings from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr            string
	LogLevel        string
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration

	Providers []string

	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	AttemptTimeout time.Duration

	BreakerMinRequests  int
	BreakerFailureRatio float64
	BreakerOpenTimeout  time.Duration
	BreakerInterval     time.Duration

	MaxTokens int

	BedrockRegion string
	BedrockModel  string

	AnthropicModel string
}

// Load reads the environment and applies defaults.
func Load() (Config, error) {
	var e env

	cfg := Config{
		Addr:            e.str("GATEWAY_ADDR", ":8080"),
		LogLevel:        e.str("LOG_LEVEL", "info"),
		RequestTimeout:  e.dur("REQUEST_TIMEOUT", 60*time.Second),
		ShutdownTimeout: e.dur("SHUTDOWN_TIMEOUT", 15*time.Second),
		Providers:       e.list("PROVIDERS", []string{"mock-flaky", "mock"}),

		MaxAttempts:    e.num("RETRY_MAX_ATTEMPTS", 3),
		InitialBackoff: e.dur("RETRY_INITIAL_BACKOFF", 100*time.Millisecond),
		MaxBackoff:     e.dur("RETRY_MAX_BACKOFF", 2*time.Second),
		AttemptTimeout: e.dur("PROVIDER_ATTEMPT_TIMEOUT", 30*time.Second),

		BreakerMinRequests:  e.num("BREAKER_MIN_REQUESTS", 5),
		BreakerFailureRatio: e.ratio("BREAKER_FAILURE_RATIO", 0.5),
		BreakerOpenTimeout:  e.dur("BREAKER_OPEN_TIMEOUT", 15*time.Second),
		BreakerInterval:     e.dur("BREAKER_INTERVAL", 60*time.Second),

		MaxTokens: e.num("MAX_TOKENS", 1024),

		BedrockRegion: e.str("BEDROCK_REGION", "us-east-1"),
		BedrockModel:  e.str("BEDROCK_MODEL", "anthropic.claude-opus-5"),

		AnthropicModel: e.str("ANTHROPIC_MODEL", "claude-opus-5"),
	}

	return cfg, e.err()
}

// env reads typed values from the environment and collects the failures.
type env struct {
	errs []error
}

func (e *env) err() error {
	return errors.Join(e.errs...)
}

func (e *env) invalid(key, raw string, err error) {
	e.errs = append(e.errs, fmt.Errorf("%s=%q: %w", key, raw, err))
}

func (e *env) str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (e *env) num(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		e.invalid(key, raw, err)
		return def
	}
	return n
}

func (e *env) ratio(key string, def float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		e.invalid(key, raw, err)
		return def
	}
	return f
}

func (e *env) dur(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		e.invalid(key, raw, err)
		return def
	}
	return d
}

func (e *env) list(key string, def []string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		e.invalid(key, raw, errors.New("no non-empty entries"))
		return def
	}
	return out
}
