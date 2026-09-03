# ai-failover-gateway

Go HTTP gateway that routes prompt-completion requests to Claude and fails over
between providers when one degrades. Hexagonal architecture.

## Commands

```sh
make run     # start once
make dev     # live reload (air) — resets breaker state on every save
make test    # go test ./...
make lint    # go vet + gofmt check
make arch    # verify core depends on no adapter
make cover   # coverage summary
```

## Architecture

```
cmd/gateway        → app
internal/app       → rest, bedrock, anthropic, mock, metrics, config, core
internal/adapter/  → core          (inbound/rest, outbound/{mock,anthropic,bedrock,metrics})
internal/config    → (nothing)
internal/core      → (nothing)     ← the rule
```

**The one rule: `internal/core` must not import any other internal package.**
Adapters depend inward on core; core never depends outward. `make arch`
enforces this and should run in CI.

Slogan for every change: **adapters translate, core decides.** A new provider
is a new adapter plus one case in `app.buildProviders` — `routing.go` is never
touched.

| Package | Responsibility |
|---|---|
| `core/model.go` | Vendor-neutral types: Message, Request, Response, Result |
| `core/errors.go` | `core.Error` with the `Retryable` flag |
| `core/port.go` | Ports: `Provider`, `Recorder` (+ `NopRecorder`) |
| `core/routing.go` | Retry, circuit breaker, failover — all policy lives here |
| `adapter/outbound/mock` | Scriptable fake provider for tests and local runs |
| `adapter/outbound/anthropic` | Claude via the Anthropic API (API key) |
| `adapter/outbound/bedrock` | Claude via AWS Bedrock Mantle (SigV4) |
| `adapter/outbound/metrics` | Prometheus; implements `core.Recorder` **and** `rest.RequestRecorder` |
| `adapter/inbound/rest` | Gin HTTP, DTOs, error→status mapping |
| `app/app.go` | Composition root, graceful shutdown |
| `config/` | Env loading, plain data only |

## Design decisions (do not silently reverse these)

**Retry sits inside the circuit breaker.** `breaker.Execute(retry(provider))`.
All attempts for one request collapse into one breaker outcome. Nesting them
the other way would let `MaxAttempts` silently control how fast the circuit
trips — two unrelated knobs coupled.

**One error classification drives three decisions.** `core.Error.Retryable` is
read by the retry loop, by the breaker's `IsSuccessful`, and by the failover
loop. A non-retryable error (a 400) is not retried, is not failed over, and
counts as a *success* for the breaker — a flood of malformed requests must
never take a healthy provider offline.

**Unclassified errors default to retryable.** An unknown transport failure is
more likely transient than permanent, and the cost of guessing wrong is
asymmetric.

**No `Health()` method on the `Provider` port.** Active health checks lie: they
probe a different path than real traffic. Health is derived from real traffic
by the circuit breaker.

**Providers perform exactly one attempt.** SDK-level retries are disabled
(`option.WithMaxRetries(0)`); an adapter that retries hides failures from the
breaker and corrupts its accounting.

**Two-level timeouts.** The caller's context bounds the whole request;
`AttemptTimeout` bounds a single provider call, so a slow primary cannot eat
the budget and leave nothing for the fallback.

**Dead caller context stops the chain.** No failover when `ctx.Err() != nil` —
nobody will read the answer.

**Metrics are labelled with the route pattern** (`c.FullPath()`), never the raw
URL; unmatched routes are not recorded at all. Raw URLs would explode
cardinality.

**Interfaces are declared by the consumer.** `rest.Completer` and
`rest.RequestRecorder` live in `rest`, not next to their implementations.

## Provider note

`bedrock` and `anthropic` reach **the same Claude model** over two different
infrastructures. Bedrock is a hosting platform, not a separate LLM. The
failover is still real: separate quotas, credentials and failure domains.
Describe it that way — do not claim "two different LLM vendors".

Their `translateError` tables are deliberately duplicated rather than shared,
so one vendor's status semantics can change without altering the other's
failover behaviour.

## Configuration

All via environment variables; defaults in `internal/config/config.go`.

For local runs, `cp .env.example .env` — the composition root loads it through
`godotenv` before `config.Load()`. A variable already present in the real
environment wins over the file, so a pod or CI never sees a stray `.env`; a
missing `.env` is not an error. `.env` is gitignored, `.env.example` is not.

A *set but unparseable* variable now fails startup (`config.Load` returns every
bad key at once) instead of silently falling back to its default.

| Variable | Default |
|---|---|
| `GATEWAY_ADDR` | `:8080` |
| `LOG_LEVEL` | `info` |
| `PROVIDERS` | `mock-flaky,mock` (failover order) |
| `REQUEST_TIMEOUT` | `60s` |
| `PROVIDER_ATTEMPT_TIMEOUT` | `30s` |
| `RETRY_MAX_ATTEMPTS` | `3` (total tries, not retries) |
| `RETRY_INITIAL_BACKOFF` / `RETRY_MAX_BACKOFF` | `100ms` / `2s` |
| `BREAKER_MIN_REQUESTS` | `5` |
| `BREAKER_FAILURE_RATIO` | `0.5` |
| `BREAKER_OPEN_TIMEOUT` | `15s` |
| `MAX_TOKENS` | `1024` |
| `BEDROCK_REGION` / `BEDROCK_MODEL` | `us-east-1` / `anthropic.claude-opus-5` |
| `ANTHROPIC_MODEL` | `claude-opus-5` |
| `ANTHROPIC_API_KEY` | read by the SDK, not by `config` |
| `ANTHROPIC_WORKSPACE_ID` | optional; only identity-linked keys need it |

Provider names accepted by `PROVIDERS`: `mock`, `mock-flaky`, `mock-slow`,
`mock-dead`, `bedrock`, `anthropic`.

`mock-slow` hangs until `PROVIDER_ATTEMPT_TIMEOUT` cuts each attempt. It exists
because `mock-dead` cannot show what the breaker is worth: an instant failure
costs only the retry backoff (~300ms), so opening the circuit saves almost
nothing. A hanging provider costs the full timeout on every attempt. Measured
with `PROVIDERS=mock-slow,mock PROVIDER_ATTEMPT_TIMEOUT=1s`: 3.2s per request
while the circuit is closed, 0.0004s once it opens.

No credential is read into `config.Config`. AWS credentials come from the
default chain (env, shared file, IAM role); `ANTHROPIC_API_KEY` is picked up
from the environment by the Anthropic SDK. `config.go` names those variables in
documentation only — it never holds their values, so no secret can reach a log
line, a `%+v` dump or a config snapshot. The adapters still check at startup
that a key is present, so a missing one fails before the gateway accepts
traffic.

## Endpoints

| Method | Path | Notes |
|---|---|---|
| `POST` | `/v1/chat` | Response carries `provider`, `failovers`, `stop_reason` |
| `GET` | `/healthz` | Liveness |
| `GET` | `/readyz` | Readiness |
| `GET` | `/metrics` | Prometheus, mounted by the composition root |

Error mapping (`rest.classify`, locked by tests — this is the API contract):
`ErrNoHealthyProvider`→503, `DeadlineExceeded`→504, `Canceled`→408, provider
4xx→passed through, provider 5xx→502.

## Testing

`core` and `rest` are the two packages that make decisions; both are >95%
covered and must stay that way. Tests assert on behaviour, never on config
values (e.g. use `cfg.MaxAttempts`, not a hard-coded `3`).

Adapters are tested through their `translateError` tables — no network needed.
Only a final smoke test requires real credentials.

## Status

Done: domain + ports, retry + circuit breaker + failover, HTTP API, Prometheus
metrics, mock/anthropic/bedrock adapters, 44 tests, `make arch`. Verified
end to end against the live Anthropic API: failover into a real vendor, the
breaker opening after a dead primary, and the `translateError` table matching
what the API actually returns for an invalid model (404, not retried).

Next: commit the working tree (nothing is committed yet), then Docker + kind +
Kubernetes manifests, then Prometheus + Grafana dashboards, then DynamoDB quota
tracking and an SQS audit queue for failed requests.

Known gap: `config.Load` validates that values *parse*, not that they make
sense. `BREAKER_FAILURE_RATIO=1.5` or `RETRY_MAX_ATTEMPTS=0` is accepted today
and silently disables the mechanism it configures.

Deliberately out of scope for now: streaming (SSE), and any provider beyond
Claude. A future OpenAI-compatible adapter should take a configurable base URL
so it can be tested against a local Ollama at no cost.

Also deferred: per-request workspace selection. `ANTHROPIC_WORKSPACE_ID` is
read once at construction, so one process serves one workspace — enough for an
identity-linked key. Routing each request to a different workspace would need
the caller's intent to reach the adapter, and "workspace" is an Anthropic
concept that must not enter `core.Request`. The shape to use when it is needed:
`rest` reads a tenant header, passes it through as an opaque value, and each
adapter decides what that means for its vendor.

## Working style

Explain the reasoning behind a change in chat, not just the change. Do not
commit or push unless asked.

Keep answers short. Lead with the result; one or two sentences of reasoning
when a decision is non-obvious, then stop.

**Comments: doc comments only.** One sentence on exported identifiers and on
each package, nothing else — no explanatory notes inside function bodies, no
notes on struct fields, no trailing comments on code lines. The reasoning
belongs in this file, in the commit message, or in chat, not in the source.
The design decisions above exist so the code does not have to carry them.
