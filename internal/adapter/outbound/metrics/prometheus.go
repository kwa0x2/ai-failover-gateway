// Package metrics is the Prometheus outbound adapter.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

type Prometheus struct {
	registry *prometheus.Registry

	attempts   *prometheus.CounterVec
	latency    *prometheus.HistogramVec
	breaker    *prometheus.GaugeVec
	rejections *prometheus.CounterVec
	failovers  *prometheus.CounterVec
	tokens     *prometheus.CounterVec
	requests   *prometheus.HistogramVec
}

var _ core.Recorder = (*Prometheus)(nil)

// New builds the collectors on a private registry rather than the global
// default, so nothing a dependency registers leaks into our /metrics output.
func New() *Prometheus {
	reg := prometheus.NewRegistry()
	f := promauto.With(reg)

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return &Prometheus{
		registry: reg,
		attempts: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_provider_attempts_total",
			Help: "Provider call attempts by outcome.",
		}, []string{"provider", "outcome"}),
		latency: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_provider_latency_seconds",
			Help:    "Latency of a single provider attempt.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider"}),
		breaker: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_breaker_state",
			Help: "Circuit breaker state: 0 closed, 1 half-open, 2 open.",
		}, []string{"provider"}),
		rejections: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_breaker_rejections_total",
			Help: "Calls short-circuited by an open breaker.",
		}, []string{"provider"}),
		failovers: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_failovers_total",
			Help: "Failover transitions between providers.",
		}, []string{"from", "to"}),
		tokens: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_tokens_total",
			Help: "Tokens consumed by provider and direction.",
		}, []string{"provider", "direction"}),
		requests: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_request_duration_seconds",
			Help:    "End-to-end HTTP request duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
	}
}

// Handler serves the exposition format for scraping.
func (p *Prometheus) Handler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}

// InitProvider pre-creates the series for a provider so dashboards show a
// closed breaker from startup instead of an empty graph until the first
// failure.
func (p *Prometheus) InitProvider(name string) {
	p.breaker.WithLabelValues(name).Set(float64(core.BreakerClosed))
}

func (p *Prometheus) ProviderAttempt(provider, outcome string) {
	p.attempts.WithLabelValues(provider, outcome).Inc()
}

func (p *Prometheus) ProviderLatency(provider string, d time.Duration) {
	p.latency.WithLabelValues(provider).Observe(d.Seconds())
}

func (p *Prometheus) BreakerStateChanged(provider string, state core.BreakerState) {
	p.breaker.WithLabelValues(provider).Set(float64(state))
}

func (p *Prometheus) BreakerRejected(provider string) {
	p.rejections.WithLabelValues(provider).Inc()
}

func (p *Prometheus) Failover(from, to string) {
	p.failovers.WithLabelValues(from, to).Inc()
}

func (p *Prometheus) TokensUsed(provider string, input, output int) {
	p.tokens.WithLabelValues(provider, "input").Add(float64(input))
	p.tokens.WithLabelValues(provider, "output").Add(float64(output))
}

// Request records an HTTP request.
func (p *Prometheus) Request(route, method, status string, d time.Duration) {
	p.requests.WithLabelValues(route, method, status).Observe(d.Seconds())
}
