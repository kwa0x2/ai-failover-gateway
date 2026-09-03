// Package anthropic is the outbound adapter for the Anthropic API.
package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

// defaultMaxTokens applies when the caller does not ask for a specific limit.
const defaultMaxTokens = 1024

// Config carries no credential.
type Config struct {
	Name      string
	Model     string
	MaxTokens int
}

type Provider struct {
	name      string
	model     string
	maxTokens int
	client    anthropic.Client
}

var _ core.Provider = (*Provider)(nil)

func New(cfg Config) (*Provider, error) {
	switch key := os.Getenv("ANTHROPIC_API_KEY"); {
	case strings.TrimSpace(key) == "":
		return nil, fmt.Errorf("anthropic: ANTHROPIC_API_KEY is not set")
	case key != strings.TrimSpace(key):
		return nil, fmt.Errorf("anthropic: ANTHROPIC_API_KEY has leading or trailing whitespace")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("anthropic: model is required")
	}
	if cfg.Name == "" {
		cfg.Name = "anthropic"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}

	opts := []option.RequestOption{option.WithMaxRetries(0)}

	if ws := strings.TrimSpace(os.Getenv("ANTHROPIC_WORKSPACE_ID")); ws != "" {
		opts = append(opts, option.WithHeader("anthropic-workspace-id", ws))
	}

	return &Provider{
		name:      cfg.Name,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		client:    anthropic.NewClient(opts...),
	}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: int64(p.maxTokens),
		Messages:  toVendorMessages(req.Messages),
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = int64(req.MaxTokens)
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, translateError(p.name, err)
	}

	if msg.StopReason == "refusal" {
		return nil, &core.Error{
			Provider:   p.name,
			StatusCode: http.StatusForbidden,
			Retryable:  false,
			Err:        fmt.Errorf("model declined the request (stop_reason=refusal)"),
		}
	}
	return &core.Response{
		Text:         textOf(msg),
		Model:        string(msg.Model),
		StopReason:   string(msg.StopReason),
		Provider:     p.name,
		InputTokens:  int(msg.Usage.InputTokens),
		OutputTokens: int(msg.Usage.OutputTokens),
	}, nil
}

func toVendorMessages(msgs []core.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		block := anthropic.NewTextBlock(m.Content)
		if m.Role == "assistant" {
			out = append(out, anthropic.NewAssistantMessage(block))
			continue
		}
		out = append(out, anthropic.NewUserMessage(block))
	}
	return out
}

// textOf concatenates the text blocks of a response.
func textOf(msg *anthropic.Message) string {
	var out string
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			out += t.Text
		}
	}
	return out
}
