package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic talks to the Messages API.
//
// Written directly rather than through a vendor SDK: the surface used here is
// small and stable, and a self-hosted product that ships an SDK inherits its
// release cadence and its transitive dependencies for the rest of its life.
type Anthropic struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

// DefaultModel is used when an administrator has not chosen one.
const DefaultModel = "claude-sonnet-4-5"

// answerTokens caps a single reply when nothing else says otherwise.
//
// Sized for what current models actually write rather than for the smallest
// thing that fits: a long ticket analysis with a drafted reply runs well past
// a few thousand tokens, and being cut off mid-sentence is the worst outcome.
// A deployment on a tight budget lowers it in settings.
const answerTokens = 16384

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) endpoint() string {
	base := strings.TrimSuffix(a.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return base + "/v1/messages"
}

// wire types, kept private so the rest of the package never depends on one
// vendor's field names.

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type wireBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
}

type wireResponse struct {
	Content    []wireBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// emptySchema is sent for a tool that takes no arguments; the API requires an
// object schema even when there is nothing in it.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

func (a *Anthropic) Complete(ctx context.Context, req Request) (Reply, error) {
	body := wireRequest{
		Model:     req.Model,
		MaxTokens: cmp.Or(req.MaxAnswerTokens, answerTokens),
		System:    req.System,
	}
	if body.Model == "" {
		body.Model = DefaultModel
	}

	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = emptySchema
		}
		body.Tools = append(body.Tools, wireTool{
			Name:        toolName(t.Name),
			Description: t.Description,
			InputSchema: schema,
		})
	}

	for _, turn := range req.Turns {
		if turn.Role == "user" {
			body.Messages = append(body.Messages, wireMessage{
				Role:    "user",
				Content: []wireBlock{{Type: "text", Text: turn.Content}},
			})
			continue
		}

		// An assistant turn that made lookups becomes two messages: what it
		// asked for, then what came back. Replaying both is what stops it
		// asking the same question again on the next pass.
		blocks := []wireBlock{}
		if strings.TrimSpace(turn.Content) != "" {
			blocks = append(blocks, wireBlock{Type: "text", Text: turn.Content})
		}
		for _, l := range turn.Lookups {
			blocks = append(blocks, wireBlock{
				Type: "tool_use", ID: l.ID, Name: toolName(l.Capability), Input: l.Args,
			})
		}
		if len(blocks) == 0 {
			continue
		}
		body.Messages = append(body.Messages, wireMessage{Role: "assistant", Content: blocks})

		if len(turn.Lookups) > 0 {
			results := make([]wireBlock, 0, len(turn.Lookups))
			for _, l := range turn.Lookups {
				block := wireBlock{Type: "tool_result", ToolUseID: l.ID}
				if l.Err != "" {
					block.IsError = true
					block.Content = l.Err
				} else {
					block.Content = string(l.Result)
				}
				results = append(results, block)
			}
			body.Messages = append(body.Messages, wireMessage{Role: "user", Content: results})
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Reply{}, fmt.Errorf("assistant: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return Reply{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}

	res, err := client.Do(httpReq)
	if err != nil {
		return Reply{}, fmt.Errorf("assistant: could not reach the model provider: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("assistant: could not read the reply: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("assistant: %s", explain(res.StatusCode, raw))
	}

	var decoded wireResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Reply{}, fmt.Errorf("assistant: could not understand the reply: %w", err)
	}
	if decoded.Error != nil {
		return Reply{}, fmt.Errorf("assistant: %s", decoded.Error.Message)
	}

	var reply Reply
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			reply.Text += block.Text
		case "tool_use":
			reply.Lookups = append(reply.Lookups, Lookup{
				ID:         block.ID,
				Capability: capabilityName(block.Name),
				Args:       block.Input,
			})
		}
	}
	return reply, nil
}

// explain turns a provider failure into something an administrator can act on.
//
// The provider's own message is included only for the statuses where it says
// something useful about configuration; otherwise it can carry request detail
// that does not belong in a technician's chat window.
func explain(status int, body []byte) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "the model provider rejected the API key; check it in Settings"
	case http.StatusNotFound:
		return "the model provider does not recognise that model name; check it in Settings"
	case http.StatusTooManyRequests:
		return "the model provider is rate limiting this account; try again shortly"
	case http.StatusRequestEntityTooLarge:
		return "this conversation has grown too large for the model; start a new chat"
	case http.StatusPaymentRequired:
		// The provider's own wording names the limit and where to raise it,
		// which is more useful than anything generic.
		var doc struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &doc); err == nil && doc.Error.Message != "" {
			return "the model provider refused on billing: " + doc.Error.Message
		}
		return "the model provider is out of credit for this key"
	}

	var doc struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err == nil && doc.Error.Message != "" && status < 500 {
		return doc.Error.Message
	}
	if status >= 500 {
		return "the model provider is having trouble; try again shortly"
	}
	return fmt.Sprintf("the model provider returned %d", status)
}

// The API restricts tool names to [a-zA-Z0-9_-], and Azir's capability names
// are dotted. The mapping is total and reversible, so the model's choice always
// resolves back to exactly one capability.

func toolName(capability string) string {
	return strings.ReplaceAll(capability, ".", "__")
}

func capabilityName(tool string) string {
	return strings.ReplaceAll(tool, "__", ".")
}
