package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// fakeCompleter stands in for the router: the HTTP layer only cares what the
// domain returns, not how it got there.
type fakeCompleter struct {
	result *core.Result
	err    error
	calls  int
}

func (f *fakeCompleter) Complete(context.Context, core.Request) (*core.Result, error) {
	f.calls++
	return f.result, f.err
}

type recordedRequest struct {
	route, method, status string
}

type fakeRecorder struct{ seen []recordedRequest }

func (r *fakeRecorder) Request(route, method, status string, _ time.Duration) {
	r.seen = append(r.seen, recordedRequest{route, method, status})
}

func newTestServer(c Completer, rec RequestRecorder) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(c, log, 5*time.Second, rec).Handler()
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const validBody = `{"messages":[{"role":"user","content":"hi"}]}`

func okResult() *core.Result {
	return &core.Result{
		Response: &core.Response{
			Text: "hello", Model: "m", Provider: "primary",
			InputTokens: 1, OutputTokens: 2,
		},
		Provider:  "primary",
		Failovers: 1,
	}
}

func TestChat_Success(t *testing.T) {
	h := newTestServer(&fakeCompleter{result: okResult()}, nil)

	w := post(h, validBody)
	require.Equal(t, http.StatusOK, w.Code)

	var got chatResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "hello", got.Text)
	assert.Equal(t, "primary", got.Provider)
	assert.Equal(t, 1, got.Failovers)
	assert.NotEmpty(t, got.RequestID)
	assert.Equal(t, got.RequestID, w.Header().Get("X-Request-ID"))
}

// A caller-supplied request id must survive, so a trace spans services.
func TestChat_PropagatesCallerRequestID(t *testing.T) {
	h := newTestServer(&fakeCompleter{result: okResult()}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(validBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "caller-supplied-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, "caller-supplied-id", w.Header().Get("X-Request-ID"))
}

// Validation happens at the edge: a malformed request must never reach the
// router, so it costs no provider quota.
func TestChat_ValidationRejectsBeforeRouter(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty messages", `{"messages":[]}`},
		{"missing messages", `{}`},
		{"unknown role", `{"messages":[{"role":"admin","content":"x"}]}`},
		{"empty content", `{"messages":[{"role":"user","content":""}]}`},
		{"max_tokens too large", `{"messages":[{"role":"user","content":"x"}],"max_tokens":99999}`},
		{"malformed json", `{"messages":`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &fakeCompleter{result: okResult()}
			h := newTestServer(router, nil)

			w := post(h, tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, 0, router.calls, "router must not be reached")

			var got errorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, "invalid_request", got.Code)
		})
	}
}

// The status mapping is the API contract; this table is what stops it from
// drifting silently.
func TestChat_ErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "all providers down",
			err:        fmt.Errorf("%w: boom", core.ErrNoHealthyProvider),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "no_healthy_provider",
		},
		{
			name:       "request budget exhausted",
			err:        fmt.Errorf("cancelled: %w", context.DeadlineExceeded),
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "upstream_timeout",
		},
		{
			name:       "client went away",
			err:        fmt.Errorf("cancelled: %w", context.Canceled),
			wantStatus: http.StatusRequestTimeout,
			wantCode:   "client_cancelled",
		},
		{
			name:       "provider rejected the request",
			err:        &core.Error{Provider: "primary", StatusCode: 400, Retryable: false},
			wantStatus: http.StatusBadRequest,
			wantCode:   "provider_rejected",
		},
		{
			name:       "provider 5xx is not passed through",
			err:        &core.Error{Provider: "primary", StatusCode: 500, Retryable: true},
			wantStatus: http.StatusBadGateway,
			wantCode:   "upstream_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestServer(&fakeCompleter{err: tc.err}, nil)

			w := post(h, validBody)
			assert.Equal(t, tc.wantStatus, w.Code)

			var got errorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tc.wantCode, got.Code)
			assert.NotEmpty(t, got.RequestID)
		})
	}
}

func TestProbes(t *testing.T) {
	h := newTestServer(&fakeCompleter{result: okResult()}, nil)

	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, w.Code, path)
	}
}

// Metrics must be labelled with the route pattern, never the raw URL, or
// cardinality explodes.
func TestAccessLog_RecordsRoutePattern(t *testing.T) {
	rec := &fakeRecorder{}
	h := newTestServer(&fakeCompleter{result: okResult()}, rec)

	post(h, validBody)

	require.Len(t, rec.seen, 1)
	assert.Equal(t, recordedRequest{route: "/v1/chat", method: "POST", status: "200"}, rec.seen[0])
}

// An unmatched path has no route pattern, so recording it would create a
// series per bogus URL.
func TestAccessLog_SkipsUnmatchedRoutes(t *testing.T) {
	rec := &fakeRecorder{}
	h := newTestServer(&fakeCompleter{result: okResult()}, rec)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/no/such/path", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, rec.seen)
}
