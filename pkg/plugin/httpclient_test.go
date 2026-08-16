package plugin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dreulavelle/azir/pkg/plugin"
)

func newTestClient(t *testing.T, srv *httptest.Server, cfg plugin.HTTPConfig) *plugin.HTTPClient {
	t.Helper()
	cfg.BaseURL = srv.URL
	if cfg.RequestsPerMinute == 0 {
		cfg.RequestsPerMinute = 6000 // effectively unthrottled for tests
	}
	c, err := plugin.NewHTTPClient(cfg, func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer test-token-value")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The read-only invariant, enforced below any tool-level check.
func TestWriteMethodsAreRefused(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		_, err := c.Do(context.Background(), method, "/tickets", nil, nil)
		if !errors.Is(err, plugin.ErrMethodNotAllowed) {
			t.Errorf("%s was not refused: %v", method, err)
		}
	}
	if reached.Load() {
		t.Fatal("a write request reached the server; the invariant is not enforced at the transport")
	}
}

// Some genuine reads are POSTs — a filtered search, a login. Those are
// enumerated exceptions, not a reason to widen the default.
func TestAllowlistedWriteShapedReadIsPermitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{
		AllowedWritePaths: []plugin.MethodPath{
			{Method: http.MethodPost, Prefix: "/search", Why: "filtered search takes a body"},
		},
	})

	if _, err := c.Do(context.Background(), http.MethodPost, "/search", nil, strings.NewReader(`{}`)); err != nil {
		t.Fatalf("allowlisted POST was refused: %v", err)
	}
	// The exception must be narrow: a different path stays refused.
	if _, err := c.Do(context.Background(), http.MethodPost, "/tickets", nil, nil); !errors.Is(err, plugin.ErrMethodNotAllowed) {
		t.Errorf("allowlist leaked to another path: %v", err)
	}
}

// Repeated bad credentials are how a client earns an IP ban. An auth failure
// must surface to a human, not be retried.
func TestAuthFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{MaxRetries: 3})

	_, err := c.Do(context.Background(), http.MethodGet, "/tickets", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("auth failure was retried %d times; that is how an IP gets blacklisted", n-1)
	}

	var perr *plugin.Error
	if !errors.As(err, &perr) {
		t.Fatalf("want a *plugin.Error the caller can surface, got %T", err)
	}
	if !strings.Contains(perr.Message, "settings") {
		t.Errorf("error does not point the operator anywhere useful: %q", perr.Message)
	}
}

// 429 must be respected rather than hammered, and Retry-After honoured.
func TestRateLimitIsRespected(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{MaxRetries: 2})

	start := time.Now()
	body, err := c.Do(context.Background(), http.MethodGet, "/tickets", nil, nil)
	if err != nil {
		t.Fatalf("client gave up on a retryable 429: %v", err)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("unexpected body: %s", body)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("Retry-After was ignored; waited only %v", elapsed)
	}
}

// Vendor error bodies routinely contain request URLs and auth headers. None of
// it may reach the caller, which may surface the error into model context.
func TestVendorErrorDetailIsNotPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request to https://acme.syncromsp.com/api/v1?api_key=SUPERSECRET"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{})

	_, err := c.Do(context.Background(), http.MethodGet, "/tickets", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leaked := range []string{"SUPERSECRET", "acme.syncromsp.com", "api_key"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("vendor detail %q reached the caller: %v", leaked, err)
		}
	}
}

// A redirect must not carry the Authorization header to a host the operator
// never configured.
func TestCrossHostRedirectIsRefused(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			evilHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{MaxRetries: 0})

	if _, err := c.Do(context.Background(), http.MethodGet, "/tickets", nil, nil); err == nil {
		t.Error("cross-host redirect was followed")
	}
	if evilHits.Load() > 0 {
		t.Fatal("Authorization header was carried across a redirect to another host")
	}
}

// Rate limiting must actually pace requests, not merely be configured.
func TestClientPacesItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// 60/min = 1/sec, burst 1: the second request must wait about a second.
	c := newTestClient(t, srv, plugin.HTTPConfig{RequestsPerMinute: 60, Burst: 1})

	ctx := context.Background()
	start := time.Now()
	for range 2 {
		if _, err := c.Do(ctx, http.MethodGet, "/x", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("client did not pace itself: two requests took %v", elapsed)
	}
}

// http, not https, is refused for anything but loopback.
func TestPlaintextBaseURLIsRefused(t *testing.T) {
	_, err := plugin.NewHTTPClient(plugin.HTTPConfig{BaseURL: "http://acme.syncromsp.com/api/v1"}, nil)
	if err == nil {
		t.Fatal("a plaintext base URL was accepted; credentials would travel in the clear")
	}
}

// A retry must resend the body. An io.Reader is consumed by the first attempt,
// so a naive implementation delivers an empty body on the retry — silently
// returning wrong results rather than failing, which is worse than an error.
func TestRetriedRequestKeepsItsBody(t *testing.T) {
	var attempts atomic.Int32
	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{
		MaxRetries:        2,
		AllowedWritePaths: []plugin.MethodPath{{Method: http.MethodPost, Prefix: "/search"}},
	})

	const payload = `{"query":"acme"}`
	if _, err := c.Do(context.Background(), http.MethodPost, "/search", nil,
		strings.NewReader(payload)); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("expected a retry, saw %d attempts", len(bodies))
	}
	for i, b := range bodies {
		if b != payload {
			t.Errorf("attempt %d sent %q, want %q", i+1, b, payload)
		}
	}
}

// An allowlisted prefix must not leak across a path boundary.
func TestWriteAllowlistRespectsPathBoundaries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, plugin.HTTPConfig{
		AllowedWritePaths: []plugin.MethodPath{{Method: http.MethodPost, Prefix: "/search"}},
	})

	ctx := context.Background()
	if _, err := c.Do(ctx, http.MethodPost, "/search/tickets", nil, nil); err != nil {
		t.Errorf("a path under the allowlisted prefix was refused: %v", err)
	}
	if _, err := c.Do(ctx, http.MethodPost, "/searchable-write", nil, nil); !errors.Is(err, plugin.ErrMethodNotAllowed) {
		t.Errorf("allowlist leaked across a path boundary: %v", err)
	}
}
