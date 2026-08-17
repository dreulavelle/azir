// Package oidc signs people in through an external identity provider.
//
// Microsoft Entra ID is the provider this was built against, but nothing here
// is specific to it beyond one check: Entra's tenant claim. The rest is plain
// OpenID Connect, so another provider is a different issuer URL rather than
// different code.
//
// The provider answers one question — who is this — and nothing else. What they
// may do is decided by Azir, from the role on their account. A directory group
// changing must never be able to move someone between roles here.
package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/dreulavelle/azir/internal/store"
)

// Provider is a configured connection to an identity provider.
type Provider struct {
	config   store.AuthConfig
	oidc     *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	oauth    oauth2.Config
}

// EntraIssuer builds the issuer URL for an Entra tenant.
//
// The tenant GUID is in the URL rather than using a shared endpoint, so the
// issuer this deployment trusts is one specific directory. A shared endpoint
// would authenticate any Microsoft account in the world, which is the single
// most common way to get an Entra integration wrong.
func EntraIssuer(tenantID string) string {
	return "https://login.microsoftonline.com/" + strings.TrimSpace(tenantID) + "/v2.0"
}

// Discover contacts the provider and prepares the flow.
//
// Network work happens here, once, rather than on every sign-in. A provider
// that cannot be reached fails configuration rather than failing a person who
// is trying to get in.
func Discover(ctx context.Context, cfg store.AuthConfig, clientSecret, redirectURL string) (*Provider, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("oidc: no issuer is configured")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("oidc: no client id is configured")
	}
	if redirectURL == "" {
		return nil, errors.New("oidc: no redirect url could be determined")
	}

	discoverCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	p, err := gooidc.NewProvider(discoverCtx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: could not reach the identity provider: %w", err)
	}

	return &Provider{
		config:   cfg,
		oidc:     p,
		verifier: p.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: clientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  redirectURL,
			// email is requested but not relied upon: Entra populates it only
			// when the directory has one, so the identity below falls back to
			// the sign-in name.
			Scopes: []string{gooidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
}

// RedirectURL is where the provider will send the browser back to.
func (p *Provider) RedirectURL() string { return p.oauth.RedirectURL }

// Start returns the URL to send the browser to, and the values that must come
// back unchanged.
func (p *Provider) Start() (authURL string, state store.OIDCState, err error) {
	stateValue, err := randomToken()
	if err != nil {
		return "", store.OIDCState{}, err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", store.OIDCState{}, err
	}
	verifier := oauth2.GenerateVerifier()

	// PKCE, even though this is a confidential client with a secret. It costs
	// nothing and it protects the authorization code on the leg where it is
	// most exposed: a URL, which lands in browser history and proxy logs.
	url := p.oauth.AuthCodeURL(stateValue,
		gooidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
	return url, store.OIDCState{State: stateValue, Nonce: nonce, Verifier: verifier}, nil
}

// Identity is what the provider asserted about a person.
type Identity struct {
	// Subject is the provider's stable identifier. For Entra this is the oid
	// claim: the user's object id, which survives a rename.
	Subject     string
	Email       string
	DisplayName string
}

// entraClaims are the claims this code reads.
type entraClaims struct {
	// Entra's per-directory object id. Stable across renames, unlike sub,
	// which is per-application and would break if the app registration is
	// ever replaced.
	OID string `json:"oid"`
	TID string `json:"tid"`

	Email string `json:"email"`
	// The sign-in name. For Entra this is the UPN and is usually the only
	// address present, since the email claim requires a mail attribute.
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// Exchange completes a sign-in and returns who the provider says this is.
func (p *Provider) Exchange(ctx context.Context, code string, state store.OIDCState) (Identity, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(state.Verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("oidc: the provider rejected this sign-in: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return Identity{}, errors.New("oidc: the provider returned no id token")
	}

	// Signature, issuer, audience and expiry, against the provider's published
	// keys. Everything below this line depends on this having passed.
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Identity{}, fmt.Errorf("oidc: the id token did not verify: %w", err)
	}

	// The nonce ties this token to the sign-in that was started here. Without
	// it, a token obtained elsewhere could be replayed into this callback.
	if idToken.Nonce != state.Nonce {
		return Identity{}, errors.New("oidc: this token belongs to a different sign-in attempt")
	}

	var claims entraClaims
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("oidc: could not read the token claims: %w", err)
	}

	// The tenant check. Belt and braces alongside the issuer check above: if
	// the issuer is ever configured as a shared endpoint, this is the line
	// that still refuses an account from somebody else's directory.
	if p.config.TenantID != "" && !strings.EqualFold(claims.TID, p.config.TenantID) {
		return Identity{}, errors.New("oidc: this account belongs to a different directory")
	}

	subject := claims.OID
	if subject == "" {
		subject = idToken.Subject
	}

	email := strings.TrimSpace(strings.ToLower(claims.Email))
	if email == "" {
		email = strings.TrimSpace(strings.ToLower(claims.PreferredUsername))
	}
	if subject == "" || email == "" {
		return Identity{}, errors.New("oidc: the provider returned no usable identity")
	}

	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = email
	}
	return Identity{Subject: subject, Email: email, DisplayName: name}, nil
}

// PermittedDomain reports whether an address is in the allowed list. An empty
// list allows everything the provider authenticated, which is only reasonable
// when the provider is a single directory you control.
func (p *Provider) PermittedDomain(email string) bool {
	if len(p.config.AllowedDomains) == 0 {
		return true
	}
	_, domain, ok := strings.Cut(strings.ToLower(email), "@")
	if !ok {
		return false
	}
	for _, allowed := range p.config.AllowedDomains {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(allowed), "@"), domain) {
			return true
		}
	}
	return false
}

// Config returns the configuration this provider was built from.
func (p *Provider) Config() store.AuthConfig { return p.config }

// --- caching -----------------------------------------------------------------

// Cache holds the configured provider, rebuilding it when the configuration
// changes. Discovery is a network round trip and the result is stable, so doing
// it per sign-in would add latency and a dependency on the provider being
// reachable at exactly the wrong moment.
type Cache struct {
	mu      sync.Mutex
	current *Provider
	key     string
}

// Get returns a provider for this configuration, discovering only when
// something that matters has changed.
func (c *Cache) Get(ctx context.Context, cfg store.AuthConfig, clientSecret, redirectURL string) (*Provider, error) {
	key := strings.Join([]string{cfg.Issuer, cfg.ClientID, cfg.TenantID, redirectURL, clientSecret}, "\x00")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.current != nil && c.key == key {
		// Values that do not affect discovery are refreshed in place, so an
		// edit to the allowed domains takes effect without a round trip.
		c.current.config = cfg
		return c.current, nil
	}

	p, err := Discover(ctx, cfg, clientSecret, redirectURL)
	if err != nil {
		return nil, err
	}
	c.current, c.key = p, key
	return p, nil
}

// Forget drops the cached provider, so the next sign-in rediscovers.
func (c *Cache) Forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current, c.key = nil, ""
}

// randomToken returns an unguessable value for a state or nonce.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oidc: no randomness available: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
