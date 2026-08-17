package oidc_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dreulavelle/azir/internal/oidc"
	"github.com/dreulavelle/azir/internal/store"
)

const (
	testTenant   = "11111111-2222-3333-4444-555555555555"
	otherTenant  = "99999999-8888-7777-6666-555555555555"
	testClientID = "azir-test-client"
)

// fakeProvider is an OpenID Connect provider that signs whatever it is told to.
//
// A real tenant cannot be used in a test, and the properties worth guarding —
// the tenant check, the nonce check, signature verification — are exactly the
// ones a mock of this package would assume rather than exercise.
type fakeProvider struct {
	*httptest.Server
	key *rsa.PrivateKey

	// claims the token endpoint will mint on the next exchange.
	claims map[string]any
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"jwks_uri":                              f.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"access_token": "unused", "token_type": "Bearer",
			"id_token": f.sign(t, f.claims),
		})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// sign mints an RS256 JWT. Written out rather than pulled from a helper so the
// test cannot accidentally depend on the same code path it is checking.
func (f *fakeProvider) sign(t *testing.T, claims map[string]any) string {
	t.Helper()

	full := map[string]any{
		"iss": f.URL, "aud": testClientID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	for k, v := range claims {
		full[k] = v
	}

	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}

	enc := base64.RawURLEncoding.EncodeToString
	signing := enc(header) + "." + enc(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + enc(sig)
}

func (f *fakeProvider) provider(t *testing.T, tenant string) *oidc.Provider {
	t.Helper()
	p, err := oidc.Discover(context.Background(), store.AuthConfig{
		Issuer: f.URL, ClientID: testClientID, TenantID: tenant,
	}, "test-secret", "http://azir.test/api/auth/oidc/callback")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	return p
}

// An Entra application configured against a shared endpoint will authenticate
// any Microsoft account in existence. The tenant claim is what keeps sign-in to
// one directory, and it is the single most consequential check in this package.
func TestTokenFromAnotherDirectoryIsRefused(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	_, state, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	f.claims = map[string]any{
		"nonce": state.Nonce, "oid": "someone-elses-user",
		"tid": otherTenant, "email": "attacker@evil.example", "name": "Not Yours",
	}

	if _, err := p.Exchange(context.Background(), "code", state); err == nil {
		t.Fatal("a token from a different directory was accepted")
	} else if !strings.Contains(err.Error(), "different directory") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

func TestTokenFromTheConfiguredDirectoryIsAccepted(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	_, state, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	f.claims = map[string]any{
		"nonce": state.Nonce, "oid": "user-object-id", "tid": testTenant,
		"email": "Person@Cooli.ai", "name": "A Person",
	}

	who, err := p.Exchange(context.Background(), "code", state)
	if err != nil {
		t.Fatalf("a valid token was refused: %v", err)
	}
	if who.Subject != "user-object-id" {
		t.Errorf("subject is %q; the stable oid claim should be preferred", who.Subject)
	}
	if who.Email != "person@cooli.ai" {
		t.Errorf("email is %q, want it normalised to lowercase", who.Email)
	}
	if who.DisplayName != "A Person" {
		t.Errorf("display name is %q", who.DisplayName)
	}
}

// A token obtained during someone else's sign-in must not be usable here.
func TestTokenFromADifferentSignInIsRefused(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	_, state, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	f.claims = map[string]any{
		"nonce": "a-nonce-from-some-other-attempt", "oid": "u", "tid": testTenant,
		"email": "person@cooli.ai",
	}

	if _, err := p.Exchange(context.Background(), "code", state); err == nil {
		t.Fatal("a token minted for a different sign-in was accepted")
	}
}

// Entra populates the email claim only when the directory has a mail
// attribute, so an account without one must still be able to sign in.
func TestSignInNameIsUsedWhenThereIsNoEmailClaim(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	_, state, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	f.claims = map[string]any{
		"nonce": state.Nonce, "oid": "u", "tid": testTenant,
		"preferred_username": "tech@cooli.ai",
	}

	who, err := p.Exchange(context.Background(), "code", state)
	if err != nil {
		t.Fatalf("an account with no mail attribute could not sign in: %v", err)
	}
	if who.Email != "tech@cooli.ai" {
		t.Errorf("email is %q, want the sign-in name", who.Email)
	}
	if who.DisplayName != "tech@cooli.ai" {
		t.Errorf("display name is %q, want a fallback rather than empty", who.DisplayName)
	}
}

// A token signed by anyone other than the provider is worthless.
func TestATokenSignedByTheWrongKeyIsRefused(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	_, state, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	f.claims = map[string]any{"nonce": state.Nonce, "oid": "u", "tid": testTenant, "email": "a@b.c"}

	// Swap the signing key after discovery cached the published one.
	impostor, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.key = impostor

	if _, err := p.Exchange(context.Background(), "code", state); err == nil {
		t.Fatal("a token signed by an unknown key was accepted")
	}
}

func TestStartProducesAFreshStateAndNonceEveryTime(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	seen := map[string]bool{}
	for range 20 {
		_, state, err := p.Start()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{state.State, state.Nonce, state.Verifier} {
			if value == "" {
				t.Fatal("a sign-in was started with an empty state, nonce or verifier")
			}
			if seen[value] {
				t.Fatalf("a value was reused across sign-ins: %q", value)
			}
			seen[value] = true
		}
	}
}

// PKCE must actually be on the wire; a challenge that is never sent protects
// nothing, and its absence is invisible without checking.
func TestAuthorizationRequestCarriesAPKCEChallenge(t *testing.T) {
	f := newFakeProvider(t)
	p := f.provider(t, testTenant)

	url, _, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"code_challenge=", "code_challenge_method=S256", "nonce=", "state="} {
		if !strings.Contains(url, want) {
			t.Errorf("the authorization url is missing %s: %s", want, url)
		}
	}
}

func TestAllowedDomains(t *testing.T) {
	f := newFakeProvider(t)

	build := func(domains ...string) *oidc.Provider {
		p, err := oidc.Discover(context.Background(), store.AuthConfig{
			Issuer: f.URL, ClientID: testClientID, AllowedDomains: domains,
		}, "s", "http://azir.test/cb")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// An empty list allows anyone the provider authenticated.
	if !build().PermittedDomain("anyone@anywhere.example") {
		t.Error("an empty domain list refused someone")
	}

	restricted := build("cooli.ai", "@example.com")
	for _, email := range []string{"a@cooli.ai", "A@COOLI.AI", "b@example.com"} {
		if !restricted.PermittedDomain(email) {
			t.Errorf("%s was refused but its domain is allowed", email)
		}
	}
	for _, email := range []string{"a@evil.example", "cooli.ai", "", "a@notcooli.ai"} {
		if restricted.PermittedDomain(email) {
			t.Errorf("%s was allowed but its domain is not", email)
		}
	}
}

func TestEntraIssuerNamesTheTenant(t *testing.T) {
	issuer := oidc.EntraIssuer(testTenant)
	if !strings.Contains(issuer, testTenant) {
		t.Errorf("issuer %q does not name the tenant", issuer)
	}
	// A shared endpoint would authenticate any Microsoft account there is.
	for _, shared := range []string{"/common/", "/organizations/", "/consumers/"} {
		if strings.Contains(issuer, shared) {
			t.Errorf("issuer %q uses the shared %s endpoint", issuer, shared)
		}
	}
}
