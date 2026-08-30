// Package config loads settings from the environment. It holds plain data and
// imports no other internal package; mapping these values onto domain types is
// the composition root's job.
package config

import (
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

	// Providers is the failover order, e.g. "bedrock,anthropic".
	Providers []string

	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	AttemptTimeout time.Duration

	BreakerMinRequests  int
	BreakerFailureRatio float64
	BreakerOpenTimeout  time.Duration
	BreakerInterval     time.Duration
}

// Load reads the environment and applies defaults.
func Load() Config {
	return Config{
		Addr:            str("GATEWAY_ADDR", ":8080"),
		LogLevel:        str("LOG_LEVEL", "info"),
		RequestTimeout:  dur("REQUEST_TIMEOUT", 60*time.Second),
		ShutdownTimeout: dur("SHUTDOWN_TIMEOUT", 15*time.Second),
		Providers:       list("PROVIDERS", []string{"mock-flaky", "mock"}),

		MaxAttempts:    num("RETRY_MAX_ATTEMPTS", 3),
		InitialBackoff: dur("RETRY_INITIAL_BACKOFF", 100*time.Millisecond),
		MaxBackoff:     dur("RETRY_MAX_BACKOFF", 2*time.Second),
		AttemptTimeout: dur("PROVIDER_ATTEMPT_TIMEOUT", 30*time.Second),

		BreakerMinRequests:  num("BREAKER_MIN_REQUESTS", 5),
		BreakerFailureRatio: ratio("BREAKER_FAILURE_RATIO", 0.5),
		BreakerOpenTimeout:  dur("BREAKER_OPEN_TIMEOUT", 15*time.Second),
		BreakerInterval:     dur("BREAKER_INTERVAL", 60*time.Second),
	}
}

func str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func num(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func ratio(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return f
	}
	return def
}

func dur(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}

func list(key string, def []string) []string {
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
		return def
	}
	return out
}
