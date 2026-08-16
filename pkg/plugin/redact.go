package plugin

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// redactedMarker replaces any value the redactor removes. It is deliberately
// conspicuous so a leak-shaped bug shows up in output rather than hiding.
const redactedMarker = "[redacted]"

// sensitiveKeys are matched case-insensitively as substrings of JSON object
// keys. False positives are acceptable and intentional: a ticket field that
// happens to be called "token" being redacted is a far cheaper mistake than a
// credential reaching model context.
var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "apikey", "api_key",
	"authorization", "credential", "private_key", "session", "cookie",
}

// Redactor scrubs handler return values before they reach the wire. It runs
// inside the SDK rather than in each plugin, because enforcement that is
// opt-in per plugin is not enforcement. Core redacts again during context
// assembly; this is the first of two gates, not the only one.
type Redactor struct {
	mu       sync.RWMutex
	literals []string
}

// NewRedactor returns a Redactor that removes the given literal secret values
// wherever they appear, in addition to redacting by key name. Callers pass the
// credentials they resolved from the vault, so a value cannot escape by being
// embedded in a message the key-name rules would miss.
func NewRedactor(literals ...string) *Redactor {
	r := &Redactor{}
	r.Learn(literals...)
	return r
}

// Learn registers additional secret values at runtime. Credentials are
// resolved long after startup, so the redactor has to be able to grow: a
// value that arrives at request time is exactly the one most likely to be
// echoed back by mistake.
func (r *Redactor) Learn(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		// Very short values would match everywhere and destroy the payload.
		if len(v) < 6 || slices.Contains(r.literals, v) {
			continue
		}
		r.literals = append(r.literals, v)
	}
}

// Text scrubs registered secret literals from a plain string. Used for
// anything that reaches a caller without passing through Value — an error
// message most importantly.
func (r *Redactor) Text(s string) string { return r.scrubLiterals(s) }

// Value round-trips v through JSON and returns a redacted copy.
func (r *Redactor) Value(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return r.walk(decoded, false), nil
}

func (r *Redactor) walk(v any, parentSensitive bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			// Keys are scrubbed too. A secret used as an object key would
			// otherwise escape entirely, since walking only values never
			// inspects it — found by the adversarial plugin fixture.
			out[r.scrubLiterals(k)] = r.walk(val, isSensitiveKey(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = r.walk(val, parentSensitive)
		}
		return out
	case string:
		if parentSensitive {
			return redactedMarker
		}
		return r.scrubLiterals(t)
	default:
		if parentSensitive {
			return redactedMarker
		}
		return v
	}
}

func (r *Redactor) scrubLiterals(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, lit := range r.literals {
		if strings.Contains(s, lit) {
			s = strings.ReplaceAll(s, lit, redactedMarker)
		}
	}
	return s
}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	for _, s := range sensitiveKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// stripSecretFields removes secret-marked properties from a JSON Schema before
// it is published for model consumption, so the model cannot request what it
// must never receive.
func stripSecretFields(schema json.RawMessage, secrets []string) json.RawMessage {
	if len(schema) == 0 || len(secrets) == 0 {
		return schema
	}
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return schema
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		return schema
	}
	for _, s := range secrets {
		delete(props, s)
	}
	if req, ok := doc["required"].([]any); ok {
		filtered := make([]any, 0, len(req))
		for _, item := range req {
			name, _ := item.(string)
			if !slices.Contains(secrets, name) {
				filtered = append(filtered, item)
			}
		}
		doc["required"] = filtered
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return schema
	}
	return out
}
