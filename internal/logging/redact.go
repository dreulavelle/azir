// Package logging provides a slog handler that scrubs credential material
// before it reaches any output.
//
// This is not a convenience. Logs are the most common place secrets escape:
// they are written casually, retained for a long time, and shipped to systems
// nobody audits. A secret on stdout has leaked as surely as one in a prompt.
package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Marker replaces any redacted value.
const Marker = "[redacted]"

// minLiteralLength is the shortest registered literal that will be scrubbed.
// Shorter values match everywhere and would render logs useless.
const minLiteralLength = 6

var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "apikey", "api_key",
	"authorization", "credential", "private_key", "session", "cookie",
	"dek", "master_key",
}

// Handler wraps another slog.Handler and redacts attribute values by key name
// and by registered literal.
type Handler struct {
	inner slog.Handler
	lits  *literals
}

// literals is shared by every Handler derived from one root, so a secret
// registered after startup is scrubbed by loggers already handed out.
type literals struct {
	mu     sync.RWMutex
	values []string
}

func (l *literals) add(vals ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range vals {
		if len(v) >= minLiteralLength {
			l.values = append(l.values, v)
		}
	}
}

func (l *literals) scrub(s string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, v := range l.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Marker)
		}
	}
	return s
}

// New wraps inner with redaction.
func New(inner slog.Handler) *Handler {
	return &Handler{inner: inner, lits: &literals{}}
}

// Register adds literal secret values to be scrubbed wherever they appear.
// Call it as credentials are resolved: a value can escape inside a message
// string that key-name rules never inspect.
func (h *Handler) Register(values ...string) {
	h.lits.add(values...)
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.lits.scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.redact(a, false))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = h.redact(a, false)
	}
	return &Handler{inner: h.inner.WithAttrs(redacted), lits: h.lits}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), lits: h.lits}
}

func (h *Handler) redact(a slog.Attr, parentSensitive bool) slog.Attr {
	sensitive := parentSensitive || isSensitiveKey(a.Key)

	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		out := make([]any, 0, len(attrs))
		for _, sub := range attrs {
			out = append(out, h.redact(sub, sensitive))
		}
		return slog.Group(a.Key, out...)
	}

	if sensitive {
		return slog.String(a.Key, Marker)
	}

	// Resolve LogValuer and stringify, so a secret embedded in a formatted
	// error or a struct's String() is still caught by literal matching.
	v := a.Value.Resolve()
	s := v.String()
	if scrubbed := h.lits.scrub(s); scrubbed != s {
		return slog.String(a.Key, scrubbed)
	}
	return slog.Attr{Key: a.Key, Value: v}
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
