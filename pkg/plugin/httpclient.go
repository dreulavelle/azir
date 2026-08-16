package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// ErrMethodNotAllowed means a request used a method this client does not
// permit. Azir is read-only, so the allowlist is the enforcement point for
// that invariant at the transport layer — below any tool-level check, and
// therefore below any mistake a plugin author can make.
var ErrMethodNotAllowed = errors.New("plugin: http method not permitted by the read-only invariant")

// ErrRateLimited means the vendor returned 429 and retries were exhausted.
var ErrRateLimited = errors.New("plugin: vendor rate limit exhausted")

// safeMethods are permitted unconditionally.
//
// This cannot simply be "GET only": real read APIs use POST for searches with
// complex filters, and some use it for login. Those are enumerated per client
// as exceptions rather than widening the default.
var safeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
}

// HTTPConfig configures a vendor client.
type HTTPConfig struct {
	// BaseURL is the API root. Requests are resolved against it.
	BaseURL string

	// RequestsPerMinute is the vendor's documented limit. The client paces
	// itself below it rather than discovering the limit by being throttled —
	// a 429 costs a round trip and, on some vendors, a lockout.
	RequestsPerMinute int

	// Burst allows short bursts within the overall rate. Zero picks a small
	// sensible default.
	Burst int

	// AllowedWritePaths enumerates (method, path-prefix) pairs that may use a
	// write-shaped method despite being reads — a search endpoint that takes
	// POST, or a login that mints a session token. Anything not listed is
	// refused.
	AllowedWritePaths []MethodPath

	// MaxRetries for 429 and 5xx responses.
	MaxRetries int

	// Timeout per attempt.
	Timeout time.Duration
}

// MethodPath is one allowlisted write-shaped read.
type MethodPath struct {
	Method string
	// Prefix matches the start of the request path.
	Prefix string
	// Why documents the exception, so a future reader can judge whether it is
	// still justified rather than assuming it always was.
	Why string
}

// HTTPClient is a rate-limited, read-only HTTP client for vendor APIs.
//
// It exists in the SDK rather than in each plugin because every plugin needs
// the same five behaviours — rate limiting, retry with backoff, method
// allowlisting, credential injection and error sanitisation — and because the
// read-only invariant is only an invariant if it is enforced in one place that
// plugin code cannot bypass.
type HTTPClient struct {
	base    *url.URL
	http    *http.Client
	limiter *rate.Limiter
	cfg     HTTPConfig

	// authorize installs credentials on each request. Held as a function so a
	// credential can be resolved per request and rotated without rebuilding
	// the client.
	authorize func(context.Context, *http.Request) error
}

// NewHTTPClient builds a client. authorize is called for every request and
// should set whatever header the vendor requires.
//
// Credentials belong in headers, never in the URL: query strings are logged by
// proxies, echoed in error messages, and retained in access logs long after
// anyone remembers they were sensitive.
func NewHTTPClient(cfg HTTPConfig, authorize func(context.Context, *http.Request) error) (*HTTPClient, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("plugin: BaseURL is required")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("plugin: BaseURL is not a valid URL")
	}
	if base.Scheme != "https" && base.Hostname() != "127.0.0.1" && base.Hostname() != "localhost" {
		return nil, errors.New("plugin: BaseURL must be https")
	}
	if cfg.RequestsPerMinute <= 0 {
		cfg.RequestsPerMinute = 60
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 5
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}

	return &HTTPClient{
		base: base,
		http: &http.Client{
			Timeout: cfg.Timeout,
			// Never follow a redirect to a different host: a redirect would
			// otherwise carry the Authorization header somewhere the operator
			// never configured.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					return errors.New("cross-host redirect refused")
				}
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
		limiter:   rate.NewLimiter(rate.Limit(float64(cfg.RequestsPerMinute)/60.0), cfg.Burst),
		cfg:       cfg,
		authorize: authorize,
	}, nil
}

// permitted reports whether a method and path may be sent.
func (c *HTTPClient) permitted(method, path string) bool {
	if safeMethods[method] {
		return true
	}
	for _, allowed := range c.cfg.AllowedWritePaths {
		if allowed.Method == method && strings.HasPrefix(path, allowed.Prefix) {
			return true
		}
	}
	return false
}

// Do sends a request, pacing and retrying as configured.
//
// The returned error never carries vendor detail: the caller may surface it,
// and vendor errors routinely contain request URLs and auth headers. Detail
// worth keeping is the caller's to log locally.
func (c *HTTPClient) Do(ctx context.Context, method, path string, query url.Values, body io.Reader) ([]byte, error) {
	if !c.permitted(method, path) {
		return nil, fmt.Errorf("%w: %s %s", ErrMethodNotAllowed, method, path)
	}

	target := c.base.JoinPath(path)
	if query != nil {
		target.RawQuery = query.Encode()
	}

	var lastStatus int
	for attempt := range c.cfg.MaxRetries + 1 {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
		if err != nil {
			return nil, errors.New("plugin: could not build request")
		}
		req.Header.Set("Accept", "application/json")
		if c.authorize != nil {
			if err := c.authorize(ctx, req); err != nil {
				return nil, err
			}
		}

		resp, err := c.http.Do(req)
		if err != nil {
			// A transport error's text can contain the full URL. Do not
			// propagate it.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == c.cfg.MaxRetries {
				return nil, errors.New("plugin: vendor request failed")
			}
			if !sleepFor(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
			continue
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		lastStatus = resp.StatusCode

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			// Respect Retry-After when the vendor sends one: guessing shorter
			// is how a client earns a longer ban.
			wait := backoff(attempt)
			if hinted := retryAfter(resp.Header.Get("Retry-After")); hinted > 0 {
				wait = hinted
			}
			if attempt == c.cfg.MaxRetries {
				return nil, ErrRateLimited
			}
			if !sleepFor(ctx, wait) {
				return nil, ctx.Err()
			}
			continue

		case resp.StatusCode >= 500:
			if attempt == c.cfg.MaxRetries {
				return nil, fmt.Errorf("plugin: vendor returned %d", resp.StatusCode)
			}
			if !sleepFor(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
			continue

		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			// Never retry an auth failure. Repeated bad credentials are how a
			// client gets its IP blacklisted, and a rotated password should
			// surface to a human rather than being hammered at.
			return nil, &Error{Code: strconv.Itoa(resp.StatusCode),
				Message: "the stored credential was rejected; check it in settings"}

		case resp.StatusCode >= 400:
			return nil, &Error{Code: strconv.Itoa(resp.StatusCode),
				Message: "the vendor rejected this request"}
		}

		if readErr != nil {
			return nil, errors.New("plugin: could not read vendor response")
		}
		return data, nil
	}
	return nil, fmt.Errorf("plugin: vendor returned %d", lastStatus)
}

// backoff grows exponentially with a cap.
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * time.Second
	return min(d, 30*time.Second)
}

func retryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return min(time.Duration(secs)*time.Second, 5*time.Minute)
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, 5*time.Minute)
		}
	}
	return 0
}

func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
