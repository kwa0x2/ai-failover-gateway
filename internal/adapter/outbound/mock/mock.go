// Package mock is a scriptable Provider adapter used for local runs and for
// deterministic tests of the routing policy.
package mock

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// Behaviour is what a single call should do.
type Behaviour struct {
	Delay time.Duration
	Err   error
	Text  string
}

// Provider replays behaviours in order; the last entry repeats once the script
// runs out.
type Provider struct {
	name string

	mu     sync.Mutex
	script []Behaviour

	calls atomic.Int64
}

var _ core.Provider = (*Provider)(nil)

// New returns a provider that always succeeds with the given text.
func New(name, text string) *Provider {
	return &Provider{name: name, script: []Behaviour{{Text: text}}}
}

// NewScripted returns a provider driven by an explicit script.
func NewScripted(name string, script ...Behaviour) *Provider {
	if len(script) == 0 {
		script = []Behaviour{{Text: "ok"}}
	}
	return &Provider{name: name, script: script}
}

func (p *Provider) Name() string { return p.name }

// Calls reports how many times Complete was invoked.
func (p *Provider) Calls() int { return int(p.calls.Load()) }

// SetScript swaps the script and resets the call counter.
func (p *Provider) SetScript(script ...Behaviour) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = script
	p.calls.Store(0)
}

func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	n := int(p.calls.Add(1))

	p.mu.Lock()
	b := p.script[min(n-1, len(p.script)-1)]
	p.mu.Unlock()

	if b.Delay > 0 {
		// Race the delay against the context so a cancelled caller is not made
		// to wait it out.
		select {
		case <-time.After(b.Delay):
		case <-ctx.Done():
			return nil, &core.Error{Provider: p.name, Retryable: true, Err: ctx.Err()}
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, &core.Error{Provider: p.name, Retryable: true, Err: err}
	}

	if b.Err != nil {
		return nil, b.Err
	}

	text := b.Text
	if text == "" {
		text = fmt.Sprintf("mock response from %s", p.name)
	}
	return &core.Response{
		Text:         text,
		Model:        "mock-model",
		Provider:     p.name,
		InputTokens:  len(req.Messages),
		OutputTokens: len(text),
	}, nil
}

// Retryable builds a transient failure, e.g. a simulated 503.
func Retryable(name string, status int, msg string) error {
	return &core.Error{Provider: name, StatusCode: status, Retryable: true, Err: fmt.Errorf("%s", msg)}
}

// Permanent builds a non-retryable failure, e.g. a simulated 400.
func Permanent(name string, status int, msg string) error {
	return &core.Error{Provider: name, StatusCode: status, Retryable: false, Err: fmt.Errorf("%s", msg)}
}
