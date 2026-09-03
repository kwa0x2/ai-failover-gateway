// Package bedrock is the outbound adapter for Claude on Amazon Bedrock.
package bedrock

import (
	"context"
	"fmt"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/kwa0x2/ai-failover-gateway/internal/core"
)

const defaultMaxTokens = 1024

type Config struct {
	Name      string
	Region    string
	Model     string
	MaxTokens int
}

type Provider struct {
	name      string
	model     string
	maxTokens int
	client    *bedrock.MantleClient
}

var _ core.Provider = (*Provider)(nil)

// New resolves AWS credentials through the default chain: environment
// variables, the shared credentials file, then the instance or pod role.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("bedrock: region is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("bedrock: model is required")
	}
	if cfg.Name == "" {
		cfg.Name = "bedrock"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}

	client, err := bedrock.NewMantleClient(ctx,
		bedrock.MantleClientConfig{AWSRegion: cfg.Region},
		option.WithMaxRetries(0),
	)
	if err != nil {
		return nil, fmt.Errorf("bedrock: client: %w", err)
	}

	return &Provider{
		name:      cfg.Name,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		client:    client,
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

// textOf concatenates every text block; reading only the first would silently
// truncate a multi-block response.
func textOf(msg *anthropic.Message) string {
	var out string
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			out += t.Text
		}
	}
	return out
}
