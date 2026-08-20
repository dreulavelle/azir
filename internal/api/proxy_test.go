package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func mustTrust(t *testing.T, spec string) ProxyTrust {
	t.Helper()
	trust, err := TrustedProxies(spec)
	if err != nil {
		t.Fatalf("TrustedProxies(%q): %v", spec, err)
	}
	return trust
}

// With nothing configured, a forwarding header is just something a stranger
// wrote. This is the default, so it is the case that matters most.
func TestForwardedHeadersAreIgnoredByDefault(t *testing.T) {
	var trust ProxyTrust // the zero value: trust nobody

	got := trust.clientIP(request("203.0.113.9:44321", map[string]string{
		"X-Forwarded-For": "198.51.100.7",
	}))
	if got != "203.0.113.9" {
		t.Fatalf("a forged X-Forwarded-For was believed: got %q", got)
	}

	if trust.overTLS(request("203.0.113.9:44321", map[string]string{
		"X-Forwarded-Proto": "https",
	})) {
		t.Fatal("a forged X-Forwarded-Proto was believed")
	}
}

func TestForwardedHeadersAreBelievedFromATrustedProxy(t *testing.T) {
	trust := mustTrust(t, "10.0.0.0/8, 192.168.1.5")

	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"single hop", "10.0.0.2:5000", "198.51.100.7", "198.51.100.7"},
		{"bare address in the list", "192.168.1.5:5000", "198.51.100.7", "198.51.100.7"},
		{"whitespace around hops", "10.0.0.2:5000", "  198.51.100.7  ", "198.51.100.7"},
		{"ipv6 client", "10.0.0.2:5000", "2001:db8::1", "2001:db8::1"},

		// The rightmost hop that is not itself a proxy. A client sending its
		// own X-Forwarded-For has that value preserved at the front, so
		// reading from the left would return whatever it chose to claim.
		{"client forged a leading hop", "10.0.0.2:5000", "1.2.3.4, 198.51.100.7", "198.51.100.7"},
		{"chained proxies", "10.0.0.2:5000", "198.51.100.7, 10.0.0.9, 10.0.0.2", "198.51.100.7"},

		// Nothing usable: fall back to the address the packets came from
		// rather than inventing one.
		{"no header", "10.0.0.2:5000", "", "10.0.0.2"},
		{"every hop is a proxy", "10.0.0.2:5000", "10.0.0.9, 10.0.0.2", "10.0.0.2"},
		{"unparseable hop", "10.0.0.2:5000", "not-an-address", "10.0.0.2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.forwarded != "" {
				headers["X-Forwarded-For"] = tc.forwarded
			}
			if got := trust.clientIP(request(tc.remoteAddr, headers)); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// A proxy being trusted to say where a request came from does not make an
// address outside the list trusted to say the same thing.
func TestAnUntrustedHopCannotBorrowTrust(t *testing.T) {
	trust := mustTrust(t, "10.0.0.0/8")

	got := trust.clientIP(request("203.0.113.9:44321", map[string]string{
		"X-Forwarded-For": "198.51.100.7, 10.0.0.2",
	}))
	if got != "203.0.113.9" {
		t.Fatalf("naming a trusted proxy inside the header bought trust: got %q", got)
	}
}

func TestOverTLSFollowsTheSameTrustBoundary(t *testing.T) {
	trust := mustTrust(t, "10.0.0.0/8")

	if !trust.overTLS(request("10.0.0.2:5000", map[string]string{"X-Forwarded-Proto": "https"})) {
		t.Error("a trusted proxy's X-Forwarded-Proto was not believed")
	}
	if !trust.overTLS(request("10.0.0.2:5000", map[string]string{"X-Forwarded-Proto": "HTTPS"})) {
		t.Error("the scheme comparison is case-sensitive; it should not be")
	}
	if trust.overTLS(request("10.0.0.2:5000", map[string]string{"X-Forwarded-Proto": "http"})) {
		t.Error("plain http was reported as TLS")
	}
	if trust.overTLS(request("203.0.113.9:44321", map[string]string{"X-Forwarded-Proto": "https"})) {
		t.Error("an untrusted client talked its way into the Secure cookie flag")
	}
}

func TestTrustedProxiesRejectsNonsense(t *testing.T) {
	if _, err := TrustedProxies("not-an-address"); err == nil {
		t.Error("a malformed entry was accepted; a deployment would start with a trust list it did not mean")
	}
	// Empty and whitespace-only specs are the ordinary "not behind a proxy"
	// case, not a configuration error.
	for _, spec := range []string{"", "   ", ",", " , "} {
		trust, err := TrustedProxies(spec)
		if err != nil {
			t.Errorf("TrustedProxies(%q) failed: %v", spec, err)
		}
		if len(trust.nets) != 0 {
			t.Errorf("TrustedProxies(%q) trusts something", spec)
		}
	}
}

// --- throttle ----------------------------------------------------------------

func TestThrottleAllowsABurstThenPaces(t *testing.T) {
	now := time.Now()
	th := newThrottle()
	th.nowFunc = func() time.Time { return now }

	for i := range loginBurst {
		if !th.allow("198.51.100.7") {
			t.Fatalf("attempt %d refused inside the burst; a mistyped password must not lock anyone out", i+1)
		}
	}
	if th.allow("198.51.100.7") {
		t.Fatal("the burst was not a limit; guessing is unbounded")
	}

	// A different source is unaffected: one person getting their password
	// wrong must not stop the rest of the company signing in.
	if !th.allow("198.51.100.8") {
		t.Fatal("one source's attempts throttled another's")
	}

	// The bucket refills with time.
	now = now.Add(time.Minute)
	if !th.allow("198.51.100.7") {
		t.Fatal("the limiter never refilled; a source is locked out permanently")
	}
}

// Sources are forgotten once they go quiet, so attempts from many addresses
// cannot grow the map without bound.
func TestThrottleEvictsIdleSources(t *testing.T) {
	now := time.Now()
	th := newThrottle()
	th.nowFunc = func() time.Time { return now }

	for i := range 500 {
		th.allow(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	before := len(th.perIP)
	if before == 0 {
		t.Fatal("nothing was recorded; the test proves nothing")
	}

	now = now.Add(2 * throttleIdle)
	th.allow("198.51.100.7")

	if len(th.perIP) != 1 {
		t.Fatalf("idle sources were not evicted: %d entries remain of %d", len(th.perIP), before)
	}
}

// The limiter keys on the trusted client address, so a header cannot be used
// to get a fresh allowance. This is why the proxy trust had to come first.
func TestThrottleCannotBeResetWithAForgedHeader(t *testing.T) {
	s := &Server{Throttle: newThrottle()} // no trusted proxies

	var served int
	h := s.limited(func(http.ResponseWriter, *http.Request) { served++ })

	for i := range loginBurst + 20 {
		rec := httptest.NewRecorder()
		// A different claimed address every time.
		h(rec, request("203.0.113.9:44321", map[string]string{
			"X-Forwarded-For": "198.51.100." + string(rune('0'+i%10)),
		}))
	}

	if served > loginBurst {
		t.Fatalf("rotating X-Forwarded-For bought %d attempts past a burst of %d", served, loginBurst)
	}
}

func TestThrottleRefusalSaysNothingAboutTheAccount(t *testing.T) {
	s := &Server{Throttle: newThrottle()}
	h := s.limited(func(http.ResponseWriter, *http.Request) {})

	var last *httptest.ResponseRecorder
	for range loginBurst + 1 {
		last = httptest.NewRecorder()
		h(last, request("203.0.113.9:44321", nil))
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", last.Code)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After; a well-behaved client has nothing to wait for")
	}
}

// A nil throttle must not panic. Tests build Servers directly, and a handler
// that crashes when a field is unset is a trap for the next person.
func TestNilThrottleAllows(t *testing.T) {
	var th *throttle
	if !th.allow("198.51.100.7") {
		t.Fatal("a nil throttle refused a request")
	}
}

// The other half of TestStrictTransportOnlyWhenItIsTrue: behind a proxy that
// was actually named, the proxy's account of the original scheme is believed
// and HSTS is sent.
func TestStrictTransportBehindATrustedProxy(t *testing.T) {
	trust := mustTrust(t, "10.0.0.0/8")
	h := secured(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), trust)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("10.0.0.2:5000", map[string]string{"X-Forwarded-Proto": "https"}))

	hsts := rec.Header().Get("Strict-Transport-Security")
	if hsts == "" {
		t.Fatal("nothing behind a tls-terminating proxy")
	}
	if strings.Contains(hsts, "includeSubDomains") {
		t.Errorf("reaches other names under the same domain: %q", hsts)
	}

	// The same claim from outside the trusted range gets nothing.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, request("203.0.113.9:44321", map[string]string{"X-Forwarded-Proto": "https"}))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("an untrusted client was answered with %q", got)
	}
}
