// Package rest is the inbound HTTP adapter.
package rest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// Completer is what this adapter needs from the domain; declaring it here
// keeps the handler testable with a small fake.
type Completer interface {
	Complete(ctx context.Context, req core.Request) (*core.Result, error)
}

// RequestRecorder is the HTTP-level telemetry this adapter needs.
type RequestRecorder interface {
	Request(route, method, status string, d time.Duration)
}

type nopRequestRecorder struct{}

func (nopRequestRecorder) Request(string, string, string, time.Duration) {}

type Server struct {
	router  Completer
	log     *slog.Logger
	timeout time.Duration
	rec     RequestRecorder
	extra   map[string]http.Handler
}

func NewServer(router Completer, log *slog.Logger, timeout time.Duration, rec RequestRecorder) *Server {
	if rec == nil {
		rec = nopRequestRecorder{}
	}
	return &Server{
		router:  router,
		log:     log,
		timeout: timeout,
		rec:     rec,
		extra:   map[string]http.Handler{},
	}
}

// Mount registers an extra GET handler, e.g. the Prometheus endpoint, without
// this package having to know about it.
func (s *Server) Mount(path string, h http.Handler) { s.extra[path] = h }

func (s *Server) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(gin.Recovery(), s.requestID(), s.accessLog())

	e.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	e.GET("/readyz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ready"}) })

	e.POST("/v1/chat", s.handleChat)

	for path, h := range s.extra {
		e.GET(path, gin.WrapH(h))
	}

	s.logRoutes(e)
	return e
}

// logRoutes prints the routing table at startup; in a container the pod log is
// the only place to discover what is mounted.
func (s *Server) logRoutes(e *gin.Engine) {
	routes := e.Routes()
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})
	for _, r := range routes {
		s.log.Info("route registered",
			slog.String("method", r.Method),
			slog.String("path", r.Path))
	}
}

const requestIDKey = "request_id"

// requestID reuses the caller's header when present so a trace survives across
// services.
func (s *Server) requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		elapsed := time.Since(start)

		route := c.FullPath()

		s.log.Info("request",
			slog.String("request_id", c.GetString(requestIDKey)),
			slog.String("method", c.Request.Method),
			slog.String("path", route),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", elapsed))

		if route != "" {
			s.rec.Request(route, c.Request.Method, strconv.Itoa(c.Writer.Status()), elapsed)
		}
	}
}

func (s *Server) handleChat(c *gin.Context) {
	reqID := c.GetString(requestIDKey)

	var body chatRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			RequestID: reqID, Code: "invalid_request", Error: err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), s.timeout)
	defer cancel()

	res, err := s.router.Complete(ctx, body.toDomain())
	if err != nil {
		status, code := classify(err)
		s.log.Error("chat failed",
			slog.String("request_id", reqID),
			slog.String("code", code),
			slog.String("error", err.Error()))
		c.JSON(status, errorResponse{RequestID: reqID, Code: code, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, chatResponse{
		RequestID:    reqID,
		Text:         res.Response.Text,
		Provider:     res.Provider,
		Model:        res.Response.Model,
		Failovers:    res.Failovers,
		InputTokens:  res.Response.InputTokens,
		StopReason:   res.Response.StopReason,
		OutputTokens: res.Response.OutputTokens,
	})
}

// classify maps domain errors onto HTTP status codes.
func classify(err error) (int, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "upstream_timeout"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "client_cancelled"
	case errors.Is(err, core.ErrNoHealthyProvider):
		return http.StatusServiceUnavailable, "no_healthy_provider"
	}

	var pErr *core.Error
	if errors.As(err, &pErr) && !pErr.Retryable {
		if pErr.StatusCode >= 400 && pErr.StatusCode <= 499 {
			return pErr.StatusCode, "provider_rejected"
		}
	}
	return http.StatusBadGateway, "upstream_error"
}
