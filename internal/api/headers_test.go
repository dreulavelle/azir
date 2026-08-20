package api_test

import (
	"net/http"
	"strings"
	"testing"
)

/*
The headers are on every answer, not on the ones somebody remembered.

Registered as a wrapper around the whole mux, so the way this breaks is not a
route that forgot them — it is the wrapper being dropped during some later
refactor and nothing saying so. Hence checking a handful of unrelated paths
rather than one.
*/
func TestEveryAnswerCarriesTheHeaders(t *testing.T) {
	srv, client := server(t)

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
	}

	// An open route, a guarded one, and one that does not exist: the wrapper
	// sits outside all of them.
	for _, path := range []string{"/healthz", "/api/setup", "/api/me", "/api/nothing-here"} {
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()

		for header, value := range want {
			if got := res.Header.Get(header); got != value {
				t.Errorf("%s: %s was %q, wanted %q", path, header, got, value)
			}
		}
		if res.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no content security policy", path)
		}
	}
}

/*
Script has no way in.

'unsafe-inline' is present for styles because the component library writes
style attributes at runtime and there is no version of this that works without
it. On script it would undo the point of having a policy at all, so the two are
asserted apart rather than the policy being compared as one string — which
would only ever be rewritten to match whatever it had become.
*/
func TestThePolicyDoesNotLetScriptInline(t *testing.T) {
	srv, client := server(t)

	res, err := client.Get(srv.URL + "/api/setup")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	policy := res.Header.Get("Content-Security-Policy")
	directives := map[string]string{}
	for _, part := range strings.Split(policy, ";") {
		name, rest, _ := strings.Cut(strings.TrimSpace(part), " ")
		directives[name] = rest
	}

	script, ok := directives["script-src"]
	if !ok {
		t.Fatalf("no script-src in %q", policy)
	}
	for _, escape := range []string{"'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(script, escape) {
			t.Errorf("script-src allows %s: %q", escape, script)
		}
	}

	// Remote images are what an answer from the model would use to carry
	// something it had read off this origin.
	if img := directives["img-src"]; strings.Contains(img, "http") || strings.Contains(img, "*") {
		t.Errorf("img-src reaches off-origin: %q", img)
	}

	for _, directive := range []string{"frame-ancestors", "object-src", "base-uri"} {
		if got := directives[directive]; got != "'none'" {
			t.Errorf("%s was %q, wanted 'none'", directive, got)
		}
	}
}

/*
No HSTS on a plain-HTTP answer.

A browser ignores it there anyway, so this is about not pinning a name to HTTPS
on the strength of a request that never proved it could serve it — and about
includeSubDomains staying absent, which would reach names Azir has never heard
of and cannot fix.
*/
/*
Strict-Transport-Security is sent only when the connection really was
encrypted, and a request does not get to say that it was.

This harness names no trusted proxy, which is the default deployment. So
X-Forwarded-Proto here is a header a stranger wrote, and answering it with HSTS
would mean pinning a browser to HTTPS on the say-so of the browser. The case
that does send it — a genuine TLS-terminating proxy, named in
AZIR_TRUSTED_PROXIES — is TestStrictTransportBehindATrustedProxy in proxy_test.go,
which can set the trust boundary this harness deliberately leaves empty.
*/
func TestStrictTransportOnlyWhenItIsTrue(t *testing.T) {
	srv, client := server(t)

	res, err := client.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain http answered with %q", got)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if got := res.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("an unverifiable X-Forwarded-Proto was answered with %q", got)
	}
}
