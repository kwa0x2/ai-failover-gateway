// Package core holds the domain: the vendor-neutral model, the ports it talks
// through, and the failover routing policy.
package core

// Message is a single turn in a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a vendor-neutral completion request.
type Request struct {
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// Response is a vendor-neutral completion response.
type Response struct {
	Text         string `json:"text"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	StopReason   string `json:"stop_reason"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// Result is a Response plus how the router reached it.
type Result struct {
	Response  *Response
	Provider  string
	Failovers int
}
