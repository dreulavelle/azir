package assistant

import (
	"bufio"
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

// OpenAICompatible talks the chat/completions shape.
//
// Written against the shape rather than against one vendor, because it is what
// almost everything speaks: OpenAI itself, a gateway in front of several
// providers, and most self-hosted servers. A deployment that routes its model
// traffic through its own gateway keeps a single place to see cost, apply
// limits and choose a model — which is a better answer than Azir holding a
// separate key per vendor.
type OpenAICompatible struct {
	APIKey  string
	BaseURL string
	// Label names the service for settings and audit, since "openai" is the
	// protocol here rather than necessarily the company.
	Label  string
	Client *http.Client
}

func (o *OpenAICompatible) Name() string {
	if o.Label != "" {
		return o.Label
	}
	return "openai"
}

func (o *OpenAICompatible) endpoint() string {
	base := strings.TrimSuffix(o.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	// Tolerates a base given with or without the version segment, because both
	// are what people paste.
	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		base += "/v1"
	}
	return base + "/chat/completions"
}

type oaiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
		// Arguments arrive as a JSON string rather than an object, which is
		// this protocol's one genuine wart.
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiRequest struct {
	Model string `json:"model"`
	// Sent explicitly. Left out, a gateway is free to assume the model's full
	// window — which some bill against up front, so a short answer gets
	// refused for a budget it was never going to spend.
	MaxTokens int          `json:"max_tokens"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []oaiTool    `json:"tools,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message oaiMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAICompatible) Complete(ctx context.Context, req Request) (Reply, error) {
	body := oaiRequest{Model: req.Model, MaxTokens: cmp.Or(req.MaxAnswerTokens, answerTokens)}
	if body.Model == "" {
		return Reply{}, fmt.Errorf("assistant: no model is configured for %s", o.Name())
	}

	if req.System != "" {
		body.Messages = append(body.Messages, oaiMessage{Role: "system", Content: req.System})
	}

	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = emptySchema
		}
		body.Tools = append(body.Tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        toolName(t.Name),
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}

	for _, turn := range req.Turns {
		if turn.Role == "user" {
			body.Messages = append(body.Messages, oaiMessage{Role: "user", Content: turn.Content})
			continue
		}

		msg := oaiMessage{Role: "assistant", Content: turn.Content}
		for _, l := range turn.Lookups {
			call := oaiToolCall{ID: l.ID, Type: "function"}
			call.Function.Name = toolName(l.Capability)
			call.Function.Arguments = argumentString(l.Args)
			msg.ToolCalls = append(msg.ToolCalls, call)
		}
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		body.Messages = append(body.Messages, msg)

		// Each result is its own message, keyed back to the call it answers.
		for _, l := range turn.Lookups {
			content := string(l.Result)
			if l.Err != "" {
				content = l.Err
			}
			body.Messages = append(body.Messages, oaiMessage{
				Role: "tool", ToolCallID: l.ID, Content: content,
			})
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Reply{}, fmt.Errorf("assistant: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return Reply{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+o.APIKey)

	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}

	res, err := client.Do(httpReq)
	if err != nil {
		return Reply{}, fmt.Errorf("assistant: could not reach %s: %w", o.Name(), err)
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	// An event stream is read as it arrives, so the answer can be shown while
	// it is still being written rather than after. Some gateways stream
	// whether or not one was asked for, which is why the content type decides
	// rather than the request.
	if strings.Contains(res.Header.Get("content-type"), "text/event-stream") {
		if res.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			return Reply{}, fmt.Errorf("assistant: %s", explain(res.StatusCode, raw))
		}
		return readStream(res.Body, req.OnToken)
	}

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("assistant: could not read the reply: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("assistant: %s", explain(res.StatusCode, raw))
	}

	// A body that turns out to be a stream despite the header.
	if looksLikeEventStream("", raw) {
		return readStream(bytes.NewReader(raw), req.OnToken)
	}

	var decoded oaiResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Reply{}, fmt.Errorf("assistant: could not understand the reply: %w", err)
	}
	if decoded.Error != nil {
		return Reply{}, fmt.Errorf("assistant: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return Reply{}, fmt.Errorf("assistant: %s returned no answer", o.Name())
	}

	message := decoded.Choices[0].Message
	reply := Reply{Text: message.Content}
	for _, call := range message.ToolCalls {
		args := json.RawMessage(call.Function.Arguments)
		if len(strings.TrimSpace(call.Function.Arguments)) == 0 {
			args = json.RawMessage(`{}`)
		}
		reply.Lookups = append(reply.Lookups, Lookup{
			ID:         call.ID,
			Capability: capabilityName(call.Function.Name),
			Args:       args,
		})
	}
	return reply, nil
}

// argumentString renders arguments as this protocol wants them: a JSON string,
// not an object.
func argumentString(args json.RawMessage) string {
	if len(args) == 0 {
		return "{}"
	}
	return string(args)
}

// Models asks the service what it can run.
//
// Model names are the most commonly wrong setting on the assistant page: a
// gateway usually renames what it fronts, so the name from a vendor's own
// documentation is rarely the name to type here. Listing them turns a guess
// into a choice.
func (o *OpenAICompatible) Models(ctx context.Context) ([]string, error) {
	base := strings.TrimSuffix(o.endpoint(), "/chat/completions")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+o.APIKey)

	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("assistant: could not reach %s: %w", o.Name(), err)
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("assistant: %s", explain(res.StatusCode, raw))
	}

	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("assistant: could not read the model list: %w", err)
	}

	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// --- event streams ------------------------------------------------------------

func looksLikeEventStream(contentType string, body []byte) bool {
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}
	return bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:"))
}

// streamChoice is one delta in a streamed completion.
type streamChoice struct {
	Delta struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			// Index rather than id, because a streamed call arrives in pieces
			// and only the index is present on every one of them.
			Index    int    `json:"index"`
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"delta"`
}

// readStream reassembles a streamed completion, reporting text as it arrives.
//
// Tool calls come split across events — a name in one, argument fragments in
// several more — so they accumulate by index and are only assembled at the end.
// Text, by contrast, is forwarded immediately: that is the whole point.
func readStream(body io.Reader, onToken func(string)) (Reply, error) {
	type building struct {
		id, name string
		args     strings.Builder
	}

	var text strings.Builder
	calls := map[int]*building{}
	order := []int{}
	var streamErr string

	scanner := bufio.NewScanner(body)
	// A single event can carry a large tool-call fragment, well past the
	// default limit; the whole answer is already capped by max_tokens.
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		// An error can arrive mid-stream, after a 200 has already been sent.
		var maybeErr struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &maybeErr); err == nil && maybeErr.Error != nil {
			streamErr = maybeErr.Error.Message
			continue
		}

		var event struct {
			Choices []streamChoice `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue // a keepalive, or a shape this code does not need
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if onToken != nil {
					onToken(choice.Delta.Content)
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				b, seen := calls[call.Index]
				if !seen {
					b = &building{}
					calls[call.Index] = b
					order = append(order, call.Index)
				}
				if call.ID != "" {
					b.id = call.ID
				}
				if call.Function.Name != "" {
					b.name = call.Function.Name
				}
				b.args.WriteString(call.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Reply{}, fmt.Errorf("assistant: the answer was cut short: %w", err)
	}
	if streamErr != "" {
		return Reply{}, fmt.Errorf("assistant: %s", streamErr)
	}

	reply := Reply{Text: text.String()}
	for _, index := range order {
		b := calls[index]
		if b.name == "" {
			continue
		}
		args := strings.TrimSpace(b.args.String())
		if args == "" {
			args = "{}"
		}
		reply.Lookups = append(reply.Lookups, Lookup{
			ID:         b.id,
			Capability: capabilityName(b.name),
			Args:       json.RawMessage(args),
		})
	}
	return reply, nil
}
