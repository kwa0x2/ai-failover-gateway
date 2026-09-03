package rest

import "github.com/kwa0x2/ai-failover-gateway/internal/core"

// chatRequest is the public wire format, kept separate from core.Request so
// validation rules and JSON field names can change without touching the
// domain.
type chatRequest struct {
	Messages []struct {
		Role    string `json:"role" binding:"required,oneof=user assistant"`
		Content string `json:"content" binding:"required"`
	} `json:"messages" binding:"required,min=1,dive"`
	System    string `json:"system"`
	MaxTokens int    `json:"max_tokens" binding:"omitempty,min=1,max=8192"`
}

func (r chatRequest) toDomain() core.Request {
	msgs := make([]core.Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		msgs = append(msgs, core.Message{Role: m.Role, Content: m.Content})
	}
	return core.Request{Messages: msgs, System: r.System, MaxTokens: r.MaxTokens}
}

// chatResponse exposes Provider and Failovers so callers can see which backend
// served the request.
type chatResponse struct {
	RequestID    string `json:"request_id"`
	Text         string `json:"text"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason"`
	Failovers    int    `json:"failovers"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type errorResponse struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Error     string `json:"error"`
}
